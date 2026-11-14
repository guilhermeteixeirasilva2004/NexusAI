// Package policy toma as decisões de segurança de IA, sem I/O: quais
// ferramentas podem rodar, quais pedem confirmação, se os argumentos batem com
// o schema e o que a saída do modelo não pode vazar.
package policy

import (
	"fmt"

	"nexusai/internal/core/domain"
)

// Policy decide sobre o uso de ferramentas.
type Policy struct {
	// Allow é a allowlist de ferramentas. Vazia = nenhuma ferramenta roda.
	Allow map[string]bool
}

// New cria uma política com a allowlist dada.
func New(allow ...string) Policy {
	p := Policy{Allow: make(map[string]bool, len(allow))}
	for _, name := range allow {
		p.Allow[name] = true
	}
	return p
}

// Verdict é a decisão sobre uma chamada de ferramenta.
type Verdict struct {
	Allowed           bool
	NeedsConfirmation bool
	Reason            string // por que foi negada; vai para o modelo e para a auditoria
}

// Check decide se a ferramenta pode rodar e se precisa de confirmação.
func (p Policy) Check(spec domain.ToolSpec) Verdict {
	if !p.Allow[spec.Name] {
		return Verdict{Reason: fmt.Sprintf("a ferramenta %q não está liberada", spec.Name)}
	}
	switch spec.Risk {
	case domain.RiskRead:
		return Verdict{Allowed: true}
	case domain.RiskWrite, domain.RiskDestructive:
		return Verdict{Allowed: true, NeedsConfirmation: true}
	default:
		// Nível desconhecido é tratado como o mais perigoso.
		return Verdict{Allowed: true, NeedsConfirmation: true}
	}
}
