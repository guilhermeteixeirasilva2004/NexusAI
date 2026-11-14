package domain

import (
	"encoding/json"
	"errors"
	"time"
)

// Origin diz de onde veio um trecho do prompt. Só OriginSystem e OriginUser
// são instrução; o resto entra delimitado como dado não confiável.
type Origin string

const (
	OriginSystem    Origin = "system"
	OriginUser      Origin = "user"
	OriginAssistant Origin = "assistant"
	OriginTool      Origin = "tool"
	OriginMemory    Origin = "memory"
)

// Trusted diz se o conteúdo dessa origem pode ser tratado como instrução.
func (o Origin) Trusted() bool { return o == OriginSystem || o == OriginUser }

// Message é uma mensagem da conversa, como fica gravada na sessão.
type Message struct {
	ID        int64
	SessionID string
	Origin    Origin
	Content   string
	ToolName  string // para Origin == OriginTool e chamadas do assistente
	CreatedAt time.Time
}

// Session agrupa as mensagens de uma conversa.
type Session struct {
	ID        string
	Title     string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Risk é o nível de risco de uma ferramenta.
type Risk string

const (
	RiskRead        Risk = "read"        // só lê
	RiskWrite       Risk = "write"       // muda algo; pede confirmação
	RiskDestructive Risk = "destructive" // apaga ou é irreversível; pede confirmação
)

// NeedsConfirmation diz se o nível exige confirmação humana.
func (r Risk) NeedsConfirmation() bool { return r != RiskRead }

// ToolSpec descreve uma ferramenta para o modelo e para a política.
type ToolSpec struct {
	Name        string
	Description string
	Params      json.RawMessage // JSON Schema dos argumentos
	Risk        Risk
}

// ToolCall é um pedido de ferramenta feito pelo modelo.
type ToolCall struct {
	Name string
	Args json.RawMessage
}

// ToolResult é o que uma ferramenta devolve. Sempre dado, nunca instrução.
type ToolResult struct {
	Content string
	// Summary é a linha curta mostrada na tela ("lendo notas.md").
	Summary string
}

// Trust é o grau de confiança numa memória.
type Trust string

const (
	TrustHigh Trust = "high" // veio do usuário
	TrustLow  Trust = "low"  // veio de ferramenta, web ou do modelo
)

// MemoryItem é um fato ou episódio da memória longa.
type MemoryItem struct {
	ID        int64
	Text      string
	Kind      string // fact, episode
	Source    string // user, tool, model
	Trust     Trust
	CreatedAt time.Time
}

// MemoryQuery é uma busca na memória longa.
type MemoryQuery struct {
	Text  string
	Limit int
}

// ToolRun é o registro de auditoria de uma execução de ferramenta.
type ToolRun struct {
	SessionID string
	TraceID   string
	Tool      string
	Args      string
	Risk      Risk
	Decision  string // allowed, denied_policy, denied_user, invalid_args
	Result    string
	Error     string
	Duration  time.Duration
	CreatedAt time.Time
}

// Erros do domínio.
var (
	ErrNotFound      = errors.New("não encontrado")
	ErrStepLimit     = errors.New("limite de passos atingido")
	ErrModelDown     = errors.New("modelo fora do ar")
	ErrContextBudget = errors.New("a mensagem não cabe no contexto do modelo")
)
