// Package orchestrator roda o loop ReAct com orçamento de passos e de tempo.
//
// O modelo nunca executa nada: ele propõe, o orquestrador valida pela
// política, pede confirmação quando preciso, executa e audita.
package orchestrator

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"

	"nexusai/internal/core/cognition"
	"nexusai/internal/core/domain"
	"nexusai/internal/core/policy"
	"nexusai/internal/core/ports"
)

// EventKind são os mesmos eventos que a API SSE vai expor.
type EventKind string

const (
	EventStep       EventKind = "step"
	EventToolCall   EventKind = "tool_call"
	EventToolResult EventKind = "tool_result"
	EventToken      EventKind = "token"
	EventDone       EventKind = "done"
	EventError      EventKind = "error"
)

// Event é o que o cliente acompanha durante uma requisição.
type Event struct {
	Kind    EventKind
	Step    int
	Tool    string
	Summary string // linha curta para a tela: "lendo notas.md"
	Text    string // token, resposta final ou mensagem de erro
	Denied  bool   // tool_result de uma ação negada ou recusada
}

// Limits são os orçamentos de uma requisição. Zeros viram os padrões.
type Limits struct {
	MaxSteps        int
	RequestTimeout  time.Duration
	ToolTimeout     time.Duration
	MaxTokens       int     // tokens por geração
	Temperature     float64 // temperatura da amostragem
	MaxToolOutput   int     // caracteres de resultado de ferramenta que vão ao modelo
	MaxRetries      int     // novas tentativas por erro de formato ou argumento
	HistoryMessages int     // mensagens de histórico carregadas do store
	MemoryResults   int     // memórias buscadas por pergunta
	MemoryTokens    int     // teto de tokens de memória no prompt
}

func (l Limits) withDefaults() Limits {
	def := func(v *int, d int) {
		if *v <= 0 {
			*v = d
		}
	}
	def(&l.MaxSteps, 6)
	def(&l.MaxTokens, 1024)
	def(&l.MaxToolOutput, 8000)
	def(&l.MaxRetries, 2)
	def(&l.HistoryMessages, 60)
	def(&l.MemoryResults, 3)
	def(&l.MemoryTokens, 400)
	if l.RequestTimeout <= 0 {
		l.RequestTimeout = 2 * time.Minute
	}
	if l.ToolTimeout <= 0 {
		l.ToolTimeout = 30 * time.Second
	}
	return l
}

// Orchestrator liga modelo, memória, ferramentas e política.
type Orchestrator struct {
	Models    map[domain.Role]ports.ChatModel
	Sessions  ports.SessionStore
	Memory    ports.MemoryStore // opcional
	Audit     ports.AuditLog
	Tools     []ports.Tool
	Codec     cognition.ToolCallCodec
	Policy    policy.Policy
	Confirmer ports.Confirmer
	Metrics   ports.Metrics // opcional
	Log       *slog.Logger  // opcional
	System    string
	Limits    Limits

	Now        func() time.Time // opcional, para testes
	NewTraceID func() string    // opcional, para testes
}

// Reply é o resultado de uma requisição.
type Reply struct {
	Text    string
	Steps   int
	TraceID string
}

// Ask processa uma mensagem da pessoa na sessão e devolve a resposta final.
// sink recebe os eventos na ordem; pode ser nil. O turno inteiro é gravado
// numa transação só, no fim: ou ele está completo no histórico, ou não está.
func (o *Orchestrator) Ask(ctx context.Context, sessionID, text string, sink func(Event)) (Reply, error) {
	if sink == nil {
		sink = func(Event) {}
	}
	r := o.newRun(sessionID, sink)
	reply, err := r.loop(ctx, text)
	if err != nil {
		sink(Event{Kind: EventError, Text: err.Error()})
		r.log.Warn("requisição falhou", "error", err, "steps", reply.Steps)
		return reply, err
	}
	sink(Event{Kind: EventDone, Text: reply.Text, Step: reply.Steps})
	return reply, nil
}

type run struct {
	o         *Orchestrator
	limits    Limits
	sessionID string
	traceID   string
	sink      func(Event)
	log       *slog.Logger
	model     ports.ChatModel
	tools     map[string]ports.Tool
	specs     []domain.ToolSpec
	builder   *cognition.Builder
	caps      ports.Capabilities
	done      map[string]bool // chamadas já executadas neste turno
}

