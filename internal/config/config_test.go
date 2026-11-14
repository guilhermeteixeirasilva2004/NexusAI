package config

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"nexusai/internal/core/domain"
)

func noEnv(string) (string, bool) { return "", false }

func envMap(m map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) { v, ok := m[k]; return v, ok }
}

func writeFile(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "nexus.json")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestDefaultIsValid(t *testing.T) {
	if _, err := Load("", noEnv); err != nil {
		t.Fatalf("padrão deveria ser válido: %v", err)
	}
}

func TestExampleFileIsValid(t *testing.T) {
	if _, err := Load(filepath.Join("..", "..", "nexus.example.json"), noEnv); err != nil {
		t.Fatalf("nexus.example.json deveria carregar: %v", err)
	}
}

func TestLoadFileOverridesDefaults(t *testing.T) {
	p := writeFile(t, `{
		"loop": {"max_steps": 3, "request_timeout": "45s"},
		"models": {"main": {"url": "http://127.0.0.1:9001", "model": "qwen-7b", "context_size": 4096}}
	}`)
	cfg, err := Load(p, noEnv)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Loop.MaxSteps != 3 || cfg.Loop.RequestTimeout.Duration != 45*time.Second {
		t.Errorf("loop = %+v", cfg.Loop)
	}
	if got := cfg.Models[domain.RoleMain].URL; got != "http://127.0.0.1:9001" {
		t.Errorf("main.url = %q", got)
	}
	if got := cfg.Models[domain.RoleAux].URL; got != "http://127.0.0.1:8082" {
		t.Errorf("aux deveria manter o padrão, veio %q", got)
	}
}

func TestPartialRoleDoesNotInheritDefaults(t *testing.T) {
	p := writeFile(t, `{"models": {"main": {"url": "http://127.0.0.1:9001"}}}`)
	_, err := Load(p, noEnv)
	if err == nil || !strings.Contains(err.Error(), "models.main.model") {
		t.Fatalf("esperava erro em models.main.model, veio %v", err)
	}
}

func TestEnvOverridesFile(t *testing.T) {
	p := writeFile(t, `{"log": {"level": "debug"}}`)
	cfg, err := Load(p, envMap(map[string]string{
		"NEXUS_LOG_LEVEL":     "warn",
		"NEXUS_LISTEN":        "127.0.0.1:8080, 10.8.0.1:8080",
		"NEXUS_MODEL_AUX_URL": "http://localhost:7000",
		"NEXUS_MAX_STEPS":     "10",
		"NEXUS_STORAGE_PATH":  "/var/lib/nexus/nexus.db",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Log.Level != "warn" || cfg.Loop.MaxSteps != 10 || cfg.Storage.Path != "/var/lib/nexus/nexus.db" {
		t.Errorf("env não aplicado: %+v", cfg)
	}
	if len(cfg.Server.Listen) != 2 || cfg.Server.Listen[1] != "10.8.0.1:8080" {
		t.Errorf("listen = %v", cfg.Server.Listen)
	}
	if cfg.Models[domain.RoleAux].URL != "http://localhost:7000" {
		t.Errorf("aux.url = %q", cfg.Models[domain.RoleAux].URL)
	}
}

func TestBadEnvNumber(t *testing.T) {
	if _, err := Load("", envMap(map[string]string{"NEXUS_MAX_STEPS": "seis"})); err == nil {
		t.Fatal("esperava erro")
	}
}

func TestLoadRejects(t *testing.T) {
	tests := []struct {
		name, file, wantErr string
	}{
		{"campo desconhecido", `{"loop": {"max_step": 3}}`, "unknown field"},
		{"papel desconhecido", `{"models": {"big": {"url": "http://127.0.0.1:1", "model": "x", "context_size": 1}}}`, "papel desconhecido"},
		{"duração inválida", `{"loop": {"request_timeout": "rápido"}}`, "invalid duration"},
		{"conteúdo extra", `{} {}`, "conteúdo extra"},
		{"listen em todas as interfaces", `{"server": {"listen": ["0.0.0.0:8080"]}}`, "todas as interfaces"},
		{"listen com hostname", `{"server": {"listen": ["meupc:8080"]}}`, "IP explícito"},
		{"listen sem porta", `{"server": {"listen": ["127.0.0.1"]}}`, "server.listen"},
		{"listen vazio", `{"server": {"listen": []}}`, "ao menos um"},
		{"modelo fora do loopback", `{"models": {"main": {"url": "http://192.168.0.10:8081", "model": "x", "context_size": 1}}}`, "loopback"},
		{"modelo sem porta", `{"models": {"main": {"url": "http://localhost", "model": "x", "context_size": 1}}}`, "porta"},
		{"esquema errado", `{"models": {"main": {"url": "ftp://127.0.0.1:21", "model": "x", "context_size": 1}}}`, "esquema"},
		{"sha inválido", `{"models": {"main": {"url": "http://127.0.0.1:8081", "model": "x", "context_size": 1, "gguf_sha256": "abc"}}}`, "gguf_sha256"},
		{"passos demais", `{"loop": {"max_steps": 500}}`, "max_steps"},
		{"timeout zero", `{"loop": {"request_timeout": "0s"}}`, "request_timeout"},
		{"storage vazio", `{"storage": {"path": " "}}`, "storage.path"},
		{"nível de log", `{"log": {"level": "verbose"}}`, "log.level"},
		{"formato de log", `{"log": {"format": "xml"}}`, "log.format"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Load(writeFile(t, tt.file), noEnv)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("esperava erro contendo %q, veio %v", tt.wantErr, err)
			}
		})
	}
}

func TestValidateReportsAllErrors(t *testing.T) {
	c, err := Load("", noEnv)
	if err != nil {
		t.Fatal(err)
	}
	c.Loop.MaxSteps = 0
	c.Log.Format = "xml"
	delete(c.Models, domain.RoleEmbed)
	err = c.Validate()
	for _, want := range []string{"max_steps", "log.format", "models.embed"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("faltou %q em %v", want, err)
		}
	}
}

