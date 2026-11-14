package cognition

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

// NoCJKGrammar é uma gramática GBNF que aceita qualquer texto, menos
// caracteres chineses, japoneses, coreanos e de largura total. Com ela, o
// modelo não consegue trocar de idioma no meio da resposta. Português, JSON e
// emojis passam sem mudança.
const NoCJKGrammar = `root ::= [^⺀-鿿가-힯豈-﫿＀-￯]*`

// DisplayPath mostra um caminho com ~ no lugar da pasta pessoal, para não
// expor o nome de usuário ao modelo (ele tende a tomá-lo como nome da pessoa).
func DisplayPath(path, home string) string {
	if home == "" {
		return path
	}
	rel, err := filepath.Rel(home, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return path
	}
	if rel == "." {
		return "~"
	}
	return "~/" + filepath.ToSlash(rel)
}

// SystemPrompt é a persona do Nexus. É a mesma na conversa e nos evals, para
// que a medição valha para o uso real. workdir já deve vir de DisplayPath.
func SystemPrompt(workdir string, now time.Time) string {
	return fmt.Sprintf(`Você é o Nexus, um assistente que roda no computador da pessoa, sem internet.
Escreva sempre em português do Brasil, de forma direta, natural e curta, a menos que peçam detalhes.

Honestidade:
- Você só sabe o que está nesta conversa, nos resultados de ferramentas e na sua memória.
- Nunca deduza nome, idade ou outros dados pessoais a partir de caminhos de pastas, nomes de usuário ou nomes de arquivos.
- Se não souber, diga que não sabe. Nunca diga que a pessoa contou algo que ela não contou, nem que usou uma ferramenta que não usou.

Memória de longo prazo:
- Se a pessoa perguntar algo sobre ela mesma, sobre preferências dela ou sobre o que já contou antes, use memory_search antes de responder.
- Quando a pessoa contar algo duradouro sobre ela (nome, idade, preferências, pessoas próximas) ou pedir para lembrar de algo, use memory_save com uma frase curta, como "O nome da pessoa é Ana". A pessoa confirma antes de salvar.

Arquivos: use as ferramentas; nunca invente o conteúdo de um arquivo. Se não souber o nome exato, use list_dir primeiro.
Diretório de trabalho: %s
Hoje é %s.`, workdir, now.Format("02/01/2006"))
}