func (o *Orchestrator) newRun(sessionID string, sink func(Event)) *run {
	trace := o.traceID()
	log := o.Log
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	r := &run{
		o:         o,
		limits:    o.Limits.withDefaults(),
		sessionID: sessionID,
		traceID:   trace,
		sink:      sink,
		log:       log.With("trace_id", trace, "session_id", sessionID, "role", string(domain.RoleMain)),
		model:     o.Models[domain.RoleMain],
		tools:     make(map[string]ports.Tool, len(o.Tools)),
		done:      map[string]bool{},
	}
	for _, t := range o.Tools {
		spec := t.Spec()
		r.tools[spec.Name] = t
		if o.Policy.Allow[spec.Name] {
			r.specs = append(r.specs, spec) // o modelo só conhece o que pode usar
		}
	}
	return r
}

func (r *run) loop(ctx context.Context, text string) (Reply, error) {
	reply := Reply{TraceID: r.traceID}
	if r.model == nil {
		return reply, fmt.Errorf("%w: nenhum modelo no papel main", domain.ErrModelDown)
	}
	if strings.TrimSpace(text) == "" {
		return reply, errors.New("mensagem vazia")
	}
	ctx, cancel := context.WithTimeout(ctx, r.limits.RequestTimeout)
	defer cancel()
	start := r.o.now()
	defer func() { r.observe("nexus_request_seconds", r.o.now().Sub(start).Seconds()) }()

	history, err := r.o.Sessions.History(ctx, r.sessionID, r.limits.HistoryMessages)
	if err != nil {
		return reply, fmt.Errorf("carregar histórico: %w", err)
	}
	memories := r.recall(ctx, text)

	caps := r.model.Capabilities()
	r.caps = caps
	r.builder = &cognition.Builder{
		Codec:   r.o.Codec,
		Counter: r.model,
		Budget: cognition.Budget{
			ContextSize:      caps.ContextSize,
			ReserveForAnswer: r.limits.MaxTokens,
			MaxMemoryTokens:  r.limits.MemoryTokens,
		},
	}

	turn := []domain.Message{r.message(domain.OriginUser, text, "")}
	retries := 0
	for step := 1; step <= r.limits.MaxSteps; step++ {
		reply.Steps = step
		r.sink(Event{Kind: EventStep, Step: step})
		lg := r.log.With("step", step)

		msgs, err := r.builder.Build(ctx, cognition.PromptInput{
			System: r.o.System, Tools: r.specs, Memories: memories, History: history, Turn: turn,
		})
		if err != nil {
			return reply, err
		}

		guard := policy.NewStreamGuard(
			func(s string) { r.sink(Event{Kind: EventToken, Step: step, Text: s}) },
			func(kind string) { r.count("nexus_guard_blocks_total", "type", kind) },
		)
		gate := cognition.NewStreamGate(r.o.Codec, guard.Write)
		out, err := r.generate(ctx, msgs, gate.OnToken)
		if err != nil {
			return reply, err
		}

		dec, perr := r.o.Codec.Parse(out)
		if perr != nil {
			r.count("nexus_tool_parse_failures_total")
			lg.Info("chamada de ferramenta malformada", "error", perr)
			if retries >= r.limits.MaxRetries {
				return reply, fmt.Errorf("o modelo não conseguiu formatar a chamada de ferramenta: %w", perr)
			}
			retries++
			turn = append(turn,
				r.message(domain.OriginAssistant, out, "?"),
				r.message(domain.OriginTool, "Erro de formato: "+perr.Error()+`. Para usar ferramenta, responda só com {"tool": "<nome>", "args": {...}}; para responder, use texto normal.`, ""),
			)
			continue
		}

		if dec.Call == nil {
			gate.Finish()
			guard.Flush()
			final, _ := policy.Redact(dec.Final)
			reply.Text = final
			turn = append(turn, r.message(domain.OriginAssistant, final, ""))
			if err := r.persist(ctx, turn); err != nil {
				return reply, err
			}
			r.observe("nexus_react_steps", float64(step))
			lg.Info("resposta final", "chars", utf8.RuneCountInString(final))
			return reply, nil
		}

		call := *dec.Call
		result, retry := r.runTool(ctx, step, call)
		if retry {
			if retries >= r.limits.MaxRetries {
				return reply, fmt.Errorf("o modelo errou a chamada de %q depois de %d tentativas", call.Name, retries+1)
			}
			retries++
		}
		turn = append(turn,
			r.message(domain.OriginAssistant, r.o.Codec.FormatCall(call), call.Name),
			r.message(domain.OriginTool, result, call.Name),
		)
	}

	// Sem resposta dentro do orçamento: grava o turno com um aviso, para a
	// pessoa ver o que aconteceu e o histórico continuar coerente.
	notice := fmt.Sprintf("Não consegui terminar em %d passos. Tente pedir de outro jeito ou em partes menores.", r.limits.MaxSteps)
	turn = append(turn, r.message(domain.OriginAssistant, notice, ""))
	if err := r.persist(ctx, turn); err != nil {
		return reply, err
	}
	reply.Text = notice
	r.observe("nexus_react_steps", float64(r.limits.MaxSteps))
	return reply, domain.ErrStepLimit
}

