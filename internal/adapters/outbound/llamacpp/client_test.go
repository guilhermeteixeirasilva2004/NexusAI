package llamacpp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"nexusai/internal/core/domain"
	"nexusai/internal/core/fakes"
	"nexusai/internal/core/ports"
)

// fakeServer imita as rotas do llama-server que o cliente usa.
func fakeServer(t *testing.T, tokens []string) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var active atomic.Int32
	var maxActive atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, `{"status":"ok"}`) })
	mux.HandleFunc("GET /props", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"default_generation_settings":{"n_ctx":8192},"chat_template_caps":{"supports_tool_calls":true}}`)
	})
	mux.HandleFunc("POST /tokenize", func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Content string }
		_ = json.NewDecoder(r.Body).Decode(&req)
		toks := make([]int, len(strings.Fields(req.Content)))
		_ = json.NewEncoder(w).Encode(map[string]any{"tokens": toks})
	})
	mux.HandleFunc("POST /v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		n := active.Add(1)
		defer active.Add(-1)
		if n > maxActive.Load() {
			maxActive.Store(n)
		}
		var req chatRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || !req.Stream || len(req.Messages) == 0 || !req.CachePrompt {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `{"error":{"code":400,"message":"pedido ruim"}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"finish_reason\":null,\"delta\":{\"role\":\"assistant\",\"content\":null}}]}\n\n")
		for _, tok := range tokens {
			b, _ := json.Marshal(tok)
			fmt.Fprintf(w, "data: {\"choices\":[{\"finish_reason\":null,\"delta\":{\"content\":%s}}]}\n\n", b)
			w.(http.Flusher).Flush()
			time.Sleep(2 * time.Millisecond)
		}
		fmt.Fprint(w, "data: {\"choices\":[{\"finish_reason\":\"stop\",\"delta\":{}}]}\n\n")
		fmt.Fprintf(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":12,\"completion_tokens\":%d}}\n\n", len(tokens))
		fmt.Fprint(w, "data: [DONE]\n\n")
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, &maxActive
}

func TestGenerateStreams(t *testing.T) {
	srv, _ := fakeServer(t, []string{"Ol", "á", ", tudo", " bem?"})
	metrics := &fakes.Metrics{}
	c := New(srv.URL+"/", domain.RoleMain)
	c.Metrics = metrics
	var got []string
	comp, err := c.Generate(context.Background(), ports.GenerateRequest{
		Messages: []ports.ChatMessage{{Role: "user", Content: "oi"}}, MaxTokens: 10,
	}, func(s string) { got = append(got, s) })
	if err != nil {
		t.Fatal(err)
	}
	if comp.Text != "Olá, tudo bem?" || strings.Join(got, "|") != "Ol|á|, tudo| bem?" {
		t.Errorf("texto = %q, tokens = %v", comp.Text, got)
	}
	if comp.PromptTokens != 12 || comp.CompletionTokens != 4 || comp.FinishReason != "stop" {
		t.Errorf("completion = %+v", comp)
	}
	if len(metrics.Obs["nexus_inference_queue_wait_seconds{role,main}"]) != 1 {
		t.Errorf("métricas = %v", metrics.Obs)
	}
}

func TestOneGenerationAtATime(t *testing.T) {
	srv, maxActive := fakeServer(t, strings.Split(strings.Repeat("x ", 20), " "))
	c := New(srv.URL, domain.RoleMain)
	errs := make(chan error, 4)
	for i := 0; i < 4; i++ {
		go func() {
			_, err := c.Generate(context.Background(), ports.GenerateRequest{Messages: []ports.ChatMessage{{Role: "user", Content: "x"}}}, nil)
			errs <- err
		}()
	}
	for i := 0; i < 4; i++ {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	if maxActive.Load() != 1 {
		t.Errorf("gerações simultâneas = %d, quer 1", maxActive.Load())
	}
}

func TestCountProbeHealth(t *testing.T) {
	srv, _ := fakeServer(t, nil)
	c := New(srv.URL, domain.RoleMain)
	ctx := context.Background()
	if n, err := c.CountTokens(ctx, "um dois três"); err != nil || n != 3 {
		t.Errorf("CountTokens = %d, %v", n, err)
	}
	if caps := c.Capabilities(); caps.ContextSize != 4096 {
		t.Errorf("antes do Probe = %+v", caps)
	}
	caps, err := c.Probe(ctx)
	if err != nil || caps.ContextSize != 8192 || !caps.NativeTools {
		t.Fatalf("Probe = %+v, %v", caps, err)
	}
	if c.Capabilities().ContextSize != 8192 {
		t.Error("Capabilities deveria usar o Probe")
	}
	if err := c.Health(ctx); err != nil {
		t.Error(err)
	}
}

func TestErrors(t *testing.T) {
	ctx := context.Background()
	msgs := ports.GenerateRequest{Messages: []ports.ChatMessage{{Role: "user", Content: "x"}}}

	// Nada escutando.
	down := New("http://127.0.0.1:1", domain.RoleMain)
	if _, err := down.Generate(ctx, msgs, nil); !errors.Is(err, domain.ErrModelDown) {
		t.Errorf("Generate fora do ar = %v", err)
	}
	if err := down.Health(ctx); !errors.Is(err, domain.ErrModelDown) {
		t.Errorf("Health fora do ar = %v", err)
	}
	if _, err := down.Probe(ctx); !errors.Is(err, domain.ErrModelDown) {
		t.Errorf("Probe fora do ar = %v", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprint(w, `{"error":{"code":503,"message":"Loading model"}}`)
	})
	mux.HandleFunc("GET /props", func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, `{}`) })
	mux.HandleFunc("POST /tokenize", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprint(w, `{"error":{"code":503,"message":"Loading model"}}`)
	})
	mux.HandleFunc("POST /v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.Header.Get("X-Case"), "ctx") {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `{"error":{"code":400,"message":"the request exceeds the available context size"}}`)
			return
		}
		fmt.Fprint(w, "data: {\"error\":{\"message\":\"falhou no meio\"}}\n\n")
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	c := New(srv.URL, domain.RoleMain)
	if err := c.Health(ctx); !errors.Is(err, domain.ErrModelDown) {
		t.Errorf("Health carregando = %v", err)
	}
	if _, err := c.CountTokens(ctx, "x"); !errors.Is(err, domain.ErrModelDown) {
		t.Errorf("tokenize 503 = %v", err)
	}
	if _, err := c.Probe(ctx); err == nil || !strings.Contains(err.Error(), "n_ctx") {
		t.Errorf("props sem n_ctx = %v", err)
	}
	if _, err := c.Generate(ctx, msgs, nil); err == nil || !strings.Contains(err.Error(), "falhou no meio") {
		t.Errorf("erro no stream = %v", err)
	}
	c.HTTP.Transport = headerTransport{c.HTTP.Transport, "ctx"}
	if _, err := c.Generate(ctx, msgs, nil); !errors.Is(err, domain.ErrContextBudget) {
		t.Errorf("contexto estourado = %v", err)
	}
}

