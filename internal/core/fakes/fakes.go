// Package fakes tem implementações em memória das portas, para testar o core
// sem modelo, sem banco e sem rede. Cada fake passa na suíte de contrato da
// sua porta (internal/core/ports/contract).
package fakes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"nexusai/internal/core/domain"
	"nexusai/internal/core/ports"
)

// Model é um modelo com respostas roteirizadas, na ordem.
type Model struct {
	mu       sync.Mutex
	Replies  []string
	Err      error // devolvido em toda geração, se não nil
	Requests []ports.GenerateRequest
	Context  int
	Grammar  bool // declara suporte a gramática
	// Chunk divide cada resposta em pedaços deste tamanho no streaming (0 = 3).
	Chunk int
	// Respond, se definido, gera a resposta a partir do pedido.
	Respond func(req ports.GenerateRequest) string
}

// Generate implementa ports.ChatModel.
func (m *Model) Generate(ctx context.Context, req ports.GenerateRequest, onToken func(string)) (ports.Completion, error) {
	m.mu.Lock()
	m.Requests = append(m.Requests, req)
	var reply string
	switch {
	case m.Err != nil:
		m.mu.Unlock()
		return ports.Completion{}, m.Err
	case m.Respond != nil:
		reply = m.Respond(req)
	case len(m.Replies) == 0:
		m.mu.Unlock()
		return ports.Completion{}, errors.New("fakes.Model: sem respostas roteirizadas")
	default:
		reply = m.Replies[0]
		m.Replies = m.Replies[1:]
	}
	chunk := m.Chunk
	m.mu.Unlock()
	if chunk <= 0 {
		chunk = 3
	}
	runes := []rune(reply)
	for i := 0; i < len(runes); i += chunk {
		if err := ctx.Err(); err != nil {
			return ports.Completion{}, err
		}
		end := min(i+chunk, len(runes))
		if onToken != nil {
			onToken(string(runes[i:end]))
		}
	}
	return ports.Completion{Text: reply, PromptTokens: 10, CompletionTokens: len(strings.Fields(reply)), FinishReason: "stop"}, nil
}

// CountTokens implementa ports.ChatModel: uma palavra, um token.
func (m *Model) CountTokens(_ context.Context, text string) (int, error) {
	return len(strings.Fields(text)), nil
}

// Capabilities implementa ports.ChatModel.
func (m *Model) Capabilities() ports.Capabilities {
	ctx := m.Context
	if ctx == 0 {
		ctx = 8192
	}
	return ports.Capabilities{ContextSize: ctx, Grammar: m.Grammar}
}

// Calls devolve quantas gerações foram pedidas.
func (m *Model) Calls() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.Requests)
}

// Sessions guarda sessões em memória.
type Sessions struct {
	mu       sync.Mutex
	sessions map[string]domain.Session
	messages map[string][]domain.Message
	nextID   int64
	clock    int64
	Err      error // devolvido em Append, se não nil
}

// NewSessions cria um SessionStore vazio.
func NewSessions() *Sessions {
	return &Sessions{sessions: map[string]domain.Session{}, messages: map[string][]domain.Message{}}
}

func (s *Sessions) tick() time.Time {
	s.clock++
	return time.Unix(0, 0).Add(time.Duration(s.clock) * time.Millisecond)
}

// Create implementa ports.SessionStore.
func (s *Sessions) Create(context.Context) (domain.Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.tick()
	sess := domain.Session{ID: fmt.Sprintf("s%d", len(s.sessions)+1), CreatedAt: now, UpdatedAt: now}
	s.sessions[sess.ID] = sess
	return sess, nil
}

// Latest implementa ports.SessionStore.
func (s *Sessions) Latest(context.Context) (domain.Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var best domain.Session
	for _, sess := range s.sessions {
		if best.ID == "" || sess.UpdatedAt.After(best.UpdatedAt) {
			best = sess
		}
	}
	if best.ID == "" {
		return domain.Session{}, domain.ErrNotFound
	}
	return best, nil
}

// Append implementa ports.SessionStore.
func (s *Sessions) Append(_ context.Context, sessionID string, msgs ...domain.Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Err != nil {
		return s.Err
	}
	sess, ok := s.sessions[sessionID]
	if !ok {
		return fmt.Errorf("sessão %q: %w", sessionID, domain.ErrNotFound)
	}
	for _, m := range msgs {
		s.nextID++
		m.ID = s.nextID
		m.SessionID = sessionID
		s.messages[sessionID] = append(s.messages[sessionID], m)
	}
	sess.UpdatedAt = s.tick()
	s.sessions[sessionID] = sess
	return nil
}