// recall busca memórias relevantes. Falha na memória não derruba a conversa.
func (r *run) recall(ctx context.Context, text string) []domain.MemoryItem {
	if r.o.Memory == nil {
		return nil
	}
	start := r.o.now()
	items, err := r.o.Memory.Search(ctx, domain.MemoryQuery{Text: text, Limit: r.limits.MemoryResults})
	r.observe("nexus_memory_search_seconds", r.o.now().Sub(start).Seconds())
	if err != nil {
		r.log.Warn("busca de memória falhou", "error", err)
		return nil
	}
	return items
}

func (r *run) generate(ctx context.Context, msgs []ports.ChatMessage, onToken func(string)) (string, error) {
	start := r.o.now()
	first := true
	req := ports.GenerateRequest{
		Messages:    msgs,
		MaxTokens:   r.limits.MaxTokens,
		Temperature: r.limits.Temperature,
	}
	if r.caps.Grammar {
		// O Qwen às vezes troca para chinês no meio da frase; a gramática
		// torna esses caracteres impossíveis de gerar.
		req.Grammar = cognition.NoCJKGrammar
	}
	comp, err := r.model.Generate(ctx, req, func(tok string) {
		if first {
			first = false
			r.observe("nexus_time_to_first_token_seconds", r.o.now().Sub(start).Seconds(), "role", string(domain.RoleMain))
		}
		onToken(tok)
	})
	if err != nil {
		return "", fmt.Errorf("gerar resposta: %w", err)
	}
	if elapsed := r.o.now().Sub(start).Seconds(); elapsed > 0 && comp.CompletionTokens > 0 {
		r.observe("nexus_tokens_per_second", float64(comp.CompletionTokens)/elapsed, "role", string(domain.RoleMain))
	}
	return comp.Text, nil
}

