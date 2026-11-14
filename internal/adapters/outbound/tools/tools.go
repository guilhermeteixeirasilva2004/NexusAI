// Package tools implementa as ferramentas do Nexus. As de arquivo ficam
// presas ao diretório de trabalho; as de memória falam com ports.MemoryStore.
package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"nexusai/internal/core/domain"
	"nexusai/internal/core/ports"
)

// Limites das ferramentas de arquivo.
const (
	MaxReadBytes   = 256 << 10
	MaxDirEntries  = 200
	maxMemoryChars = 500
)

// All devolve as ferramentas da Fase 1, com as de arquivo presas a workdir.
func All(workdir string, mem ports.MemoryStore) ([]ports.Tool, error) {
	fs, err := NewSandbox(workdir)
	if err != nil {
		return nil, err
	}
	return []ports.Tool{ReadFile{fs}, ListDir{fs}, MemorySearch{mem}, MemorySave{mem}}, nil
}

// Sandbox resolve caminhos dentro de um diretório raiz, seguindo symlinks,
// e recusa arquivos com cara de segredo.
type Sandbox struct {
	root string // absoluto e sem symlinks
}

// NewSandbox cria uma sandbox em root.
func NewSandbox(root string) (Sandbox, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return Sandbox{}, err
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return Sandbox{}, fmt.Errorf("diretório de trabalho: %w", err)
	}
	return Sandbox{root: real}, nil
}

// Root devolve a raiz da sandbox.
func (s Sandbox) Root() string { return s.root }

// ErrOutside indica um caminho fora do diretório de trabalho.
var ErrOutside = errors.New("fora do diretório de trabalho")

// ErrSensitive indica um arquivo que pode guardar segredos.
var ErrSensitive = errors.New("arquivo protegido (pode conter segredos)")

// Resolve devolve o caminho real de p, garantindo que fica na raiz.
func (s Sandbox) Resolve(p string) (string, error) {
	if strings.ContainsRune(p, 0) {
		return "", errors.New("caminho inválido")
	}
	if p == "" || p == "~" {
		p = "."
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(s.root, p)
	}
	p = filepath.Clean(p)
	real, err := filepath.EvalSymlinks(p)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			if !s.inside(p) {
				return "", ErrOutside
			}
			return "", fmt.Errorf("%s não existe", s.Rel(p))
		}
		return "", err
	}
	if !s.inside(real) {
		return "", ErrOutside
	}
	if sensitive(s.Rel(real)) {
		return "", ErrSensitive
	}
	return real, nil
}

