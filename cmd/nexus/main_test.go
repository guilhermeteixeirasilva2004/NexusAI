package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"nexusai/internal/adapters/outbound/llamacpp"
	"nexusai/internal/adapters/outbound/sqlitestore"
)

// tempEnv simula um usuário com HOME num diretório temporário.
func tempEnv(t *testing.T, extra map[string]string) (func(string) (string, bool), string) {
	t.Helper()
	home := t.TempDir()
	m := map[string]string{"HOME": home, "NEXUS_LISTEN": "127.0.0.1:59871"}
	for k, v := range extra {
		m[k] = v
	}
	return func(k string) (string, bool) { v, ok := m[k]; return v, ok }, home
}

func term(in string) (terminal, *bytes.Buffer, *bytes.Buffer) {
	var out, errb bytes.Buffer
	return terminal{in: strings.NewReader(in), out: &out, err: &errb}, &out, &errb
}

// fakeLlama responde como um llama-server: devolve as respostas em ordem.
func fakeLlama(t *testing.T, replies ...string) *httptest.Server {
	t.Helper()
	var mu sync.Mutex
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, `{"status":"ok"}`) })
	mux.HandleFunc("GET /props", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"default_generation_settings":{"n_ctx":8192}}`)
	})
	mux.HandleFunc("POST /tokenize", func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Content string }
		_ = json.NewDecoder(r.Body).Decode(&req)
		_ = json.NewEncoder(w).Encode(map[string]any{"tokens": make([]int, len(strings.Fields(req.Content)))})
	})
	mux.HandleFunc("POST /v1/chat/completions", func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		reply := "sem mais respostas"
		if len(replies) > 0 {
			reply, replies = replies[0], replies[1:]
		}
		mu.Unlock()
		b, _ := json.Marshal(reply)
		fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%s}}]}\n\n", b)
		fmt.Fprint(w, "data: {\"choices\":[{\"finish_reason\":\"stop\",\"delta\":{}}]}\n\ndata: [DONE]\n\n")
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestConversationEndToEnd(t *testing.T) {
	srv := fakeLlama(t, `{"tool": "list_dir", "args": {}}`, "Tem um arquivo: notas.md.", "Lembro sim.")
	env, home := tempEnv(t, map[string]string{"NEXUS_MODEL_MAIN_URL": srv.URL})
	work := t.TempDir()
	if err := os.WriteFile(filepath.Join(work, "notas.md"), []byte("oi"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(home, ".config", "nexus", "config.json")
	_ = os.MkdirAll(filepath.Dir(cfgPath), 0o700)
	_ = os.WriteFile(cfgPath, []byte(fmt.Sprintf(`{"tools": {"workdir": %q, "allow": ["read_file", "list_dir", "memory_search", "memory_save"]}}`, work)), 0o600)

	tm, out, errb := term("o que tem aqui?\nlembra?\nsair\n")
	if code := run(context.Background(), nil, tm, env); code != 0 {
		t.Fatalf("code = %d, stderr = %s", code, errb)
	}
	got := out.String()
	for _, want := range []string{"Nexus · pode falar.", "· olhando a pasta atual…", "nexus › Tem um arquivo: notas.md.", "nexus › Lembro sim.", "Até logo!"} {
		if !strings.Contains(got, want) {
			t.Errorf("saída sem %q:\n%s", want, got)
		}
	}
	if errb.Len() != 0 {
		t.Errorf("stderr deveria estar vazio: %q", errb)
	}

	// Tudo ficou gravado: 2 turnos e a auditoria da ferramenta.
	db := filepath.Join(home, ".local", "share", "nexus", "nexus.db")
	store, err := sqlitestore.Open(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	sess, err := store.Latest(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	counts, _ := store.CountMessages(context.Background(), sess.ID)
	runs, _ := store.ToolRuns(context.Background(), sess.ID)
	if counts["user"] != 2 || counts["assistant"] != 3 || counts["tool"] != 1 || len(runs) != 1 || runs[0].Decision != "allowed" {
		t.Errorf("gravado: %v, auditoria %+v", counts, runs)
	}
	logData, _ := os.ReadFile(filepath.Join(home, ".local", "state", "nexus", "nexus.log"))
	if !bytes.Contains(logData, []byte(`"msg":"modelo main já estava no ar"`)) {
		t.Errorf("log:\n%s", logData)
	}

	// Uma pergunta rápida continua a mesma conversa.
	srv2 := fakeLlama(t, "Seu arquivo é notas.md.")
	env2 := func(k string) (string, bool) {
		if k == "NEXUS_MODEL_MAIN_URL" {
			return srv2.URL, true
		}
		return env(k)
	}
	tm, out, errb = term("")
	if code := run(context.Background(), []string{"qual", "era", "o", "arquivo?"}, tm, env2); code != 0 {
		t.Fatalf("pergunta rápida: code = %d, %s", code, errb)
	}
	if out.String() != "nexus › Seu arquivo é notas.md.\n" {
		t.Errorf("pergunta rápida = %q", out.String())
	}
}

func TestWritesNeedATerminal(t *testing.T) {
	srv := fakeLlama(t, `{"tool": "memory_save", "args": {"text": "gosta de chá"}}`, "Não consegui guardar.")
	env, home := tempEnv(t, map[string]string{"NEXUS_MODEL_MAIN_URL": srv.URL})
	tm, _, errb := term("s\n")
	if code := run(context.Background(), []string{"lembre que gosto de chá"}, tm, env); code != 0 {
		t.Fatalf("code = %d, %s", code, errb)
	}
	store, _ := sqlitestore.Open(context.Background(), filepath.Join(home, ".local", "share", "nexus", "nexus.db"))
	defer store.Close()
	sess, _ := store.Latest(context.Background())
	runs, _ := store.ToolRuns(context.Background(), sess.ID)
	if len(runs) != 1 || runs[0].Decision != "denied_user" {
		t.Errorf("sem terminal, escrita não roda mesmo com 's' na entrada: %+v", runs)
	}
}

func TestNoModelAnywhere(t *testing.T) {
	env, _ := tempEnv(t, map[string]string{"NEXUS_MODEL_MAIN_URL": "http://127.0.0.1:1"})
	tm, _, errb := term("")
	if code := run(context.Background(), nil, tm, env); code != 1 {
		t.Fatalf("code = %d", code)
	}
	if !strings.Contains(errb.String(), "Não achei nenhum modelo .gguf") {
		t.Errorf("stderr = %s", errb)
	}
}

func TestChooseModel(t *testing.T) {
	home := t.TempDir()
	models := filepath.Join(home, "llama.cpp", "models")
	_ = os.MkdirAll(models, 0o700)
	for name, mb := range map[string]int64{"grande-14b.gguf": 9 << 10, "medio-7b.gguf": 4 << 10, "pequeno-3b.gguf": 2 << 10} {
		f, _ := os.Create(filepath.Join(models, name))
		_ = f.Truncate(mb << 20)
		f.Close()
	}
	tm, out, _ := term("")
	tm.inTTY = true
	got, err := chooseModel(tm, bufio.NewReader(strings.NewReader("abc\n9\n3\n")), home)
	if err != nil || got.Name() != "pequeno-3b" {
		t.Fatalf("escolha = %+v, %v", got, err)
	}
	text := out.String()
	if !strings.Contains(text, "2) medio-7b") || !strings.Contains(text, "(recomendado)") || strings.Count(text, "Digite um número de 1 a 3.") != 2 {
		t.Errorf("menu:\n%s", text)
	}
	if !strings.Contains(strings.Split(text, "\n")[2], "medio-7b") || !strings.Contains(strings.Split(text, "\n")[2], "recomendado") {
		t.Errorf("o recomendado é o maior até 8 GB:\n%s", text)
	}
	// Enter aceita o recomendado; sem terminal, escolhe sozinho.
	got, _ = chooseModel(tm, bufio.NewReader(strings.NewReader("\n")), home)
	if got.Name() != "medio-7b" {
		t.Errorf("enter = %s", got.Name())
	}
	tm.inTTY = false
	got, _ = chooseModel(tm, bufio.NewReader(strings.NewReader("")), home)
	if got.Name() != "medio-7b" {
		t.Errorf("sem terminal = %s", got.Name())
	}
	if recommended([]llamacpp.ModelFile{{Size: 9 << 30}, {Size: 10 << 30}}) != 1 {
		t.Error("todos grandes: o último (menor)")
	}
}

func TestStatus(t *testing.T) {
	srv := fakeLlama(t)
	env, _ := tempEnv(t, map[string]string{"NEXUS_MODEL_MAIN_URL": srv.URL})
	tm, out, errb := term("")
	if code := run(context.Background(), []string{"status"}, tm, env); code != 0 {
		t.Fatalf("code = %d, %s", code, errb)
	}
	if !strings.Contains(out.String(), "no ar em "+srv.URL) || !strings.Contains(out.String(), "padrões") {
		t.Errorf("status:\n%s", out)
	}
	env, _ = tempEnv(t, map[string]string{"NEXUS_MODEL_MAIN_URL": "http://127.0.0.1:1"})
	tm, out, _ = term("")
	run(context.Background(), []string{"status"}, tm, env)
	if !strings.Contains(out.String(), "nenhum escolhido") {
		t.Errorf("status sem modelo:\n%s", out)
	}
}

func TestCheckConfigAndFlags(t *testing.T) {
	env, _ := tempEnv(t, nil)
	tm, out, errb := term("")
	if code := run(context.Background(), []string{"-check-config"}, tm, env); code != 0 || !strings.Contains(out.String(), `"max_steps": 6`) {
		t.Errorf("check-config: %d %s %s", code, out, errb)
	}
	tm, out, _ = term("")
	if code := run(context.Background(), []string{"-version"}, tm, env); code != 0 || strings.TrimSpace(out.String()) != "dev" {
		t.Errorf("version: %d %q", code, out)
	}
	tm, _, errb = term("")
	if code := run(context.Background(), []string{"-nope"}, tm, env); code != 2 || !strings.Contains(errb.String(), "nexus status") {
		t.Errorf("flag inválida: %d %q", code, errb)
	}
	bad, _ := tempEnv(t, map[string]string{"NEXUS_LISTEN": "0.0.0.0:8080"})
	tm, _, errb = term("")
	if code := run(context.Background(), nil, tm, bad); code != 1 || !strings.Contains(errb.String(), "todas as interfaces") {
		t.Errorf("config inválida: %d %q", code, errb)
	}
}
