// Package cognition monta o prompt, gerencia a janela de contexto e define o
// ToolCallCodec, que traduz entre ferramentas e o texto do modelo.
package cognition

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"nexusai/internal/core/domain"
)

// Decision é o que o modelo decidiu num passo: responder ou chamar ferramenta.
type Decision struct {
	Final string           // resposta final, quando Call é nil
	Call  *domain.ToolCall // pedido de ferramenta
	// Prose é o texto que o modelo escreveu antes da chamada ("Vou ler o
	// arquivo."). Já pode ter ido para a tela; não entra no histórico.
	Prose string
}

// StreamClass diz o que um começo de saída parece ser.
type StreamClass int

const (
	Undecided StreamClass = iota // ainda não dá para saber
	Answer                       // texto para a pessoa: pode ir para a tela
	ToolCall                     // pode ser pedido de ferramenta: segura até o fim
)

// ToolCallCodec traduz entre ferramentas e o texto do modelo. Há uma
// implementação por estilo de modelo, escolhida na config.
type ToolCallCodec interface {
	// Instructions descreve as ferramentas para o prompt de sistema.
	Instructions(tools []domain.ToolSpec) string
	// Parse interpreta a saída completa do modelo.
	Parse(output string) (Decision, error)
	// Classify olha o começo de uma linha da saída, para decidir se ela já
	// pode ir para a tela.
	Classify(prefix string) StreamClass
	// FormatCall escreve um pedido de ferramenta como mensagem do assistente.
	FormatCall(call domain.ToolCall) string
}

// ErrNoToolCall indica uma saída que parecia chamada de ferramenta mas não é.
var ErrNoToolCall = errors.New("chamada de ferramenta malformada")

// JSONCodec funciona com qualquer modelo: descreve as ferramentas no prompt e
// pede um objeto JSON de formato fixo para chamar uma ferramenta. Na leitura,
// aceita também os jeitos como modelos costumam escrever chamadas: o formato
// nativo {"name", "arguments"}, a tag <tool_call>, cercas de código,
// nome({...}) e uma frase curta antes da chamada.
type JSONCodec struct{}

type wireCall struct {
	Tool      string          `json:"tool"`
	Args      json.RawMessage `json:"args"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

// Instructions implementa ToolCallCodec.
func (JSONCodec) Instructions(tools []domain.ToolSpec) string {
	if len(tools) == 0 {
		return ""
	}
	sorted := append([]domain.ToolSpec(nil), tools...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })

	var b strings.Builder
	b.WriteString("# Ferramentas\n\n")
	b.WriteString("Para usar uma ferramenta, responda APENAS com um objeto JSON, sem texto antes ou depois:\n")
	b.WriteString(`{"tool": "<nome>", "args": {<argumentos>}}` + "\n\n")
	b.WriteString("Use uma ferramenta por vez. O resultado volta numa mensagem marcada como dado. ")
	b.WriteString("Quando tiver o que precisa, responda à pessoa em texto normal, sem JSON.\n")
	b.WriteString("Se não precisar de ferramenta, responda direto.\n\n")
	b.WriteString("Ferramentas disponíveis:\n")
	for _, t := range sorted {
		fmt.Fprintf(&b, "- %s: %s Argumentos (JSON Schema): %s\n", t.Name, t.Description, compactJSON(t.Params))
	}
	return b.String()
}

func compactJSON(raw json.RawMessage) string {
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err != nil {
		return string(raw)
	}
	return buf.String()
}

var (
	identPrefix = regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)
	funcStart   = regexp.MustCompile(`^([a-z_][a-z0-9_]*)\s*\(`)
)

// Classify implementa ToolCallCodec. Uma chamada começa com "{", com uma
// cerca de código, com a tag <tool_call> ou com nome_de_ferramenta(.
func (JSONCodec) Classify(prefix string) StreamClass {
	p := strings.TrimLeft(prefix, " \t\r\n")
	if p == "" {
		return Undecided
	}
	for _, start := range []string{"{", "```", "<tool_call>"} {
		if strings.HasPrefix(p, start) {
			return ToolCall
		}
		if strings.HasPrefix(start, p) {
			return Undecided // ainda pode virar um desses começos
		}
	}
	if funcStart.MatchString(p) {
		return ToolCall
	}
	if identPrefix.MatchString(strings.TrimRight(p, " \t")) && !strings.ContainsAny(p, " \t") {
		return Undecided // "memory_save" pode virar "memory_save("
	}
	return Answer
}

