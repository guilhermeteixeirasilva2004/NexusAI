// Package domain guarda os tipos do core: mensagens, sessões, papéis e erros.
// Importa só a stdlib.
package domain

// Role é o papel de um modelo. O core fala em papéis, nunca em nome de modelo.
type Role string

const (
	RoleMain  Role = "main"  // raciocínio ReAct e resposta final
	RoleAux   Role = "aux"   // tarefas baratas: resumo, extração, classificação
	RoleEmbed Role = "embed" // vetores para a memória
)

// Roles lista todos os papéis conhecidos, na ordem canônica.
func Roles() []Role { return []Role{RoleMain, RoleAux, RoleEmbed} }

// Valid diz se r é um papel conhecido.
func (r Role) Valid() bool {
	switch r {
	case RoleMain, RoleAux, RoleEmbed:
		return true
	}
	return false
}
