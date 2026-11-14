package policy

import (
	"encoding/json"
	"strings"
	"testing"

	"nexusai/internal/core/domain"
)

func TestCheck(t *testing.T) {
	p := New("read_file", "memory_save", "rm")
	tests := []struct {
		spec          domain.ToolSpec
		allowed, conf bool
	}{
		{domain.ToolSpec{Name: "read_file", Risk: domain.RiskRead}, true, false},
		{domain.ToolSpec{Name: "memory_save", Risk: domain.RiskWrite}, true, true},
		{domain.ToolSpec{Name: "rm", Risk: domain.RiskDestructive}, true, true},
		{domain.ToolSpec{Name: "rm", Risk: "weird"}, true, true},
		{domain.ToolSpec{Name: "shell", Risk: domain.RiskRead}, false, false},
	}
	for _, tt := range tests {
		v := p.Check(tt.spec)
		if v.Allowed != tt.allowed || v.NeedsConfirmation != tt.conf {
			t.Errorf("%s/%s: %+v", tt.spec.Name, tt.spec.Risk, v)
		}
		if !v.Allowed && v.Reason == "" {
			t.Errorf("%s: negação sem motivo", tt.spec.Name)
		}
	}
	if (Policy{}).Check(domain.ToolSpec{Name: "x", Risk: domain.RiskRead}).Allowed {
		t.Error("allowlist vazia deveria negar tudo")
	}
}

const schema = `{
	"type": "object",
	"properties": {
		"path":  {"type": "string", "minLength": 1, "maxLength": 10},
		"limit": {"type": "integer", "minimum": 1, "maximum": 20},
		"score": {"type": "number"},
		"deep":  {"type": "boolean"},
		"mode":  {"type": "string", "enum": ["a", "b"]},
		"tags":  {"type": "array", "items": {"type": "string"}, "maxItems": 2},
		"any":   {}
	},
	"required": ["path"],
	"additionalProperties": false
}`

func TestValidateArgs(t *testing.T) {
	tests := []struct {
		args, wantErr string
	}{
		{`{"path": "a.txt"}`, ""},
		{`{"path": "a", "limit": 5, "score": 1.5, "deep": true, "mode": "b", "tags": ["x"], "any": [1]}`, ""},
		{`{"path": "a", "limit": 2.0}`, ""},
		{``, "args.path: obrigatório"},
		{`{}`, "args.path: obrigatório"},
		{`[]`, "esperado objeto"},
		{`{"path": 3}`, "esperado texto"},
		{`{"path": ""}`, "curto demais"},
		{`{"path": "01234567890"}`, "longo demais"},
		{`{"path": "a", "limit": 0}`, "menor que"},
		{`{"path": "a", "limit": 21}`, "maior que"},
		{`{"path": "a", "limit": 1.5}`, "inteiro"},
		{`{"path": "a", "limit": "3"}`, "esperado número"},
		{`{"path": "a", "deep": "sim"}`, "true ou false"},
		{`{"path": "a", "mode": "c"}`, "opções"},
		{`{"path": "a", "tags": "x"}`, "esperado lista"},
		{`{"path": "a", "tags": ["x", "y", "z"]}`, "itens demais"},
		{`{"path": "a", "tags": [1]}`, "args.tags[0]"},
		{`{"path": "a", "rm": true}`, "não permitido"},
		{`{"path": "a"} {}`, "conteúdo extra"},
		{`{"path": `, "não são JSON"},
	}
	for _, tt := range tests {
		err := ValidateArgs(json.RawMessage(schema), json.RawMessage(tt.args))
		if tt.wantErr == "" {
			if err != nil {
				t.Errorf("%s: erro inesperado %v", tt.args, err)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
			t.Errorf("%s: quer erro com %q, veio %v", tt.args, tt.wantErr, err)
		}
	}
}

func TestValidateArgsBadSchema(t *testing.T) {
	if err := ValidateArgs(json.RawMessage(`{`), json.RawMessage(`{}`)); err == nil {
		t.Error("schema quebrado deveria falhar")
	}
	if err := ValidateArgs(json.RawMessage(`{"type": "tuple"}`), json.RawMessage(`{}`)); err == nil {
		t.Error("tipo desconhecido deveria falhar")
	}
	open := `{"type": "object", "properties": {}}`
	if err := ValidateArgs(json.RawMessage(open), json.RawMessage(`{"x": 1}`)); err != nil {
		t.Errorf("additionalProperties ausente deveria aceitar: %v", err)
	}
}

func TestRedact(t *testing.T) {
	secrets := map[string]string{
		"openai_key":       "sk-abcdefghijklmnopqrstuvwx",
		"anthropic_key":    "sk-ant-api03-abcdefghijklmnopqrstuvwx",
		"aws_access_key":   "AKIAIOSFODNN7EXAMPLE",
		"github_token":     "ghp_abcdefghijklmnopqrstuvwxyz0123456789",
		"slack_token":      "xoxb-1234567890-abcdef",
		"google_api_key":   "AIzaSyA1234567890abcdefghijklmnopqrstuv",
		"jwt":              "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.dozjgNryP4J3jVmNHl0w5N_XgL0n3I9PlFUP0THsR8U",
		"private_key_body": "MIIEowIBAAKCAQEAu1SU1LfVLPHCozMxH2Mo4lgOEePzNm0tRgeLezV6ffAt0gunVTLw7onLRnrq0",
	}
	for kind, s := range secrets {
		out, kinds := Redact("a chave é " + s + ".")
		if strings.Contains(out, s) || !strings.Contains(out, Redacted) {
			t.Errorf("%s não foi removido: %q", kind, out)
		}
		if len(kinds) != 1 || (kinds[0] != kind && (kind != "anthropic_key" || kinds[0] != "openai_key")) {
			t.Errorf("%s: tipos = %v", kind, kinds)
		}
	}

	pem := "-----BEGIN RSA PRIVATE KEY-----\nabc\n-----END RSA PRIVATE KEY-----"
	if out, kinds := Redact(pem); strings.Contains(out, "BEGIN RSA PRIVATE KEY") || len(kinds) == 0 {
		t.Errorf("pem: %q %v", out, kinds)
	}

	safe := "o hash é e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855 e a pasta /home/x/y.go"
	if out, kinds := Redact(safe); out != safe || len(kinds) != 0 {
		t.Errorf("texto seguro mudou: %q %v", out, kinds)
	}
}

func TestStreamGuard(t *testing.T) {
	var out strings.Builder
	var blocked []string
	g := NewStreamGuard(func(s string) { out.WriteString(s) }, func(k string) { blocked = append(blocked, k) })
	for _, chunk := range []string{"Olá, a cha", "ve é sk-abcdefghij", "klmnopqrstuvwx e", " ", "pronto ", "fim"} {
		g.Write(chunk)
	}
	if strings.Contains(out.String(), "fim") {
		t.Error("a última palavra só sai no Flush")
	}
	g.Flush()
	want := "Olá, a chave é " + Redacted + " e pronto fim"
	if out.String() != want {
		t.Errorf("saída = %q, quer %q", out.String(), want)
	}
	if len(blocked) != 1 || blocked[0] != "openai_key" {
		t.Errorf("bloqueios = %v", blocked)
	}

	out.Reset()
	g = NewStreamGuard(func(s string) { out.WriteString(s) }, nil)
	g.Write("é ação\ncom acentos ")
	g.Flush()
	g.Flush()
	if out.String() != "é ação\ncom acentos " {
		t.Errorf("utf-8: %q", out.String())
	}
}
