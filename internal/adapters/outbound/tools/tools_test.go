package tools

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"nexusai/internal/core/domain"
	"nexusai/internal/core/fakes"
	"nexusai/internal/core/policy"
	"nexusai/internal/core/ports"
)

func setup(t *testing.T) (string, map[string]ports.Tool, *fakes.Memory) {
	t.Helper()
	root := t.TempDir()
	write := func(rel, content string) {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("notas.md", "# Notas\ncomprar café")
	write("src/main.go", "package main")
	write(".env", "TOKEN=segredo")
	write(".ssh/config", "Host x")
	write("cert.pem", "-----BEGIN")
	write("bin.dat", "a\x00b")
	mem := &fakes.Memory{}
	all, err := All(root, mem)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]ports.Tool{}
	for _, tool := range all {
		byName[tool.Spec().Name] = tool
	}
	return root, byName, mem
}

func run(t *testing.T, tool ports.Tool, args string) (string, error) {
	t.Helper()
	// Todo exemplo de argumento usado aqui precisa passar no próprio schema.
	if err := policy.ValidateArgs(tool.Spec().Params, json.RawMessage(args)); err != nil {
		t.Fatalf("args %s não passam no schema de %s: %v", args, tool.Spec().Name, err)
	}
	res, err := tool.Run(context.Background(), json.RawMessage(args))
	return res.Content, err
}

func TestSpecs(t *testing.T) {
	_, tools, _ := setup(t)
	want := map[string]domain.Risk{"read_file": domain.RiskRead, "list_dir": domain.RiskRead, "memory_search": domain.RiskRead, "memory_save": domain.RiskWrite}
	for name, risk := range want {
		tool, ok := tools[name]
		if !ok {
			t.Fatalf("falta %s", name)
		}
		spec := tool.Spec()
		if spec.Risk != risk || spec.Description == "" || !json.Valid(spec.Params) {
			t.Errorf("%s: %+v", name, spec)
		}
	}
}

func TestReadFile(t *testing.T) {
	root, tools, _ := setup(t)
	rf := tools["read_file"]
	if got, err := run(t, rf, `{"path": "notas.md"}`); err != nil || got != "# Notas\ncomprar café" {
		t.Errorf("leitura = %q, %v", got, err)
	}
	if got, err := run(t, rf, `{"path": "`+filepath.Join(root, "src/main.go")+`"}`); err != nil || got != "package main" {
		t.Errorf("caminho absoluto dentro = %q, %v", got, err)
	}
	cases := map[string]error{
		`{"path": "../fora.txt"}`:          ErrOutside,
		`{"path": "/etc/passwd"}`:          ErrOutside,
		`{"path": ".env"}`:                 ErrSensitive,
		`{"path": ".ssh/config"}`:          ErrSensitive,
		`{"path": "cert.pem"}`:             ErrSensitive,
		`{"path": "src/../../etc/passwd"}`: ErrOutside,
	}
	for args, want := range cases {
		if _, err := run(t, rf, args); !errors.Is(err, want) {
			t.Errorf("%s: err = %v, quer %v", args, err, want)
		}
	}
	for args, want := range map[string]string{
		`{"path": "nao-existe.md"}`: "não existe",
		`{"path": "src"}`:           "é uma pasta",
		`{"path": "bin.dat"}`:       "binário",
	} {
		if _, err := run(t, rf, args); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v", args, err)
		}
	}
	if s := rf.(ReadFile).Summary([]byte(`{"path":"notas.md"}`)); s != "lendo notas.md" {
		t.Errorf("summary = %q", s)
	}
}

func TestSymlinkEscape(t *testing.T) {
	root, tools, _ := setup(t)
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "segredo.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "segredo.txt"), filepath.Join(root, "atalho.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "pasta")); err != nil {
		t.Fatal(err)
	}
	if _, err := run(t, tools["read_file"], `{"path": "atalho.txt"}`); !errors.Is(err, ErrOutside) {
		t.Errorf("symlink para fora = %v", err)
	}
	if _, err := run(t, tools["list_dir"], `{"path": "pasta"}`); !errors.Is(err, ErrOutside) {
		t.Errorf("symlink de pasta para fora = %v", err)
	}
}