type headerTransport struct {
	base http.RoundTripper
	val  string
}

func (h headerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r.Header.Set("X-Case", h.val)
	return h.base.RoundTrip(r)
}

func TestCancelMidStream(t *testing.T) {
	srv, _ := fakeServer(t, strings.Split(strings.Repeat("palavra ", 200), " "))
	c := New(srv.URL, domain.RoleMain)
	ctx, cancel := context.WithCancel(context.Background())
	n := 0
	_, err := c.Generate(ctx, ports.GenerateRequest{Messages: []ports.ChatMessage{{Role: "user", Content: "x"}}}, func(string) {
		n++
		if n == 5 {
			cancel()
		}
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	// Com o slot ocupado e o contexto já cancelado, nem entra na fila.
	c.init()
	c.slot <- struct{}{}
	if _, err := c.Generate(ctx, ports.GenerateRequest{}, nil); !errors.Is(err, context.Canceled) {
		t.Errorf("fila com ctx cancelado = %v", err)
	}
}

// Contra um llama-server de verdade, quando NEXUS_TEST_LLAMA_URL aponta para um.
func TestRealServer(t *testing.T) {
	base := os.Getenv("NEXUS_TEST_LLAMA_URL")
	if base == "" {
		t.Skip("defina NEXUS_TEST_LLAMA_URL para testar contra um llama-server real")
	}
	c := New(base, domain.RoleMain)
	ctx := context.Background()
	if _, err := c.Probe(ctx); err != nil {
		t.Fatal(err)
	}
	n, err := c.CountTokens(ctx, "Olá, tudo bem?")
	if err != nil || n == 0 {
		t.Fatalf("CountTokens = %d, %v", n, err)
	}
	var streamed strings.Builder
	comp, err := c.Generate(ctx, ports.GenerateRequest{
		Messages: []ports.ChatMessage{{Role: "user", Content: "Responda só: oi"}}, MaxTokens: 8,
	}, func(s string) { streamed.WriteString(s) })
	if err != nil || comp.Text == "" || streamed.String() != comp.Text || comp.CompletionTokens == 0 {
		t.Fatalf("Generate = %+v (stream %q), %v", comp, streamed.String(), err)
	}
}

func TestStartFailures(t *testing.T) {
	dir := t.TempDir()
	model := filepath.Join(dir, "m.gguf")
	if err := os.WriteFile(model, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(dir, "logs", "llama.log")
	script := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o700); err != nil {
			t.Fatal(err)
		}
		return p
	}
	ctx := context.Background()

	_, err := Start(ctx, ServerSpec{Binary: script("crash", "echo 'erro: GPU sem memória'; exit 3"), Model: model, BaseURL: "http://127.0.0.1:1", LogPath: logPath})
	if err == nil || !strings.Contains(err.Error(), "GPU sem memória") {
		t.Errorf("processo que morre deveria trazer o log: %v", err)
	}

	start := time.Now()
	_, err = Start(ctx, ServerSpec{Binary: script("slow", "exec sleep 30"), Model: model, BaseURL: "http://127.0.0.1:1", StartTimeout: 600 * time.Millisecond})
	if err == nil || !strings.Contains(err.Error(), "não ficou pronto") || time.Since(start) > 5*time.Second {
		t.Errorf("timeout = %v (%s)", err, time.Since(start))
	}

	if _, err := Start(ctx, ServerSpec{Binary: "/nao/existe", Model: model, BaseURL: "http://127.0.0.1:1"}); err == nil {
		t.Error("binário inexistente deveria falhar")
	}
	if _, err := Start(ctx, ServerSpec{Binary: "/bin/true", Model: filepath.Join(dir, "nao.gguf"), BaseURL: "http://127.0.0.1:1"}); err == nil {
		t.Error("modelo inexistente deveria falhar")
	}
	var nilProc *Process
	if err := nilProc.Stop(); err != nil {
		t.Error(err)
	}
}

func TestStartAndStopWithFakeServer(t *testing.T) {
	// Um "llama-server" que é este próprio binário de teste servindo /health.
	if os.Getenv("NEXUS_FAKE_LLAMA") != "" {
		return
	}
	srv, _ := fakeServer(t, nil)
	dir := t.TempDir()
	model := filepath.Join(dir, "m.gguf")
	_ = os.WriteFile(model, []byte("x"), 0o600)
	// O script ignora os argumentos e fica vivo; o /health vem do httptest.
	bin := filepath.Join(dir, "srv")
	_ = os.WriteFile(bin, []byte("#!/bin/sh\nexec sleep 60\n"), 0o700)
	p, err := Start(context.Background(), ServerSpec{Binary: bin, Model: model, BaseURL: srv.URL, ContextSize: 4096, Alias: "x", Args: []string{"--foo"}})
	if err != nil {
		t.Fatal(err)
	}
	if p.PID() == 0 {
		t.Error("sem pid")
	}
	args := strings.Join(p.cmd.Args, " ")
	for _, want := range []string{"-m " + model, "--host 127.0.0.1", "-c 4096", "--alias x", "--foo", "-np 1"} {
		if !strings.Contains(args, want) {
			t.Errorf("argumentos sem %q: %s", want, args)
		}
	}
	if err := p.Stop(); err != nil {
		t.Error(err)
	}
	if err := p.Stop(); err != nil {
		t.Error("Stop duas vezes deveria ser seguro")
	}
}

func TestFindBinaryAndModels(t *testing.T) {
	home := t.TempDir()
	t.Setenv("PATH", t.TempDir())
	if FindBinary(home) != "" || FindBinary("") != "" {
		t.Error("não deveria achar binário")
	}
	bin := filepath.Join(home, "llama.cpp", "build", "bin", "llama-server")
	_ = os.MkdirAll(filepath.Dir(bin), 0o700)
	_ = os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o700)
	if got := FindBinary(home); got != bin {
		t.Errorf("FindBinary = %q", got)
	}

	models := filepath.Join(home, "llama.cpp", "models")
	big := func(name string, mb int64) {
		f, err := os.Create(filepath.Join(models, name))
		if err != nil {
			t.Fatal(err)
		}
		_ = f.Truncate(mb << 20)
		f.Close()
	}
	_ = os.MkdirAll(models, 0o700)
	big("qwen2.5-7b-instruct-q4_k_m.gguf", 400)
	big("qwen2.5-3b-instruct-q4_k_m.gguf", 200)
	big("ggml-vocab-qwen2.gguf", 200)
	big("nomic-embed-text.gguf", 200)
	big("tiny.gguf", 1)
	found := FindModels(home)
	if len(found) != 2 || found[0].Name() != "qwen2.5-7b-instruct-q4_k_m" || found[1].Size != 200<<20 {
		t.Errorf("FindModels = %+v", found)
	}
	if FindModels("") != nil {
		t.Error("sem home, sem modelos")
	}
}