// Parse implementa ToolCallCodec.
//
// Uma saída que começa como chamada e traz um objeto com "tool" (ou "name" e
// "arguments") é chamada. Se começa como chamada mas não traz o objeto, é
// resposta (um bloco de código, um JSON de exemplo), a menos que mencione
// "tool": aí é chamada malformada, e o modelo tenta de novo.
//
// Prosa antes da chamada só vale se a chamada é a última coisa da saída e
// está bem formada; senão, tudo é resposta final.
func (c JSONCodec) Parse(output string) (Decision, error) {
	text := strings.TrimSpace(output)
	if c.Classify(text) == ToolCall {
		call, err := parseCall(text)
		switch {
		case err == nil:
			return Decision{Call: call}, nil
		case errors.Is(err, ErrNoToolCall) && looksLikeCall(text):
			return Decision{}, err
		}
		return Decision{Final: text}, nil
	}
	// Procura uma chamada no começo de alguma linha, ocupando o resto da saída.
	for i := 0; i < len(text); i++ {
		if text[i] != '\n' {
			continue
		}
		rest := strings.TrimSpace(text[i+1:])
		if c.Classify(rest) != ToolCall {
			continue
		}
		if call, err := parseCall(rest); err == nil {
			return Decision{Call: call, Prose: strings.TrimSpace(text[:i])}, nil
		}
	}
	return Decision{Final: text}, nil
}

func looksLikeCall(s string) bool {
	return strings.Contains(s, `"tool"`) || strings.Contains(s, `"arguments"`)
}

// parseCall lê uma chamada que ocupa s inteiro (aceitando lixo de formatação
// em volta: tags, cercas). Devolve ErrNoToolCall quando não acha uma.
func parseCall(s string) (*domain.ToolCall, error) {
	if m := funcStart.FindStringSubmatch(s); m != nil {
		return parseFuncCall(m[1], s[len(m[0]):])
	}
	body := strings.TrimPrefix(s, "<tool_call>")
	body = strings.TrimSpace(body)
	if strings.HasPrefix(body, "```") {
		body = strings.TrimPrefix(body, "```")
		if nl := strings.IndexByte(body, '\n'); nl >= 0 && !strings.Contains(body[:nl], "{") {
			body = body[nl+1:] // pula "json" depois da cerca
		}
	}
	start := strings.IndexByte(body, '{')
	if start < 0 || strings.TrimSpace(body[:start]) != "" {
		return nil, fmt.Errorf("%w: não achei um objeto JSON", ErrNoToolCall)
	}
	obj, end, ok := firstObject(body)
	if !ok {
		return nil, fmt.Errorf("%w: objeto JSON incompleto", ErrNoToolCall)
	}
	if tail := strings.TrimSpace(body[end:]); !onlyClosers(tail) {
		return nil, fmt.Errorf("%w: texto depois da chamada", ErrNoToolCall)
	}
	var w wireCall
	if err := json.Unmarshal([]byte(obj), &w); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrNoToolCall, err)
	}
	name, args := w.Tool, w.Args
	if name == "" && len(w.Arguments) > 0 {
		name, args = w.Name, w.Arguments // formato nativo do Qwen e afins
	}
	if name == "" {
		return nil, fmt.Errorf(`%w: falta o campo "tool"`, ErrNoToolCall)
	}
	return &domain.ToolCall{Name: name, Args: normalizeArgs(args)}, nil
}

// onlyClosers aceita o que costuma fechar uma chamada: cercas e tags.
func onlyClosers(s string) bool {
	for _, closer := range []string{"```", "</tool_call>"} {
		s = strings.ReplaceAll(s, closer, "")
	}
	return strings.TrimSpace(s) == ""
}