func (s Sandbox) inside(p string) bool {
	rel, err := filepath.Rel(s.root, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// Rel mostra p relativo à raiz, para mensagens.
func (s Sandbox) Rel(p string) string {
	rel, err := filepath.Rel(s.root, p)
	if err != nil {
		return p
	}
	return filepath.ToSlash(rel)
}

var sensitiveDirs = map[string]bool{".ssh": true, ".gnupg": true, ".aws": true, ".kube": true, ".docker": true, ".password-store": true}

var sensitiveFiles = map[string]bool{
	".env": true, ".netrc": true, ".git-credentials": true, ".pgpass": true, "credentials": true,
	"id_rsa": true, "id_ed25519": true, "id_ecdsa": true, "id_dsa": true, "nexus.db": true,
}

func sensitive(rel string) bool {
	parts := strings.Split(rel, "/")
	for _, part := range parts[:len(parts)-1] {
		if sensitiveDirs[part] {
			return true
		}
	}
	base := strings.ToLower(parts[len(parts)-1])
	if sensitiveDirs[base] || sensitiveFiles[base] || strings.HasPrefix(base, ".env.") {
		return true
	}
	for _, ext := range []string{".pem", ".key", ".p12", ".pfx", ".kdbx"} {
		if strings.HasSuffix(base, ext) {
			return true
		}
	}
	return false
}

func decode(args json.RawMessage, v any) error {
	if len(bytes.TrimSpace(args)) == 0 {
		args = json.RawMessage(`{}`)
	}
	return json.Unmarshal(args, v)
}

// ReadFile lê um arquivo de texto do diretório de trabalho.
type ReadFile struct{ fs Sandbox }

// Spec implementa ports.Tool.
func (ReadFile) Spec() domain.ToolSpec {
	return domain.ToolSpec{
		Name:        "read_file",
		Description: "Lê um arquivo de texto do diretório de trabalho e devolve o conteúdo.",
		Params:      json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","minLength":1,"maxLength":1024,"description":"caminho relativo ao diretório de trabalho"}},"required":["path"],"additionalProperties":false}`),
		Risk:        domain.RiskRead,
	}
}

// Summary é a linha mostrada na tela.
func (ReadFile) Summary(args []byte) string {
	var a struct{ Path string }
	_ = decode(args, &a)
	return "lendo " + a.Path
}

// Run implementa ports.Tool.
func (t ReadFile) Run(ctx context.Context, args json.RawMessage) (domain.ToolResult, error) {
	var a struct{ Path string }
	if err := decode(args, &a); err != nil {
		return domain.ToolResult{}, err
	}
	p, err := t.fs.Resolve(a.Path)
	if err != nil {
		return domain.ToolResult{}, err
	}
	st, err := os.Stat(p)
	if err != nil {
		return domain.ToolResult{}, err
	}
	if st.IsDir() {
		return domain.ToolResult{}, fmt.Errorf("%s é uma pasta; use list_dir", t.fs.Rel(p))
	}
	if !st.Mode().IsRegular() {
		return domain.ToolResult{}, fmt.Errorf("%s não é um arquivo comum", t.fs.Rel(p))
	}
	f, err := os.Open(p)
	if err != nil {
		return domain.ToolResult{}, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, MaxReadBytes))
	if err != nil {
		return domain.ToolResult{}, err
	}
	if err := ctx.Err(); err != nil {
		return domain.ToolResult{}, err
	}
	head := data[:min(len(data), 8192)]
	if bytes.IndexByte(head, 0) >= 0 || !utf8.Valid(trimPartialRune(head)) {
		return domain.ToolResult{}, fmt.Errorf("%s parece binário (%d bytes); só leio texto", t.fs.Rel(p), st.Size())
	}
	content := string(data)
	if st.Size() > MaxReadBytes {
		content += fmt.Sprintf("\n[arquivo cortado: mostrando %d de %d bytes]", MaxReadBytes, st.Size())
	}
	return domain.ToolResult{Content: content, Summary: "li " + t.fs.Rel(p)}, nil
}

// trimPartialRune tira um caractere UTF-8 cortado no fim do bloco.
func trimPartialRune(b []byte) []byte {
	for i := 0; i < utf8.UTFMax && len(b) > 0; i++ {
		r, size := utf8.DecodeLastRune(b)
		if r != utf8.RuneError || size != 1 {
			break
		}
		b = b[:len(b)-1]
	}
	return b
}

// ListDir lista uma pasta do diretório de trabalho.
type ListDir struct{ fs Sandbox }

// Spec implementa ports.Tool.
func (ListDir) Spec() domain.ToolSpec {
	return domain.ToolSpec{
		Name:        "list_dir",
		Description: "Lista arquivos e pastas (pastas terminam com /). Sem path, lista o diretório de trabalho.",
		Params:      json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","maxLength":1024,"description":"pasta relativa ao diretório de trabalho; padrão ."}},"additionalProperties":false}`),
		Risk:        domain.RiskRead,
	}
}

// Summary é a linha mostrada na tela.
func (ListDir) Summary(args []byte) string {
	var a struct{ Path string }
	_ = decode(args, &a)
	if a.Path == "" || a.Path == "." {
		return "olhando a pasta atual"
	}
	return "olhando a pasta " + a.Path
}

// Run implementa ports.Tool.
func (t ListDir) Run(ctx context.Context, args json.RawMessage) (domain.ToolResult, error) {
	var a struct{ Path string }
	if err := decode(args, &a); err != nil {
		return domain.ToolResult{}, err
	}
	p, err := t.fs.Resolve(a.Path)
	if err != nil {
		return domain.ToolResult{}, err
	}
	entries, err := os.ReadDir(p)
	if err != nil {
		return domain.ToolResult{}, err
	}
	if err := ctx.Err(); err != nil {
		return domain.ToolResult{}, err
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].IsDir() != entries[j].IsDir() {
			return entries[i].IsDir()
		}
		return entries[i].Name() < entries[j].Name()
	})
	var b strings.Builder
	fmt.Fprintf(&b, "%s (%d itens)\n", t.fs.Rel(p), len(entries))
	for i, e := range entries {
		if i == MaxDirEntries {
			fmt.Fprintf(&b, "[mais %d itens não mostrados]\n", len(entries)-MaxDirEntries)
			break
		}
		if e.IsDir() {
			b.WriteString(e.Name() + "/\n")
			continue
		}
		size := ""
		if info, err := e.Info(); err == nil {
			size = " (" + humanSize(info.Size()) + ")"
		}
		b.WriteString(e.Name() + size + "\n")
	}
	return domain.ToolResult{Content: strings.TrimRight(b.String(), "\n")}, nil
}

