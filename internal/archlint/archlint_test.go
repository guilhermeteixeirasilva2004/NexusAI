package archlint

import (
	"path/filepath"
	"strings"
	"testing"
)

// O repositório real precisa estar limpo. Este teste é o portão de arquitetura.
func TestRepositoryRespectsArchitecture(t *testing.T) {
	root := filepath.Join("..", "..")
	module, err := ModulePath(root)
	if err != nil {
		t.Fatal(err)
	}
	vs, err := Check(root, module)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range vs {
		t.Error(v)
	}
}

// O fixture tem violações conhecidas: o checador precisa achar todas e só elas.
func TestDetectsViolations(t *testing.T) {
	root := filepath.Join("testdata", "bad")
	module, err := ModulePath(root)
	if err != nil {
		t.Fatal(err)
	}
	vs, err := Check(root, module)
	if err != nil {
		t.Fatal(err)
	}
	got := make([]string, len(vs))
	for i, v := range vs {
		got[i] = v.File + " -> " + v.Import
	}
	want := []string{
		"internal/adapters/outbound/a/a.go -> fake/internal/adapters/outbound/b",
		"internal/config/c.go -> fake/internal/adapters/outbound/b",
		"internal/core/x/x.go -> fake/internal/adapters/outbound/a",
		"internal/core/x/x.go -> fake/internal/config",
		"internal/core/x/x.go -> github.com/foo/bar",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("violações:\n%s\n\nesperado:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestJudge(t *testing.T) {
	tests := []struct{ file, imp, want string }{
		{"internal/core/a/a.go", "context", ""},
		{"internal/core/a/a.go", "m/internal/core/domain", ""},
		{"internal/core/a/a.go", "m/internal/corex", "o core só importa o próprio core"},
		{"internal/core/a/a.go", "m", "o core só importa o próprio core"},
		{"internal/core/a/a.go", "golang.org/x/sync", "o core só importa a stdlib"},
		{"internal/adapters/inbound/cli/c.go", "m/internal/adapters/inbound/cli/render", ""},
		{"internal/adapters/inbound/cli/c.go", "m/internal/adapters/inbound/httpapi", "um adapter não importa outro"},
		{"internal/adapters/inbound/cli/c.go", "m/internal/core/ports", ""},
		{"internal/config/c.go", "m/internal/adapters/outbound/llamacpp", "só cmd/ e test/ conhecem adapters"},
		{"cmd/nexus/main.go", "m/internal/adapters/outbound/llamacpp", ""},
		{"test/integration/i_test.go", "m/internal/adapters/outbound/llamacpp", ""},
		{"internal/config/c.go", "github.com/x/y", ""},
	}
	for _, tt := range tests {
		if got := judge(tt.file, tt.imp, "m"); got != tt.want {
			t.Errorf("judge(%q, %q) = %q, quer %q", tt.file, tt.imp, got, tt.want)
		}
	}
}

func TestModulePathErrors(t *testing.T) {
	if _, err := ModulePath(t.TempDir()); err == nil {
		t.Error("sem go.mod deveria falhar")
	}
}
