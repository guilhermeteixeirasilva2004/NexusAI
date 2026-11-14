// Package ports declara as interfaces que o core exige do mundo externo.
// Importa só a stdlib e o próprio core.
package ports

import (
	"context"
	"encoding/json"

	"nexusai/internal/core/domain"
)

// ChatMessage é uma mensagem já no formato do motor (role do chat template).
type ChatMessage struct {
	Role    string // system, user, assistant
	Content string
}

// GenerateRequest é um pedido de geração.
type GenerateRequest struct {
	Messages    []ChatMessage
	MaxTokens   int
	Temperature float64
	Stop        []string
	// Grammar restringe a saída (GBNF). Só é enviada se o motor declarar
	// Capabilities().Grammar.
	Grammar string
}

// Completion é o resultado de uma geração.
type Completion struct {
	Text             string
	PromptTokens     int
	CompletionTokens int
	FinishReason     string // stop, length
}

// Capabilities descreve o que o motor sabe fazer.
type Capabilities struct {
	ContextSize int
	Grammar     bool // aceita saída restrita por gramática ou JSON Schema
	NativeTools bool // o template do modelo tem formato próprio de tools
}

// ChatModel gera texto. onToken recebe cada pedaço assim que chega; pode ser nil.
type ChatModel interface {
	Generate(ctx context.Context, req GenerateRequest, onToken func(string)) (Completion, error)
	CountTokens(ctx context.Context, text string) (int, error)
	Capabilities() Capabilities
}

// Embedder transforma textos em vetores (Fase 2).
type Embedder interface {
	Embed(ctx context.Context, texts []string) ([][]float32, error)
}

// SessionStore guarda as conversas.
type SessionStore interface {
	Create(ctx context.Context) (domain.Session, error)
	Latest(ctx context.Context) (domain.Session, error) // domain.ErrNotFound se não há nenhuma
	// Append grava as mensagens de uma vez, numa transação.
	Append(ctx context.Context, sessionID string, msgs ...domain.Message) error
	// History devolve as últimas limit mensagens, da mais antiga para a mais nova.
	History(ctx context.Context, sessionID string, limit int) ([]domain.Message, error)
}

// MemoryStore guarda a memória longa.
type MemoryStore interface {
	Save(ctx context.Context, item domain.MemoryItem) (domain.MemoryItem, error)
	Search(ctx context.Context, q domain.MemoryQuery) ([]domain.MemoryItem, error)
}

// AuditLog grava toda execução de ferramenta, permitida ou não.
type AuditLog interface {
	RecordToolRun(ctx context.Context, run domain.ToolRun) error
}

// Tool é uma ferramenta que o modelo pode pedir.
type Tool interface {
	Spec() domain.ToolSpec // nome, descrição, JSON Schema dos argumentos, nível de risco
	Run(ctx context.Context, args json.RawMessage) (domain.ToolResult, error)
}

// ConfirmRequest é o que se pergunta à pessoa antes de uma ação de escrita.
type ConfirmRequest struct {
	Tool     string
	Risk     domain.Risk
	Question string // em português simples: "Posso guardar na memória: ...?"
}

// Confirmer pergunta à pessoa. Sem resposta (ctx cancelado) vale como não.
type Confirmer interface {
	Confirm(ctx context.Context, req ConfirmRequest) (bool, error)
}

// Metrics registra observações. Contadores usam Observe com valor 1.
type Metrics interface {
	Observe(name string, value float64, labels ...string)
}

// Confirmable é implementada por ferramentas que sabem escrever a própria
// pergunta de confirmação a partir dos argumentos.
type Confirmable interface {
	ConfirmQuestion(args json.RawMessage) string
}