// runTool valida, confirma, executa e audita uma chamada. Devolve o texto que
// volta ao modelo e se o erro foi do modelo (conta como nova tentativa).
func (r *run) runTool(ctx context.Context, step int, call domain.ToolCall) (string, bool) {
	audit := domain.ToolRun{
		SessionID: r.sessionID, TraceID: r.traceID, Tool: call.Name,
		Args: string(call.Args), CreatedAt: r.o.now(),
	}
	finish := func(decision, result string, err error, denied, retry bool) (string, bool) {
		audit.Decision = decision
		audit.Result = truncate(result, 2000)
		if err != nil {
			audit.Error = err.Error()
		}
		if aerr := r.o.Audit.RecordToolRun(context.WithoutCancel(ctx), audit); aerr != nil {
			// Sem auditoria, sem execução silenciosa: o resultado vira erro.
			r.log.Error("auditoria falhou", "tool", call.Name, "error", aerr)
			result, denied = "Erro interno ao registrar a ferramenta; o resultado foi descartado.", true
		}
		r.count("nexus_tool_calls_total", "tool", call.Name, "decision", decision)
		r.sink(Event{Kind: EventToolResult, Step: step, Tool: call.Name, Denied: denied, Text: decision})
		return result, retry
	}

	// Modelo pequeno às vezes repete a mesma chamada em loop: o resultado já
	// está no prompt, então não executa de novo e manda responder.
	key := callKey(call)
	if r.done[key] {
		return finish("duplicate", fmt.Sprintf("Você já chamou %s com esses mesmos argumentos neste turno; o resultado está acima. Use-o e responda à pessoa agora.", call.Name), nil, true, false)
	}

	tool, ok := r.tools[call.Name]
	if !ok {
		return finish("unknown_tool", fmt.Sprintf("A ferramenta %q não existe. Use uma das ferramentas listadas.", call.Name), nil, true, true)
	}
	spec := tool.Spec()
	audit.Risk = spec.Risk
	verdict := r.o.Policy.Check(spec)
	if !verdict.Allowed {
		return finish("denied_policy", "Negado: "+verdict.Reason+".", nil, true, false)
	}
	if err := policy.ValidateArgs(spec.Params, call.Args); err != nil {
		return finish("invalid_args", "Argumentos inválidos: "+err.Error()+". Corrija e tente de novo.", err, true, true)
	}

	summary := spec.Name
	if s, ok := tool.(interface{ Summary(args []byte) string }); ok {
		summary = s.Summary(call.Args)
	}
	r.sink(Event{Kind: EventToolCall, Step: step, Tool: call.Name, Summary: summary})

	if verdict.NeedsConfirmation {
		question := fmt.Sprintf("Posso usar %s?", spec.Name)
		if c, ok := tool.(ports.Confirmable); ok {
			if q := c.ConfirmQuestion(call.Args); q != "" {
				question = q
			}
		}
		yes := false
		if r.o.Confirmer != nil {
			var err error
			yes, err = r.o.Confirmer.Confirm(ctx, ports.ConfirmRequest{Tool: spec.Name, Risk: spec.Risk, Question: question})
			if err != nil {
				yes = false
			}
		}
		if !yes {
			return finish("denied_user", "A pessoa não autorizou esta ação. Não tente de novo; responda sem ela.", nil, true, false)
		}
	}

	tctx, cancel := context.WithTimeout(ctx, r.limits.ToolTimeout)
	defer cancel()
	start := r.o.now()
	res, err := tool.Run(tctx, call.Args)
	audit.Duration = r.o.now().Sub(start)
	r.observe("nexus_tool_duration_seconds", audit.Duration.Seconds(), "tool", call.Name)
	if err != nil {
		r.count("nexus_tool_errors_total", "tool", call.Name)
		return finish("error", "A ferramenta falhou: "+err.Error(), err, false, false)
	}
	r.done[key] = true
	return finish("allowed", truncate(res.Content, r.limits.MaxToolOutput), nil, false, false)
}

func callKey(call domain.ToolCall) string {
	var buf bytes.Buffer
	if err := json.Compact(&buf, call.Args); err != nil {
		return call.Name + "|" + string(call.Args)
	}
	return call.Name + "|" + buf.String()
}

func (r *run) persist(ctx context.Context, turn []domain.Message) error {
	// O turno terminou: grava mesmo que o cliente desconecte agora.
	pctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if err := r.o.Sessions.Append(pctx, r.sessionID, turn...); err != nil {
		return fmt.Errorf("gravar a conversa: %w", err)
	}
	return nil
}

func (r *run) message(origin domain.Origin, content, tool string) domain.Message {
	return domain.Message{SessionID: r.sessionID, Origin: origin, Content: content, ToolName: tool, CreatedAt: r.o.now()}
}

func (r *run) observe(name string, v float64, labels ...string) {
	if r.o.Metrics != nil {
		r.o.Metrics.Observe(name, v, labels...)
	}
}

func (r *run) count(name string, labels ...string) { r.observe(name, 1, labels...) }

func (o *Orchestrator) now() time.Time {
	if o.Now != nil {
		return o.Now()
	}
	return time.Now()
}

func (o *Orchestrator) traceID() string {
	if o.NewTraceID != nil {
		return o.NewTraceID()
	}
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func truncate(s string, max int) string {
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	runes := []rune(s)
	return string(runes[:max]) + fmt.Sprintf("\n[cortado: mais %d caracteres]", len(runes)-max)
}