func TestVerifyChecksum(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "m.gguf")
	_ = os.WriteFile(p, []byte("abc"), 0o600)
	const sum = "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"
	cache := filepath.Join(dir, "cache")
	if err := VerifyChecksum(p, "", cache); err != nil {
		t.Error(err)
	}
	if err := VerifyChecksum(p, strings.ToUpper(sum), cache); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(cache, "gguf-checksums.json")); err != nil {
		t.Error("deveria gravar o cache")
	}
	if err := VerifyChecksum(p, sum, cache); err != nil {
		t.Error("cache deveria bastar:", err)
	}
	if err := VerifyChecksum(p, strings.Repeat("0", 64), cache); err == nil || !strings.Contains(err.Error(), "não confere") {
		t.Errorf("checksum errado = %v", err)
	}
	if err := VerifyChecksum(filepath.Join(dir, "x"), sum, ""); err == nil {
		t.Error("arquivo inexistente")
	}
}

func TestGrammarIsSent(t *testing.T) {
	var got chatRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()
	c := New(srv.URL, domain.RoleMain)
	if _, err := c.Generate(context.Background(), ports.GenerateRequest{Messages: []ports.ChatMessage{{Role: "user", Content: "x"}}, Grammar: "root ::= [a]*"}, nil); err != nil {
		t.Fatal(err)
	}
	if got.Grammar != "root ::= [a]*" {
		t.Errorf("grammar = %q", got.Grammar)
	}
}