// parseFuncCall lê o resto de "nome(" como "{...})" ou ")".
func parseFuncCall(name, rest string) (*domain.ToolCall, error) {
	rest = strings.TrimSpace(rest)
	if strings.HasPrefix(rest, ")") && strings.TrimSpace(rest[1:]) == "" {
		return &domain.ToolCall{Name: name, Args: json.RawMessage(`{}`)}, nil
	}
	if !strings.HasPrefix(rest, "{") {
		return nil, fmt.Errorf("%w: %s( sem objeto JSON", ErrNoToolCall, name)
	}
	obj, end, ok := firstObject(rest)
	if !ok {
		return nil, fmt.Errorf("%w: %s(...) incompleto", ErrNoToolCall, name)
	}
	if tail := strings.TrimSpace(rest[end:]); !strings.HasPrefix(tail, ")") || !onlyClosers(tail[1:]) {
		return nil, fmt.Errorf("%w: %s(...) malformado", ErrNoToolCall, name)
	}
	if !json.Valid([]byte(obj)) {
		return nil, fmt.Errorf("%w: argumentos de %s não são JSON", ErrNoToolCall, name)
	}
	return &domain.ToolCall{Name: name, Args: json.RawMessage(obj)}, nil
}

func normalizeArgs(args json.RawMessage) json.RawMessage {
	if len(args) == 0 || string(args) == "null" {
		return json.RawMessage(`{}`)
	}
	// Alguns modelos mandam os argumentos como texto JSON.
	var s string
	if json.Unmarshal(args, &s) == nil && json.Valid([]byte(s)) {
		return json.RawMessage(s)
	}
	return args
}

// FormatCall implementa ToolCallCodec.
func (JSONCodec) FormatCall(call domain.ToolCall) string {
	b, _ := json.Marshal(struct {
		Tool string          `json:"tool"`
		Args json.RawMessage `json:"args"`
	}{call.Name, call.Args})
	return string(b)
}

// firstObject devolve o primeiro objeto JSON balanceado de s, respeitando
// strings, e a posição logo depois dele.
func firstObject(s string) (string, int, bool) {
	start := strings.IndexByte(s, '{')
	if start < 0 {
		return "", 0, false
	}
	depth, inStr, esc := 0, false, false
	for i := start; i < len(s); i++ {
		ch := s[i]
		switch {
		case esc:
			esc = false
		case inStr && ch == '\\':
			esc = true
		case ch == '"':
			inStr = !inStr
		case inStr:
		case ch == '{':
			depth++
		case ch == '}':
			depth--
			if depth == 0 {
				return s[start : i+1], i + 1, true
			}
		}
	}
	return "", 0, false
}

// StreamGate decide, linha a linha, o que da saída pode ir para a tela. Uma
// linha que começa como chamada de ferramenta fica retida, com tudo depois
// dela, até o fim da geração.
type StreamGate struct {
	codec   ToolCallCodec
	emit    func(string)
	pending strings.Builder // texto ainda não liberado
	state   gateState
	sent    bool
}

type gateState int

const (
	lineStart gateState = iota // esperando saber o que a linha é
	streaming                  // linha de resposta: vai direto para a tela
	holding                    // pode ser chamada: segura tudo até o fim
)

// NewStreamGate cria um portão para uma geração.
func NewStreamGate(codec ToolCallCodec, emit func(string)) *StreamGate {
	return &StreamGate{codec: codec, emit: emit}
}

// OnToken recebe um pedaço gerado.
func (g *StreamGate) OnToken(tok string) {
	g.pending.WriteString(tok)
	for {
		buf := g.pending.String()
		switch g.state {
		case holding:
			return
		case lineStart:
			switch g.codec.Classify(buf) {
			case Undecided:
				return
			case ToolCall:
				g.state = holding
				return
			}
			g.state = streaming
		case streaming:
			nl := strings.IndexByte(buf, '\n')
			if nl < 0 {
				g.release(buf)
				g.pending.Reset()
				return
			}
			g.release(buf[:nl+1])
			g.pending.Reset()
			g.pending.WriteString(buf[nl+1:])
			g.state = lineStart
		}
	}
}

// Finish libera o que ficou retido: chame quando a saída for resposta final.
func (g *StreamGate) Finish() {
	rest := g.pending.String()
	g.pending.Reset()
	if !g.sent {
		rest = strings.TrimLeft(rest, " \t\r\n")
	}
	rest = strings.TrimRight(rest, " \t\r\n")
	if rest != "" {
		g.emit(rest)
		g.sent = true
	}
}

// Streamed diz se algo já foi para a tela.
func (g *StreamGate) Streamed() bool { return g.sent }

func (g *StreamGate) release(s string) {
	if !g.sent {
		s = strings.TrimLeft(s, " \t\r\n") // pula o espaço inicial do modelo
	}
	if s != "" {
		g.emit(s)
		g.sent = true
	}
}