// History implementa ports.SessionStore.
func (s *Sessions) History(_ context.Context, sessionID string, limit int) ([]domain.Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.sessions[sessionID]; !ok {
		return nil, fmt.Errorf("sessão %q: %w", sessionID, domain.ErrNotFound)
	}
	all := s.messages[sessionID]
	if limit > 0 && len(all) > limit {
		all = all[len(all)-limit:]
	}
	return append([]domain.Message(nil), all...), nil
}

// Memory guarda memórias em memória, com busca por palavras em comum.
type Memory struct {
	mu    sync.Mutex
	items []domain.MemoryItem
}

// Save implementa ports.MemoryStore.
func (m *Memory) Save(_ context.Context, item domain.MemoryItem) (domain.MemoryItem, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if strings.TrimSpace(item.Text) == "" {
		return domain.MemoryItem{}, errors.New("memória vazia")
	}
	item.ID = int64(len(m.items) + 1)
	m.items = append(m.items, item)
	return item, nil
}

// Search implementa ports.MemoryStore: mais palavras em comum primeiro.
func (m *Memory) Search(_ context.Context, q domain.MemoryQuery) ([]domain.MemoryItem, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	words := Words(q.Text)
	type scored struct {
		item  domain.MemoryItem
		score int
	}
	var hits []scored
	for _, it := range m.items {
		have := map[string]bool{}
		for _, w := range Words(it.Text) {
			have[w] = true
		}
		score := 0
		for _, w := range words {
			if have[w] {
				score++
			}
		}
		if score > 0 {
			hits = append(hits, scored{it, score})
		}
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].score > hits[j].score })
	limit := q.Limit
	if limit <= 0 {
		limit = 5
	}
	var out []domain.MemoryItem
	for i := 0; i < len(hits) && i < limit; i++ {
		out = append(out, hits[i].item)
	}
	return out, nil
}

// Items devolve tudo que foi salvo.
func (m *Memory) Items() []domain.MemoryItem {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]domain.MemoryItem(nil), m.items...)
}

// Words separa as palavras de 3 letras ou mais, em minúsculas.
func Words(s string) []string {
	var out []string
	for _, w := range strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }) {
		if len([]rune(w)) >= 3 {
			out = append(out, w)
		}
	}
	return out
}

// Audit guarda os registros de ferramenta.
type Audit struct {
	mu   sync.Mutex
	Runs []domain.ToolRun
	Err  error
}

// RecordToolRun implementa ports.AuditLog.
func (a *Audit) RecordToolRun(_ context.Context, run domain.ToolRun) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.Err != nil {
		return a.Err
	}
	a.Runs = append(a.Runs, run)
	return nil
}

// Confirmer responde sempre Answer e guarda as perguntas.
type Confirmer struct {
	mu        sync.Mutex
	Answer    bool
	Questions []ports.ConfirmRequest
}

// Confirm implementa ports.Confirmer.
func (c *Confirmer) Confirm(_ context.Context, req ports.ConfirmRequest) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.Questions = append(c.Questions, req)
	return c.Answer, nil
}

// Metrics guarda as observações.
type Metrics struct {
	mu  sync.Mutex
	Obs map[string][]float64
}

// Observe implementa ports.Metrics.
func (m *Metrics) Observe(name string, value float64, labels ...string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.Obs == nil {
		m.Obs = map[string][]float64{}
	}
	key := name
	if len(labels) > 0 {
		key += "{" + strings.Join(labels, ",") + "}"
	}
	m.Obs[key] = append(m.Obs[key], value)
}

// Sum soma as observações de uma chave.
func (m *Metrics) Sum(key string) float64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	var s float64
	for _, v := range m.Obs[key] {
		s += v
	}
	return s
}

// Tool é uma ferramenta configurável.
type Tool struct {
	SpecValue domain.ToolSpec
	Result    domain.ToolResult
	Err       error
	Delay     time.Duration
	Question  string
	mu        sync.Mutex
	Calls     []json.RawMessage
}

// Spec implementa ports.Tool.
func (t *Tool) Spec() domain.ToolSpec { return t.SpecValue }

// Run implementa ports.Tool.
func (t *Tool) Run(ctx context.Context, args json.RawMessage) (domain.ToolResult, error) {
	t.mu.Lock()
	t.Calls = append(t.Calls, args)
	t.mu.Unlock()
	if t.Delay > 0 {
		select {
		case <-time.After(t.Delay):
		case <-ctx.Done():
			return domain.ToolResult{}, ctx.Err()
		}
	}
	return t.Result, t.Err
}

// ConfirmQuestion implementa ports.Confirmable quando Question não é vazia.
func (t *Tool) ConfirmQuestion(json.RawMessage) string { return t.Question }

// RunCount devolve quantas vezes a ferramenta rodou.
func (t *Tool) RunCount() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.Calls)
}
