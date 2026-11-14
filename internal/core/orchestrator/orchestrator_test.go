package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"nexusai/internal/core/cognition"
	"nexusai/internal/core/domain"
	"nexusai/internal/core/fakes"
	"nexusai/internal/core/policy"
	"nexusai/internal/core/ports"
)

const pathSchema = `{"type":"object","properties":{"path":{"type":"string","minLength":1}},"required":["path"],"additionalProperties":false}`

type env struct {
	o       *Orchestrator
	model   *fakes.Model
	sess    *fakes.Sessions
	mem     *fakes.Memory
	audit   *fakes.Audit
	conf    *fakes.Confirmer
	metrics *fakes.Metrics
	read    *fakes.Tool
	write   *fakes.Tool
	sid     string
	events  []Event
}

func newEnv(t *testing.T, replies ...string) *env {
	t.Helper()
	e := &env{
		model:   &fakes.Model{Replies: replies},
		sess:    fakes.NewSessions(),
		mem:     &fakes.Memory{},
		audit:   &fakes.Audit{},
		conf:    &fakes.Confirmer{},
		metrics: &fakes.Metrics{},
		read: &fakes.Tool{
			SpecValue: domain.ToolSpec{Name: "read_file", Description: "Lê um arquivo.", Params: json.RawMessage(pathSchema), Risk: domain.RiskRead},
			Result:    domain.ToolResult{Content: "conteúdo do arquivo"},
		},
		write: &fakes.Tool{
			SpecValue: domain.ToolSpec{Name: "memory_save", Description: "Guarda.", Params: json.RawMessage(`{"type":"object","properties":{"text":{"type":"string"}},"required":["text"]}`), Risk: domain.RiskWrite},
			Result:    domain.ToolResult{Content: "guardado"},
			Question:  "Posso guardar isso?",
		},
	}
	e.o = &Orchestrator{
		Models:     map[domain.Role]ports.ChatModel{domain.RoleMain: e.model},
		Sessions:   e.sess,
		Memory:     e.mem,
		Audit:      e.audit,
		Tools:      []ports.Tool{e.read, e.write},
		Codec:      cognition.JSONCodec{},
		Policy:     policy.New("read_file", "memory_save"),
		Confirmer:  e.conf,
		Metrics:    e.metrics,
		System:     "Você é o Nexus.",
		NewTraceID: func() string { return "trace-1" },
	}
	s, err := e.sess.Create(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	e.sid = s.ID
	return e
}

func (e *env) ask(text string) (Reply, error) {
	e.events = nil
	return e.o.Ask(context.Background(), e.sid, text, func(ev Event) { e.events = append(e.events, ev) })
}

func (e *env) tokens() string {
	var b strings.Builder
	for _, ev := range e.events {
		if ev.Kind == EventToken {
			b.WriteString(ev.Text)
		}
	}
	return b.String()
}

func (e *env) kinds() string {
	var out []string
	for _, ev := range e.events {
		if ev.Kind != EventToken {
			out = append(out, string(ev.Kind))
		}
	}
	return strings.Join(out, ",")
}

func (e *env) history(t *testing.T) []domain.Message {
	t.Helper()
	h, err := e.sess.History(context.Background(), e.sid, 0)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func TestSimpleAnswer(t *testing.T) {
	e := newEnv(t, "Olá! Em que posso ajudar?")
	r, err := e.ask("oi")
	if err != nil {
		t.Fatal(err)
	}
	if r.Text != "Olá! Em que posso ajudar?" || r.Steps != 1 || r.TraceID != "trace-1" {
		t.Errorf("reply = %+v", r)
	}
	if e.tokens() != r.Text {
		t.Errorf("streaming = %q", e.tokens())
	}
	if e.kinds() != "step,done" {
		t.Errorf("eventos = %s", e.kinds())
	}
	h := e.history(t)
	if len(h) != 2 || h[0].Origin != domain.OriginUser || h[1].Content != r.Text {
		t.Errorf("histórico = %+v", h)
	}
	if e.metrics.Sum("nexus_react_steps") != 1 || len(e.metrics.Obs["nexus_time_to_first_token_seconds{role,main}"]) != 1 {
		t.Errorf("métricas = %v", e.metrics.Obs)
	}
}

func TestToolCallThenAnswer(t *testing.T) {
	e := newEnv(t, `{"tool": "read_file", "args": {"path": "notas.md"}}`, "O arquivo diz: conteúdo do arquivo.")
	r, err := e.ask("o que tem em notas.md?")
	if err != nil {
		t.Fatal(err)
	}
	if r.Steps != 2 || e.read.RunCount() != 1 {
		t.Fatalf("steps=%d runs=%d", r.Steps, e.read.RunCount())
	}
	if strings.Contains(e.tokens(), "tool") {
		t.Errorf("a chamada não pode ir para a tela: %q", e.tokens())
	}
	if e.kinds() != "step,tool_call,tool_result,step,done" {
		t.Errorf("eventos = %s", e.kinds())
	}
	second := e.model.Requests[1].Messages
	last := second[len(second)-1]
	if last.Role != "user" || !strings.Contains(last.Content, `<dado origem="tool" ferramenta="read_file">`) || !strings.Contains(last.Content, "conteúdo do arquivo") {
		t.Errorf("resultado deveria voltar como dado: %+v", last)
	}
	if !strings.Contains(second[0].Content, "read_file") || strings.Contains(second[0].Content, "shell") {
		t.Error("o sistema deveria listar só ferramentas liberadas")
	}
	if len(e.audit.Runs) != 1 || e.audit.Runs[0].Decision != "allowed" || e.audit.Runs[0].TraceID != "trace-1" || e.audit.Runs[0].Risk != domain.RiskRead {
		t.Errorf("auditoria = %+v", e.audit.Runs)
	}
	if h := e.history(t); len(h) != 4 || h[1].ToolName != "read_file" || h[2].Origin != domain.OriginTool {
		t.Errorf("histórico = %+v", h)
	}
}

func TestWriteNeedsConfirmation(t *testing.T) {
	e := newEnv(t, `{"tool": "memory_save", "args": {"text": "gosta de café"}}`, "Tudo bem, não guardei.")
	if _, err := e.ask("lembre que gosto de café"); err != nil {
		t.Fatal(err)
	}
	if e.write.RunCount() != 0 {
		t.Fatal("escrita rodou sem confirmação")
	}
	if len(e.conf.Questions) != 1 || e.conf.Questions[0].Question != "Posso guardar isso?" || e.conf.Questions[0].Risk != domain.RiskWrite {
		t.Errorf("perguntas = %+v", e.conf.Questions)
	}
	if e.audit.Runs[0].Decision != "denied_user" {
		t.Errorf("auditoria = %+v", e.audit.Runs)
	}
	for _, ev := range e.events {
		if ev.Kind == EventToolResult && !ev.Denied {
			t.Error("tool_result de ação recusada deveria vir marcado")
		}
	}

	e = newEnv(t, `{"tool": "memory_save", "args": {"text": "gosta de café"}}`, "Guardado!")
	e.conf.Answer = true
	if _, err := e.ask("lembre que gosto de café"); err != nil {
		t.Fatal(err)
	}
	if e.write.RunCount() != 1 || e.audit.Runs[0].Decision != "allowed" {
		t.Errorf("com sim deveria rodar: runs=%d audit=%+v", e.write.RunCount(), e.audit.Runs)
	}
}

func TestNoConfirmerMeansNo(t *testing.T) {
	e := newEnv(t, `{"tool": "memory_save", "args": {"text": "x"}}`, "ok")
	e.o.Confirmer = nil
	if _, err := e.ask("guarde x"); err != nil {
		t.Fatal(err)
	}
	if e.write.RunCount() != 0 {
		t.Error("sem quem confirme, a escrita não roda")
	}
	if q := (&fakes.Tool{}).ConfirmQuestion(nil); q != "" {
		t.Error("pergunta padrão vazia")
	}
}

func TestDefaultQuestion(t *testing.T) {
	e := newEnv(t, `{"tool": "memory_save", "args": {"text": "x"}}`, "ok")
	e.write.Question = ""
	if _, err := e.ask("guarde x"); err != nil {
		t.Fatal(err)
	}
	if e.conf.Questions[0].Question != "Posso usar memory_save?" {
		t.Errorf("pergunta = %q", e.conf.Questions[0].Question)
	}
}

func TestPolicyDenied(t *testing.T) {
	e := newEnv(t, `{"tool": "memory_save", "args": {"text": "x"}}`, "Não posso.")
	e.o.Policy = policy.New("read_file")
	if _, err := e.ask("guarde x"); err != nil {
		t.Fatal(err)
	}
	if e.write.RunCount() != 0 || len(e.conf.Questions) != 0 || e.audit.Runs[0].Decision != "denied_policy" {
		t.Errorf("runs=%d perguntas=%d audit=%+v", e.write.RunCount(), len(e.conf.Questions), e.audit.Runs)
	}
	if strings.Contains(e.model.Requests[0].Messages[0].Content, "memory_save") {
		t.Error("ferramenta negada não deveria aparecer no prompt")
	}
}

func TestUnknownToolRetriesThenFails(t *testing.T) {
	e := newEnv(t, `{"tool": "shell", "args": {}}`, `{"tool": "shell", "args": {}}`, `{"tool": "shell", "args": {}}`)
	_, err := e.ask("rode ls")
	if err == nil || !strings.Contains(err.Error(), "shell") {
		t.Fatalf("err = %v", err)
	}
	if e.model.Calls() != 3 || len(e.audit.Runs) != 3 || e.audit.Runs[0].Decision != "unknown_tool" {
		t.Errorf("calls=%d audit=%+v", e.model.Calls(), e.audit.Runs)
	}
	if len(e.history(t)) != 0 {
		t.Error("turno com erro não é gravado")
	}
	if e.events[len(e.events)-1].Kind != EventError {
		t.Error("último evento deveria ser error")
	}
}

func TestInvalidArgsRetry(t *testing.T) {
	e := newEnv(t, `{"tool": "read_file", "args": {"file": "a"}}`, `{"tool": "read_file", "args": {"path": "a"}}`, "Pronto.")
	r, err := e.ask("leia a")
	if err != nil {
		t.Fatal(err)
	}
	if r.Steps != 3 || e.read.RunCount() != 1 {
		t.Errorf("steps=%d runs=%d", r.Steps, e.read.RunCount())
	}
	if e.audit.Runs[0].Decision != "invalid_args" || e.audit.Runs[0].Error == "" {
		t.Errorf("auditoria = %+v", e.audit.Runs[0])
	}
}

func TestMalformedCallRetry(t *testing.T) {
	e := newEnv(t, `{"tool": "read_file", "args": {"path": "a"`, "Desculpe, aqui está a resposta.")
	r, err := e.ask("leia a")
	if err != nil {
		t.Fatal(err)
	}
	if r.Steps != 2 || e.metrics.Sum("nexus_tool_parse_failures_total") != 1 {
		t.Errorf("steps=%d métricas=%v", r.Steps, e.metrics.Obs)
	}
	last := e.model.Requests[1].Messages
	if !strings.Contains(last[len(last)-1].Content, "Erro de formato") {
		t.Errorf("o erro deveria voltar ao modelo: %q", last[len(last)-1].Content)
	}
	if e.tokens() != "Desculpe, aqui está a resposta." {
		t.Errorf("tela = %q", e.tokens())
	}
}

func TestMalformedTooManyTimes(t *testing.T) {
	bad := `{"tool": "read_file"`
	e := newEnv(t, bad, bad, bad)
	if _, err := e.ask("leia"); err == nil || !strings.Contains(err.Error(), "formatar") {
		t.Fatalf("err = %v", err)
	}
	if e.model.Calls() != 3 {
		t.Errorf("deveria tentar 1 + 2 vezes, foram %d", e.model.Calls())
	}
}

func TestStepLimit(t *testing.T) {
	call := `{"tool": "read_file", "args": {"path": "a"}}`
	e := newEnv(t, call, call, call)
	e.o.Limits.MaxSteps = 3
	r, err := e.ask("leia para sempre")
	if !errors.Is(err, domain.ErrStepLimit) {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(r.Text, "3 passos") {
		t.Errorf("aviso = %q", r.Text)
	}
	h := e.history(t)
	if len(h) != 8 || h[len(h)-1].Content != r.Text {
		t.Errorf("turno com aviso deveria ser gravado: %d mensagens", len(h))
	}
}

func TestSecretsAreRedacted(t *testing.T) {
	key := "sk-abcdefghijklmnopqrstuvwxyz123456"
	e := newEnv(t, "Sua chave é "+key+" ok")
	r, err := e.ask("qual minha chave?")
	if err != nil {
		t.Fatal(err)
	}
	for name, s := range map[string]string{"tela": e.tokens(), "resposta": r.Text, "histórico": e.history(t)[1].Content} {
		if strings.Contains(s, key) || !strings.Contains(s, policy.Redacted) {
			t.Errorf("%s vazou: %q", name, s)
		}
	}
	if e.metrics.Sum("nexus_guard_blocks_total{type,openai_key}") != 1 {
		t.Errorf("métricas = %v", e.metrics.Obs)
	}
}

func TestModelError(t *testing.T) {
	e := newEnv(t)
	e.model.Err = domain.ErrModelDown
	if _, err := e.ask("oi"); !errors.Is(err, domain.ErrModelDown) {
		t.Fatalf("err = %v", err)
	}
	if len(e.history(t)) != 0 {
		t.Error("nada deveria ser gravado")
	}
	e.o.Models = nil
	if _, err := e.ask("oi"); !errors.Is(err, domain.ErrModelDown) {
		t.Fatalf("sem modelo: %v", err)
	}
	e.o.Models = map[domain.Role]ports.ChatModel{domain.RoleMain: &fakes.Model{Replies: []string{"x"}}}
	if _, err := e.ask("   "); err == nil {
		t.Error("mensagem vazia deveria falhar")
	}
}

func TestAuditFailureDiscardsResult(t *testing.T) {
	e := newEnv(t, `{"tool": "read_file", "args": {"path": "a"}}`, "ok")
	e.audit.Err = errors.New("disco cheio")
	if _, err := e.ask("leia a"); err != nil {
		t.Fatal(err)
	}
	msgs := e.model.Requests[1].Messages
	if got := msgs[len(msgs)-1].Content; strings.Contains(got, "conteúdo do arquivo") || !strings.Contains(got, "descartado") {
		t.Errorf("sem auditoria o resultado não pode chegar ao modelo: %q", got)
	}
}

func TestMemoryRecall(t *testing.T) {
	e := newEnv(t, "Você gosta de café.")
	if _, err := e.mem.Save(context.Background(), domain.MemoryItem{Text: "A pessoa gosta de café", Trust: domain.TrustHigh}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.ask("do que eu gosto? café?"); err != nil {
		t.Fatal(err)
	}
	if sys := e.model.Requests[0].Messages[0].Content; !strings.Contains(sys, "A pessoa gosta de café") {
		t.Errorf("memória não entrou no prompt:\n%s", sys)
	}
	e = newEnv(t, "ok")
	e.o.Memory = errMemory{}
	if _, err := e.ask("oi"); err != nil {
		t.Errorf("falha na memória não derruba a conversa: %v", err)
	}
	e = newEnv(t, "ok")
	e.o.Memory = nil
	if _, err := e.ask("oi"); err != nil {
		t.Error(err)
	}
}

type errMemory struct{}

func (errMemory) Save(context.Context, domain.MemoryItem) (domain.MemoryItem, error) {
	return domain.MemoryItem{}, errors.New("x")
}
func (errMemory) Search(context.Context, domain.MemoryQuery) ([]domain.MemoryItem, error) {
	return nil, errors.New("busca fora")
}

func TestToolErrorAndTimeout(t *testing.T) {
	e := newEnv(t, `{"tool": "read_file", "args": {"path": "a"}}`, "Não consegui ler.")
	e.read.Err = errors.New("permissão negada")
	if _, err := e.ask("leia a"); err != nil {
		t.Fatal(err)
	}
	msgs := e.model.Requests[1].Messages
	if !strings.Contains(msgs[len(msgs)-1].Content, "permissão negada") || e.audit.Runs[0].Decision != "error" {
		t.Errorf("erro da ferramenta deveria voltar ao modelo e à auditoria")
	}

	e = newEnv(t, `{"tool": "read_file", "args": {"path": "a"}}`, "Demorou demais.")
	e.read.Delay = time.Second
	e.read.Err = nil
	e.o.Limits.ToolTimeout = 20 * time.Millisecond
	if _, err := e.ask("leia a"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(e.audit.Runs[0].Error, "deadline") {
		t.Errorf("timeout = %+v", e.audit.Runs[0])
	}
}

func TestToolOutputTruncated(t *testing.T) {
	e := newEnv(t, `{"tool": "read_file", "args": {"path": "a"}}`, "ok")
	e.read.Result.Content = strings.Repeat("á", 100)
	e.o.Limits.MaxToolOutput = 10
	if _, err := e.ask("leia a"); err != nil {
		t.Fatal(err)
	}
	msgs := e.model.Requests[1].Messages
	if got := msgs[len(msgs)-1].Content; !strings.Contains(got, strings.Repeat("á", 10)+"\n[cortado: mais 90 caracteres]") {
		t.Errorf("corte = %q", got)
	}
}

type summaryTool struct{ *fakes.Tool }

func (summaryTool) Summary([]byte) string { return "lendo a.txt" }

func TestSummaryEvent(t *testing.T) {
	e := newEnv(t, `{"tool": "read_file", "args": {"path": "a"}}`, "ok")
	e.o.Tools = []ports.Tool{summaryTool{e.read}}
	if _, err := e.ask("leia a"); err != nil {
		t.Fatal(err)
	}
	for _, ev := range e.events {
		if ev.Kind == EventToolCall && ev.Summary != "lendo a.txt" {
			t.Errorf("summary = %q", ev.Summary)
		}
	}
}

func TestCancelAndBudget(t *testing.T) {
	e := newEnv(t, strings.Repeat("palavra ", 50))
	ctx, cancel := context.WithCancel(context.Background())
	e.model.Chunk = 1
	n := 0
	_, err := e.o.Ask(ctx, e.sid, "fale", func(ev Event) {
		if ev.Kind == EventToken {
			n++
			if n == 3 {
				cancel()
			}
		}
	})
	if !errors.Is(err, context.Canceled) || len(e.history(t)) != 0 {
		t.Fatalf("cancelar deveria parar sem gravar: %v", err)
	}

	e = newEnv(t, "ok")
	e.model.Context = 20
	if _, err := e.ask("oi"); !errors.Is(err, domain.ErrContextBudget) {
		t.Errorf("janela pequena: %v", err)
	}
}

func TestHistoryAcrossTurns(t *testing.T) {
	e := newEnv(t, "Prazer, Ana!", "Seu nome é Ana.")
	if _, err := e.ask("meu nome é Ana"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.ask("qual meu nome?"); err != nil {
		t.Fatal(err)
	}
	msgs := e.model.Requests[1].Messages
	if len(msgs) != 4 || msgs[1].Content != "meu nome é Ana" || msgs[2].Content != "Prazer, Ana!" {
		t.Errorf("histórico no prompt = %+v", msgs)
	}
}

func TestPersistError(t *testing.T) {
	e := newEnv(t, "ok")
	e.sess.Err = errors.New("disco cheio")
	if _, err := e.ask("oi"); err == nil || !strings.Contains(err.Error(), "gravar") {
		t.Errorf("err = %v", err)
	}
	e = newEnv(t, `{"tool": "read_file", "args": {"path": "a"}}`)
	e.o.Limits.MaxSteps = 1
	e.sess.Err = errors.New("disco cheio")
	if _, err := e.ask("oi"); err == nil || errors.Is(err, domain.ErrStepLimit) {
		t.Errorf("falha ao gravar o aviso: %v", err)
	}
	if _, err := e.o.Ask(context.Background(), "nao-existe", "oi", nil); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("sessão inexistente: %v", err)
	}
}

func TestDefaults(t *testing.T) {
	l := Limits{}.withDefaults()
	if l.MaxSteps != 6 || l.RequestTimeout != 2*time.Minute || l.MaxRetries != 2 {
		t.Errorf("padrões = %+v", l)
	}
	o := &Orchestrator{}
	if len(o.traceID()) != 16 || o.now().IsZero() {
		t.Error("trace e relógio padrão")
	}
	if truncate("abc", 5) != "abc" {
		t.Error("truncate curto")
	}
}

func TestDuplicateCallNotRepeated(t *testing.T) {
	call := `{"tool": "read_file", "args": {"path": "a"}}`
	e := newEnv(t, call, `{"tool":"read_file","args":{ "path":"a" }}`, "Pronto.")
	r, err := e.ask("leia a")
	if err != nil {
		t.Fatal(err)
	}
	if e.read.RunCount() != 1 || r.Steps != 3 {
		t.Errorf("runs=%d steps=%d", e.read.RunCount(), r.Steps)
	}
	if len(e.audit.Runs) != 2 || e.audit.Runs[1].Decision != "duplicate" {
		t.Errorf("auditoria = %+v", e.audit.Runs)
	}
	msgs := e.model.Requests[2].Messages
	if !strings.Contains(msgs[len(msgs)-1].Content, "já chamou read_file") {
		t.Errorf("aviso = %q", msgs[len(msgs)-1].Content)
	}
	if callKey(domain.ToolCall{Name: "x", Args: json.RawMessage(`{`)}) != "x|{" {
		t.Error("callKey com JSON inválido")
	}
}

func TestGrammarOnlyWhenSupported(t *testing.T) {
	e := newEnv(t, "oi", "oi")
	if _, err := e.ask("oi"); err != nil {
		t.Fatal(err)
	}
	if e.model.Requests[0].Grammar != "" {
		t.Error("sem suporte, sem gramática")
	}
	e.model.Grammar = true
	if _, err := e.ask("oi"); err != nil {
		t.Fatal(err)
	}
	if e.model.Requests[1].Grammar != cognition.NoCJKGrammar {
		t.Errorf("gramática = %q", e.model.Requests[1].Grammar)
	}
}
