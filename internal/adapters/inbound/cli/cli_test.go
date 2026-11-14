package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"nexusai/internal/core/cognition"
	"nexusai/internal/core/domain"
	"nexusai/internal/core/fakes"
	"nexusai/internal/core/orchestrator"
	"nexusai/internal/core/policy"
	"nexusai/internal/core/ports"
)

// syncBuffer é um bytes.Buffer seguro para leitura enquanto escrevem nele.
type syncBuffer struct {
	mu sync.Mutex
	b  strings.Builder
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

type world struct {
	chat  *Chat
	out   *syncBuffer
	model *fakes.Model
	sess  *fakes.Sessions
	save  *fakes.Tool
}

func newWorld(t *testing.T, input string, replies ...string) *world {
	t.Helper()
	w := &world{out: &syncBuffer{}, model: &fakes.Model{Replies: replies}, sess: fakes.NewSessions()}
	w.save = &fakes.Tool{
		SpecValue: domain.ToolSpec{Name: "memory_save", Description: "Guarda.", Params: json.RawMessage(`{"type":"object"}`), Risk: domain.RiskWrite},
		Result:    domain.ToolResult{Content: "Guardado."},
		Question:  `Posso guardar na memória: "café"?`,
	}
	read := &fakes.Tool{
		SpecValue: domain.ToolSpec{Name: "read_file", Description: "Lê.", Params: json.RawMessage(`{"type":"object"}`), Risk: domain.RiskRead},
		Result:    domain.ToolResult{Content: "x"},
	}
	w.chat = &Chat{Sessions: w.sess, In: bufio.NewReader(strings.NewReader(input)), Out: w.out, LogPath: "/tmp/nexus.log"}
	w.chat.Asker = &orchestrator.Orchestrator{
		Models:    map[domain.Role]ports.ChatModel{domain.RoleMain: w.model},
		Sessions:  w.sess,
		Audit:     &fakes.Audit{},
		Tools:     []ports.Tool{w.save, summarized{read}},
		Codec:     cognition.JSONCodec{},
		Policy:    policy.New("memory_save", "read_file"),
		Confirmer: w.chat,
	}
	return w
}

type summarized struct{ *fakes.Tool }

func (summarized) Summary([]byte) string { return "lendo notas.md" }

func TestConversation(t *testing.T) {
	w := newWorld(t, "oi\n\n/ajuda\n/xyz\nqual o arquivo?\n/sair\n",
		"Olá! Tudo bem?", `{"tool":"read_file","args":{}}`, "O arquivo diz x.")
	if err := w.chat.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	out := w.out.String()
	for _, want := range []string{
		"Nexus · pode falar.",
		"você › nexus › Olá! Tudo bem?\n",
		"/nova    começa uma conversa nova",
		"Não conheço o comando /xyz",
		"  · lendo notas.md…\nnexus › O arquivo diz x.\n",
		"Até logo!",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("saída sem %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "\033[") {
		t.Error("sem Color, sem códigos ANSI")
	}
	if strings.Contains(out, `"tool"`) {
		t.Error("JSON de ferramenta vazou na tela")
	}
}

func TestResumeAndNew(t *testing.T) {
	w := newWorld(t, "/nova\nsair\n")
	ctx := context.Background()
	s, _ := w.sess.Create(ctx)
	_ = w.sess.Append(ctx, s.ID,
		domain.Message{Origin: domain.OriginUser, Content: "a"},
		domain.Message{Origin: domain.OriginAssistant, Content: "{}", ToolName: "x"},
		domain.Message{Origin: domain.OriginTool, Content: "r", ToolName: "x"},
		domain.Message{Origin: domain.OriginAssistant, Content: "b"})
	if err := w.chat.Run(ctx); err != nil {
		t.Fatal(err)
	}
	out := w.out.String()
	if !strings.Contains(out, "Continuando a conversa anterior (2 mensagens)") || !strings.Contains(out, "Conversa nova.") {
		t.Errorf("saída:\n%s", out)
	}
	if w.chat.session == s.ID {
		t.Error("/nova deveria trocar de sessão")
	}
}

func TestConfirmation(t *testing.T) {
	w := newWorld(t, "lembre do café\nsim\nde novo\nnão\n",
		`{"tool":"memory_save","args":{"text":"café"}}`, "Guardei!",
		`{"tool":"memory_save","args":{"text":"café"}}`, "Tudo bem.")
	if err := w.chat.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	out := w.out.String()
	if strings.Count(out, `? Posso guardar na memória: "café"? (s/n) `) != 2 {
		t.Errorf("perguntas:\n%s", out)
	}
	if w.save.RunCount() != 1 {
		t.Errorf("sim roda, não não roda: rodou %d vezes", w.save.RunCount())
	}
	if !strings.Contains(out, "ok, não fiz.") || !strings.Contains(out, "Guardei!") {
		t.Errorf("saída:\n%s", out)
	}
}

func TestConfirmEOFIsNo(t *testing.T) {
	w := newWorld(t, "lembre do café\n", `{"tool":"memory_save","args":{"text":"café"}}`, "Não guardei.")
	if err := w.chat.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if w.save.RunCount() != 0 {
		t.Error("fim da entrada na confirmação vale como não")
	}
}

func TestOnce(t *testing.T) {
	w := newWorld(t, "", "São 3 arquivos.")
	if err := w.chat.Once(context.Background(), "quantos arquivos?"); err != nil {
		t.Fatal(err)
	}
	if w.out.String() != "nexus › São 3 arquivos.\n" {
		t.Errorf("saída = %q", w.out.String())
	}
	w = newWorld(t, "")
	w.model.Err = domain.ErrModelDown
	if err := w.chat.Once(context.Background(), "oi"); err == nil {
		t.Error("falha deveria virar erro")
	}
	if !strings.Contains(w.out.String(), "nexus status") {
		t.Errorf("mensagem = %q", w.out.String())
	}
}

func TestFriendlyErrors(t *testing.T) {
	c := &Chat{LogPath: "/x/nexus.log"}
	for err, want := range map[error]string{
		context.Canceled:                             "(interrompido)",
		context.DeadlineExceeded:                     "Demorou demais",
		fmt.Errorf("x: %w", domain.ErrModelDown):     "nexus status",
		fmt.Errorf("x: %w", domain.ErrContextBudget): "trecho menor",
		errors.New("disco cheio"):                    "Algo deu errado: disco cheio. Detalhes em /x/nexus.log",
	} {
		if got := c.friendly(err); !strings.Contains(got, want) {
			t.Errorf("friendly(%v) = %q", err, got)
		}
	}
	c.LogPath = "stderr"
	if strings.Contains(c.friendly(errors.New("x")), "Detalhes") {
		t.Error("log no stderr não é citado")
	}
}

func TestStepLimitShowsNotice(t *testing.T) {
	call := `{"tool":"read_file","args":{}}`
	w := newWorld(t, "leia\nsair\n", call, call, call, call, call, call)
	if err := w.chat.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(w.out.String(), "Não consegui terminar em 6 passos") {
		t.Errorf("saída:\n%s", w.out.String())
	}
}

// slowAsker gera tokens até ser cancelado.
type slowAsker struct{ started chan struct{} }

func (s slowAsker) Ask(ctx context.Context, _, _ string, sink func(orchestrator.Event)) (orchestrator.Reply, error) {
	close(s.started)
	sink(orchestrator.Event{Kind: orchestrator.EventToken, Text: "pensando alto"})
	<-ctx.Done()
	return orchestrator.Reply{}, ctx.Err()
}

func TestInterrupt(t *testing.T) {
	pr, pw := io.Pipe()
	out := &syncBuffer{}
	started := make(chan struct{})
	c := &Chat{Asker: slowAsker{started}, Sessions: fakes.NewSessions(), In: bufio.NewReader(pr), Out: out, Color: true}
	if c.Interrupt() {
		t.Error("sem resposta em andamento, Interrupt devolve false")
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error)
	go func() { done <- c.Run(ctx) }()
	go func() { _, _ = pw.Write([]byte("conte uma história\n")) }()
	<-started
	if !c.Interrupt() {
		t.Fatal("com resposta em andamento, Interrupt devolve true")
	}
	waitFor(t, func() bool { return strings.Contains(out.String(), "(interrompido)") })
	// Ctrl+C no prompt: o contexto acaba e a conversa sai limpa.
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run não saiu com o contexto cancelado")
	}
	got := out.String()
	if !strings.Contains(got, "\033[1mnexus › \033[0mpensando alto\n") || !strings.Contains(got, "Até logo!") {
		t.Errorf("saída = %q", got)
	}
	pw.Close()
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condição não aconteceu a tempo")
}

func TestConfirmCanceled(t *testing.T) {
	pr, pw := io.Pipe()
	defer pw.Close()
	c := &Chat{In: bufio.NewReader(pr), Out: &syncBuffer{}}
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(20 * time.Millisecond); cancel() }()
	ok, err := c.Confirm(ctx, ports.ConfirmRequest{Question: "Posso?"})
	if ok || !errors.Is(err, context.Canceled) {
		t.Errorf("Confirm cancelado = %v, %v", ok, err)
	}
	// A linha digitada depois não se perde: vai para a próxima leitura.
	go func() { _, _ = pw.Write([]byte("s\n")) }()
	ok, err = c.Confirm(context.Background(), ports.ConfirmRequest{Question: "Posso?"})
	if !ok || err != nil {
		t.Errorf("Confirm depois = %v, %v", ok, err)
	}
}

func TestThinkingIndicator(t *testing.T) {
	out := &syncBuffer{}
	c := &Chat{Out: out, Color: true}
	r := &renderer{chat: c}
	r.thinking()
	r.thinking()
	r.event(orchestrator.Event{Kind: orchestrator.EventToolCall, Summary: "lendo a"})
	r.event(orchestrator.Event{Kind: orchestrator.EventToolResult, Denied: true, Text: "denied_policy"})
	r.event(orchestrator.Event{Kind: orchestrator.EventToken, Text: "fim\n"})
	r.endLine()
	got := out.String()
	if strings.Count(got, "pensando…") != 2 || !strings.Contains(got, "\r\033[K") || strings.HasSuffix(got, "\n\n") {
		t.Errorf("saída = %q", got)
	}
}