func humanSize(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}

// MemorySearch busca na memória longa.
type MemorySearch struct{ mem ports.MemoryStore }

// Spec implementa ports.Tool.
func (MemorySearch) Spec() domain.ToolSpec {
	return domain.ToolSpec{
		Name:        "memory_search",
		Description: "Busca fatos guardados sobre a pessoa e conversas anteriores.",
		Params:      json.RawMessage(`{"type":"object","properties":{"query":{"type":"string","minLength":1,"maxLength":200},"limit":{"type":"integer","minimum":1,"maximum":10}},"required":["query"],"additionalProperties":false}`),
		Risk:        domain.RiskRead,
	}
}

// Summary é a linha mostrada na tela.
func (MemorySearch) Summary([]byte) string { return "procurando na memória" }

// Run implementa ports.Tool.
func (t MemorySearch) Run(ctx context.Context, args json.RawMessage) (domain.ToolResult, error) {
	var a struct {
		Query string
		Limit int
	}
	if err := decode(args, &a); err != nil {
		return domain.ToolResult{}, err
	}
	if a.Limit == 0 {
		a.Limit = 5
	}
	items, err := t.mem.Search(ctx, domain.MemoryQuery{Text: a.Query, Limit: a.Limit})
	if err != nil {
		return domain.ToolResult{}, err
	}
	if len(items) == 0 {
		return domain.ToolResult{Content: "Nada encontrado na memória."}, nil
	}
	var b strings.Builder
	for _, it := range items {
		fmt.Fprintf(&b, "- %s (confiança %s, %s)\n", it.Text, it.Trust, it.CreatedAt.Format("2006-01-02"))
	}
	return domain.ToolResult{Content: strings.TrimRight(b.String(), "\n")}, nil
}

// MemorySave guarda um fato na memória longa. É escrita: pede confirmação.
type MemorySave struct{ mem ports.MemoryStore }

// Spec implementa ports.Tool.
func (MemorySave) Spec() domain.ToolSpec {
	return domain.ToolSpec{
		Name:        "memory_save",
		Description: "Guarda um fato curto sobre a pessoa para lembrar depois. Use só quando a pessoa pedir ou contar algo dela que valha lembrar.",
		Params:      json.RawMessage(fmt.Sprintf(`{"type":"object","properties":{"text":{"type":"string","minLength":3,"maxLength":%d}},"required":["text"],"additionalProperties":false}`, maxMemoryChars)),
		Risk:        domain.RiskWrite,
	}
}

// Summary é a linha mostrada na tela.
func (MemorySave) Summary([]byte) string { return "guardando na memória" }

// ConfirmQuestion implementa ports.Confirmable.
func (MemorySave) ConfirmQuestion(args json.RawMessage) string {
	var a struct{ Text string }
	_ = decode(args, &a)
	return fmt.Sprintf("Posso guardar na memória: %q?", a.Text)
}

// Run implementa ports.Tool. A pessoa confirmou o texto, então ele entra
// com confiança alta.
func (t MemorySave) Run(ctx context.Context, args json.RawMessage) (domain.ToolResult, error) {
	var a struct{ Text string }
	if err := decode(args, &a); err != nil {
		return domain.ToolResult{}, err
	}
	item, err := t.mem.Save(ctx, domain.MemoryItem{Text: a.Text, Kind: "fact", Source: "user-confirmed", Trust: domain.TrustHigh})
	if err != nil {
		return domain.ToolResult{}, err
	}
	return domain.ToolResult{Content: fmt.Sprintf("Guardado (memória %d).", item.ID)}, nil
}