func TestLargeFileTruncated(t *testing.T) {
	root, tools, _ := setup(t)
	big := strings.Repeat("linha de texto\n", MaxReadBytes/10)
	if err := os.WriteFile(filepath.Join(root, "grande.txt"), []byte(big), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := run(t, tools["read_file"], `{"path": "grande.txt"}`)
	if err != nil || !strings.Contains(got, "[arquivo cortado") || len(got) > MaxReadBytes+200 {
		t.Errorf("corte: len=%d err=%v", len(got), err)
	}
	// UTF-8 cortado no meio de um caractere não vira "binário".
	accents := strings.Repeat("ação ", MaxReadBytes/3)
	if err := os.WriteFile(filepath.Join(root, "acentos.txt"), []byte(accents), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := run(t, tools["read_file"], `{"path": "acentos.txt"}`); err != nil {
		t.Errorf("acentos: %v", err)
	}
}

func TestListDir(t *testing.T) {
	root, tools, _ := setup(t)
	ld := tools["list_dir"]
	got, err := run(t, ld, `{}`)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(got, "\n")
	if lines[0] != ". (6 itens)" || lines[1] != ".ssh/" || lines[2] != "src/" || !strings.Contains(got, "notas.md (21 B)") {
		t.Errorf("listagem:\n%s", got)
	}
	if got, err := run(t, ld, `{"path": "src"}`); err != nil || !strings.Contains(got, "main.go") {
		t.Errorf("src = %q, %v", got, err)
	}
	if _, err := run(t, ld, `{"path": ".ssh"}`); !errors.Is(err, ErrSensitive) {
		t.Errorf(".ssh = %v", err)
	}
	for i := 0; i < MaxDirEntries+5; i++ {
		_ = os.WriteFile(filepath.Join(root, "src", "f"+strings.Repeat("x", i%3)+string(rune('a'+i%26))+".txt"+string(rune('0'+i/26))), nil, 0o600)
	}
	if got, _ := run(t, ld, `{"path": "src"}`); !strings.Contains(got, "não mostrados") {
		t.Error("deveria limitar itens")
	}
	if s := ld.(ListDir).Summary([]byte(`{}`)); s != "olhando a pasta atual" {
		t.Errorf("summary = %q", s)
	}
	if s := ld.(ListDir).Summary([]byte(`{"path":"src"}`)); s != "olhando a pasta src" {
		t.Errorf("summary = %q", s)
	}
	for n, want := range map[int64]string{10: "10 B", 2048: "2.0 KB", 3 << 20: "3.0 MB", 5 << 30: "5.0 GB"} {
		if humanSize(n) != want {
			t.Errorf("humanSize(%d) = %s", n, humanSize(n))
		}
	}
}

func TestMemoryTools(t *testing.T) {
	_, tools, mem := setup(t)
	save, search := tools["memory_save"], tools["memory_search"]

	q := save.(ports.Confirmable).ConfirmQuestion(json.RawMessage(`{"text": "gosta de café"}`))
	if q != `Posso guardar na memória: "gosta de café"?` {
		t.Errorf("pergunta = %q", q)
	}
	if got, err := run(t, save, `{"text": "gosta de café"}`); err != nil || !strings.Contains(got, "Guardado") {
		t.Fatalf("save = %q, %v", got, err)
	}
	items := mem.Items()
	if len(items) != 1 || items[0].Trust != domain.TrustHigh || items[0].Source != "user-confirmed" {
		t.Errorf("item = %+v", items)
	}
	if got, err := run(t, search, `{"query": "café"}`); err != nil || !strings.Contains(got, "gosta de café (confiança high") {
		t.Errorf("search = %q, %v", got, err)
	}
	if got, err := run(t, search, `{"query": "xyz", "limit": 2}`); err != nil || got != "Nada encontrado na memória." {
		t.Errorf("search vazio = %q, %v", got, err)
	}
	if save.(MemorySave).Summary(nil) == "" || search.(MemorySearch).Summary(nil) == "" {
		t.Error("summary vazio")
	}
}

func TestBadArgsAndSandbox(t *testing.T) {
	_, tools, _ := setup(t)
	for name, tool := range tools {
		if _, err := tool.Run(context.Background(), json.RawMessage(`[1]`)); err == nil {
			t.Errorf("%s aceitou argumento que não é objeto", name)
		}
	}
	if _, err := NewSandbox(filepath.Join(t.TempDir(), "nao-existe")); err == nil {
		t.Error("raiz inexistente deveria falhar")
	}
	s, _ := NewSandbox(t.TempDir())
	if _, err := s.Resolve("a\x00b"); err == nil {
		t.Error("NUL no caminho")
	}
	if _, err := s.Resolve("../nao-existe"); !errors.Is(err, ErrOutside) {
		t.Errorf("inexistente fora = %v", err)
	}
	if p, err := s.Resolve("~"); err != nil || p != s.Root() {
		t.Errorf("~ = %q, %v", p, err)
	}
	if _, err := All(filepath.Join(t.TempDir(), "x"), nil); err == nil {
		t.Error("All com raiz inexistente")
	}
}
