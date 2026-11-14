package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// tempEnv simula um usuário com HOME num diretório temporário.
func tempEnv(t *testing.T, extra map[string]string) (func(string) (string, bool), string) {
	t.Helper()
	home := t.TempDir()
	m := map[string]string{"HOME": home}
	for k, v := range extra {
		m[k] = v
	}
	return func(k string) (string, bool) { v, ok := m[k]; return v, ok }, home
}

func TestRunWithoutArgsNeedsNoConfig(t *testing.T) {
	env, home := tempEnv(t, nil)
	var out, errb bytes.Buffer
	if code := run(nil, &out, &errb, env); code != 0 {
		t.Fatalf("code = %d, stderr = %s", code, errb.String())
	}
	if errb.Len() != 0 {
		t.Errorf("nada deveria ir para o terminal de erro, veio %q", errb.String())
	}
	for _, want := range []string{"configuração ok", "padrões", "Fase 1"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("faltou %q em %q", want, out.String())
		}
	}

	logPath := filepath.Join(home, ".local", "state", "nexus", "nexus.log")
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("log deveria estar em %s: %v", logPath, err)
	}
	var line map[string]any
	if err := json.Unmarshal(bytes.SplitN(data, []byte("\n"), 2)[0], &line); err != nil {
		t.Fatalf("log deveria ser JSON: %q", data)
	}
	if line["msg"] != "config carregada" {
		t.Errorf("linha = %v", line)
	}
}

func TestRunFindsConfigInHome(t *testing.T) {
	env, home := tempEnv(t, nil)
	dir := filepath.Join(home, ".config", "nexus")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"loop": {"max_steps": 4}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	if code := run([]string{"status"}, &out, &errb, env); code != 0 {
		t.Fatalf("code = %d, stderr = %s", code, errb.String())
	}
	if !strings.Contains(out.String(), filepath.Join(dir, "config.json")) || strings.Contains(out.String(), "Fase 1") {
		t.Errorf("status = %q", out.String())
	}
}

func TestRunCheckConfig(t *testing.T) {
	env, _ := tempEnv(t, nil)
	var out, errb bytes.Buffer
	if code := run([]string{"-check-config"}, &out, &errb, env); code != 0 {
		t.Fatalf("code = %d, stderr = %s", code, errb.String())
	}
	if !strings.Contains(out.String(), `"max_steps": 6`) {
		t.Errorf("saída = %s", out.String())
	}
}

func TestRunInvalidConfig(t *testing.T) {
	env, _ := tempEnv(t, map[string]string{"NEXUS_LISTEN": "0.0.0.0:8080"})
	var out, errb bytes.Buffer
	if code := run(nil, &out, &errb, env); code != 1 {
		t.Fatalf("code = %d", code)
	}
	if !strings.Contains(errb.String(), "todas as interfaces") {
		t.Errorf("stderr = %s", errb.String())
	}
}

func TestRunQuestionNotYet(t *testing.T) {
	env, _ := tempEnv(t, nil)
	var out, errb bytes.Buffer
	if code := run([]string{"que horas são?"}, &out, &errb, env); code != 2 {
		t.Fatalf("code = %d", code)
	}
	if !strings.Contains(errb.String(), "Fase 1") {
		t.Errorf("stderr = %s", errb.String())
	}
}

func TestRunFlags(t *testing.T) {
	env, _ := tempEnv(t, nil)
	var out, errb bytes.Buffer
	if code := run([]string{"-version"}, &out, &errb, env); code != 0 || strings.TrimSpace(out.String()) != "dev" {
		t.Errorf("version: code=%d out=%q", code, out.String())
	}
	errb.Reset()
	if code := run([]string{"-nope"}, &out, &errb, env); code != 2 || !strings.Contains(errb.String(), "nexus status") {
		t.Errorf("flag inválida: code=%d stderr=%q", code, errb.String())
	}
}
