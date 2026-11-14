package llamacpp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
)

// ServerSpec descreve como subir um llama-server.
type ServerSpec struct {
	Binary       string        // caminho do llama-server
	Model        string        // caminho do GGUF
	BaseURL      string        // de onde vêm host e porta (sempre loopback)
	ContextSize  int           // -c; 0 = o do modelo
	Alias        string        // nome que o servidor informa
	Args         []string      // argumentos extras
	LogPath      string        // saída do llama-server; vazio = descartada
	StartTimeout time.Duration // espera pelo /health; 0 = 3 min
}

// Process é um llama-server iniciado pelo Nexus.
type Process struct {
	cmd  *exec.Cmd
	done chan struct{}
	err  error
	log  *os.File
}

// Start sobe o servidor e espera ele responder /health. Se o processo morrer
// antes, o erro traz o fim do log dele.
func Start(ctx context.Context, spec ServerSpec) (*Process, error) {
	u, err := url.Parse(spec.BaseURL)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(spec.Model); err != nil {
		return nil, fmt.Errorf("modelo não encontrado: %w", err)
	}
	args := []string{"-m", spec.Model, "--host", u.Hostname(), "--port", u.Port(), "-np", "1", "--no-webui"}
	if spec.ContextSize > 0 {
		args = append(args, "-c", fmt.Sprint(spec.ContextSize))
	}
	if spec.Alias != "" {
		args = append(args, "--alias", spec.Alias)
	}
	args = append(args, spec.Args...)

	cmd := exec.Command(spec.Binary, args...)
	cmd.SysProcAttr = sysProcAttr()
	p := &Process{cmd: cmd, done: make(chan struct{})}
	if spec.LogPath != "" {
		if err := os.MkdirAll(filepath.Dir(spec.LogPath), 0o700); err != nil {
			return nil, err
		}
		f, err := os.OpenFile(spec.LogPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
		if err != nil {
			return nil, err
		}
		p.log = f
		cmd.Stdout, cmd.Stderr = f, f
	}
	if err := cmd.Start(); err != nil {
		p.closeLog()
		return nil, fmt.Errorf("iniciar llama-server: %w", err)
	}
	go func() {
		p.err = cmd.Wait()
		p.closeLog()
		close(p.done)
	}()

	timeout := spec.StartTimeout
	if timeout <= 0 {
		timeout = 3 * time.Minute
	}
	wctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	client := New(spec.BaseURL, "")
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()
	for {
		hctx, hcancel := context.WithTimeout(wctx, 2*time.Second)
		herr := client.Health(hctx)
		hcancel()
		if herr == nil {
			return p, nil
		}
		select {
		case <-p.done:
			return nil, fmt.Errorf("o llama-server encerrou ao iniciar (%v)%s", p.err, tail(spec.LogPath, 8))
		case <-wctx.Done():
			_ = p.Stop()
			return nil, fmt.Errorf("o llama-server não ficou pronto em %s%s", timeout, tail(spec.LogPath, 8))
		case <-tick.C:
		}
	}
}

// Stop encerra com SIGTERM e, se não sair em 10 s, com SIGKILL.
func (p *Process) Stop() error {
	if p == nil || p.cmd.Process == nil {
		return nil
	}
	select {
	case <-p.done:
		return nil
	default:
	}
	_ = p.cmd.Process.Signal(syscall.SIGTERM)
	select {
	case <-p.done:
		return nil
	case <-time.After(10 * time.Second):
		_ = p.cmd.Process.Kill()
		<-p.done
		return errors.New("o llama-server não saiu com SIGTERM; foi encerrado à força")
	}
}

// PID devolve o pid do processo.
func (p *Process) PID() int { return p.cmd.Process.Pid }

func (p *Process) closeLog() {
	if p.log != nil {
		p.log.Close()
	}
}

func tail(path string, lines int) string {
	if path == "" {
		return ""
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	all := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(all) > lines {
		all = all[len(all)-lines:]
	}
	return "\nÚltimas linhas do log (" + path + "):\n" + strings.Join(all, "\n")
}

// FindBinary procura o llama-server no PATH e nos lugares comuns de build.
func FindBinary(home string) string {
	if p, err := exec.LookPath("llama-server"); err == nil {
		return p
	}
	if home == "" {
		return ""
	}
	for _, dir := range []string{"llama.cpp/build-cuda/bin", "llama.cpp/build-vulkan/bin", "llama.cpp/build/bin"} {
		p := filepath.Join(home, dir, "llama-server")
		if st, err := os.Stat(p); err == nil && !st.IsDir() && st.Mode()&0o111 != 0 {
			return p
		}
	}
	return ""
}

// ModelFile é um GGUF achado no disco.
type ModelFile struct {
	Path string
	Size int64
}

// Name é o nome do arquivo sem a extensão.
func (m ModelFile) Name() string {
	return strings.TrimSuffix(filepath.Base(m.Path), filepath.Ext(m.Path))
}

// FindModels lista os GGUF de chat nas pastas comuns, do maior para o menor.
// Arquivos de vocabulário e de embeddings ficam de fora.
func FindModels(home string) []ModelFile {
	if home == "" {
		return nil
	}
	var out []ModelFile
	seen := map[string]bool{}
	for _, dir := range []string{"llama.cpp/models", "models", ".cache/llama.cpp", "Modelos", "Downloads"} {
		entries, err := os.ReadDir(filepath.Join(home, dir))
		if err != nil {
			continue
		}
		for _, e := range entries {
			name := strings.ToLower(e.Name())
			if e.IsDir() || !strings.HasSuffix(name, ".gguf") || strings.Contains(name, "vocab") || strings.Contains(name, "embed") || strings.Contains(name, "mmproj") {
				continue
			}
			p := filepath.Join(home, dir, e.Name())
			info, err := e.Info()
			if err != nil || info.Size() < 100<<20 || seen[p] {
				continue
			}
			seen[p] = true
			out = append(out, ModelFile{Path: p, Size: info.Size()})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Size > out[j].Size })
	return out
}

// VerifyChecksum confere o SHA-256 do GGUF. O resultado fica em cache por
// caminho, tamanho e data de modificação, para não reler gigabytes a cada início.
func VerifyChecksum(path, want, cacheDir string) error {
	want = strings.ToLower(strings.TrimSpace(want))
	if want == "" {
		return nil
	}
	st, err := os.Stat(path)
	if err != nil {
		return err
	}
	key := fmt.Sprintf("%s|%d|%d", path, st.Size(), st.ModTime().UnixNano())
	cacheFile := filepath.Join(cacheDir, "gguf-checksums.json")
	cache := map[string]string{}
	if cacheDir != "" {
		if data, err := os.ReadFile(cacheFile); err == nil {
			_ = json.Unmarshal(data, &cache)
		}
	}
	got, ok := cache[key]
	if !ok {
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		h := sha256.New()
		_, err = io.Copy(h, f)
		f.Close()
		if err != nil {
			return err
		}
		got = hex.EncodeToString(h.Sum(nil))
		if cacheDir != "" {
			cache[key] = got
			if data, err := json.Marshal(cache); err == nil {
				_ = os.MkdirAll(cacheDir, 0o700)
				_ = os.WriteFile(cacheFile, data, 0o600)
			}
		}
	}
	if got != want {
		return fmt.Errorf("checksum do modelo não confere: esperado %s, veio %s (%s)", want, got, path)
	}
	return nil
}