func TestMissingFile(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "nao-existe.json"), noEnv); err == nil {
		t.Fatal("esperava erro")
	}
}

func TestDurationRoundTrip(t *testing.T) {
	b, err := json.Marshal(Duration{90 * time.Second})
	if err != nil || string(b) != `"1m30s"` {
		t.Fatalf("marshal = %s, %v", b, err)
	}
	var d Duration
	if err := json.Unmarshal(b, &d); err != nil || d.Duration != 90*time.Second {
		t.Fatalf("unmarshal = %v, %v", d, err)
	}
	if err := json.Unmarshal([]byte(`90`), &d); err == nil {
		t.Fatal("número deveria falhar")
	}
}

func TestNewLogger(t *testing.T) {
	var buf bytes.Buffer
	lg, err := LogConfig{Level: "warn", Format: "json"}.NewLogger(&buf)
	if err != nil {
		t.Fatal(err)
	}
	lg.Info("some")
	lg.Warn("aparece", "trace_id", "t1")
	var line map[string]any
	if err := json.Unmarshal(buf.Bytes(), &line); err != nil {
		t.Fatalf("esperava uma linha JSON, veio %q", buf.String())
	}
	if line["msg"] != "aparece" || line["trace_id"] != "t1" {
		t.Errorf("linha = %v", line)
	}

	buf.Reset()
	lg, _ = LogConfig{Level: "debug", Format: "text"}.NewLogger(&buf)
	lg.Debug("oi")
	if !strings.Contains(buf.String(), "msg=oi") {
		t.Errorf("text = %q", buf.String())
	}
	if _, err := (LogConfig{Level: "x"}).NewLogger(&buf); err == nil {
		t.Error("nível inválido deveria falhar")
	}
}

func TestDiscovery(t *testing.T) {
	xdg := t.TempDir()
	dir := filepath.Join(xdg, "nexus")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"loop": {"max_steps": 9}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load("", envMap(map[string]string{"XDG_CONFIG_HOME": xdg, "XDG_STATE_HOME": "/srv/state"}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Loop.MaxSteps != 9 || cfg.Source != filepath.Join(dir, "config.json") {
		t.Errorf("descoberta falhou: steps=%d source=%q", cfg.Loop.MaxSteps, cfg.Source)
	}
	if cfg.Log.File != "/srv/state/nexus/nexus.log" {
		t.Errorf("log.file = %q", cfg.Log.File)
	}
}

func TestDiscoveryWithoutFileUsesDefaults(t *testing.T) {
	home := t.TempDir()
	cfg, err := Load("", envMap(map[string]string{"HOME": home}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Source != "" {
		t.Errorf("source = %q", cfg.Source)
	}
	if want := filepath.Join(home, ".local", "state", "nexus", "nexus.log"); cfg.Log.File != want {
		t.Errorf("log.file = %q, quer %q", cfg.Log.File, want)
	}
	if want := filepath.Join(home, ".local", "share", "nexus", "nexus.db"); cfg.Storage.Path != want {
		t.Errorf("storage.path = %q, quer %q", cfg.Storage.Path, want)
	}
}

func TestExplicitConfigMustExist(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "x.json")
	if _, err := Load("", envMap(map[string]string{"NEXUS_CONFIG": missing})); err == nil {
		t.Error("NEXUS_CONFIG apontando para arquivo inexistente deveria falhar")
	}
}

func TestLogFileWithoutHome(t *testing.T) {
	cfg, err := Load("", noEnv)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Log.File != "stderr" {
		t.Errorf("log.file = %q", cfg.Log.File)
	}
	cfg, _ = Load("", envMap(map[string]string{"NEXUS_LOG_FILE": "stderr", "HOME": "/h"}))
	if cfg.Log.File != "stderr" {
		t.Errorf("NEXUS_LOG_FILE ignorado: %q", cfg.Log.File)
	}
}

func TestLogOpen(t *testing.T) {
	p := filepath.Join(t.TempDir(), "a", "b", "nexus.log")
	w, err := LogConfig{File: p}.Open()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("x\n")); err != nil {
		t.Fatal(err)
	}
	w.Close()
	st, err := os.Stat(p)
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("arquivo = %v, %v", st, err)
	}
	w, err = LogConfig{File: "stderr"}.Open()
	if err != nil || w.Close() != nil {
		t.Fatalf("stderr: %v", err)
	}
}
