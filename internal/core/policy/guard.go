package policy

import (
	"regexp"
	"strings"
	"unicode"
)

// Redacted substitui um segredo encontrado na saída.
const Redacted = "[segredo removido]"

// secretPatterns casam palavras inteiras (sem espaço) que parecem chave ou token.
var secretPatterns = []struct {
	kind string
	re   *regexp.Regexp
}{
	{"openai_key", regexp.MustCompile(`^sk-[A-Za-z0-9_-]{20,}$`)},
	{"anthropic_key", regexp.MustCompile(`^sk-ant-[A-Za-z0-9_-]{20,}$`)},
	{"aws_access_key", regexp.MustCompile(`^(AKIA|ASIA)[0-9A-Z]{16}$`)},
	{"github_token", regexp.MustCompile(`^(ghp|gho|ghu|ghs|ghr)_[A-Za-z0-9]{30,}$|^github_pat_[A-Za-z0-9_]{40,}$`)},
	{"slack_token", regexp.MustCompile(`^xox[abprs]-[A-Za-z0-9-]{10,}$`)},
	{"google_api_key", regexp.MustCompile(`^AIza[0-9A-Za-z_-]{35}$`)},
	{"jwt", regexp.MustCompile(`^eyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}$`)},
	{"private_key_body", regexp.MustCompile(`^[A-Za-z0-9+/]{60,}={0,2}$`)},
}

// pemHeader marca o começo de uma chave privada em PEM.
var pemHeader = regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----`)

// classify diz que tipo de segredo a palavra é, ou "".
func classify(word string) string {
	core := strings.TrimFunc(word, func(r rune) bool {
		return unicode.IsPunct(r) && r != '-' && r != '_' && r != '+' && r != '/' && r != '='
	})
	for _, p := range secretPatterns {
		if !p.re.MatchString(core) {
			continue
		}
		// Corpo de chave em base64 tem maiúscula, minúscula e dígito; um hash
		// hexadecimal não, e não deve ser apagado.
		if p.kind == "private_key_body" && !mixedClasses(core) {
			continue
		}
		return p.kind
	}
	return ""
}

func mixedClasses(s string) bool {
	var up, low, dig bool
	for _, r := range s {
		switch {
		case unicode.IsUpper(r):
			up = true
		case unicode.IsLower(r):
			low = true
		case unicode.IsDigit(r):
			dig = true
		}
	}
	return up && low && dig
}

// Redact remove segredos de um texto completo. Devolve o texto limpo e os
// tipos encontrados, um por ocorrência.
func Redact(text string) (string, []string) {
	var kinds []string
	if pemHeader.MatchString(text) {
		text = pemHeader.ReplaceAllString(text, Redacted)
		kinds = append(kinds, "private_key")
	}
	var b strings.Builder
	start := -1
	flush := func(end int) {
		word := text[start:end]
		if kind := classify(word); kind != "" {
			b.WriteString(Redacted)
			kinds = append(kinds, kind)
		} else {
			b.WriteString(word)
		}
		start = -1
	}
	for i, r := range text {
		if unicode.IsSpace(r) {
			if start >= 0 {
				flush(i)
			}
			b.WriteRune(r)
			continue
		}
		if start < 0 {
			start = i
		}
	}
	if start >= 0 {
		flush(len(text))
	}
	return b.String(), kinds
}

// StreamGuard aplica Redact a um texto que chega em pedaços. Ele só libera
// palavras completas, então um segredo dividido entre dois tokens também é pego.
type StreamGuard struct {
	emit    func(string)
	onBlock func(kind string)
	pending strings.Builder
}

// NewStreamGuard cria um guarda que entrega o texto limpo a emit.
func NewStreamGuard(emit func(string), onBlock func(kind string)) *StreamGuard {
	return &StreamGuard{emit: emit, onBlock: onBlock}
}

// Write recebe um pedaço do texto.
func (g *StreamGuard) Write(chunk string) {
	g.pending.WriteString(chunk)
	buf := g.pending.String()
	cut := strings.LastIndexFunc(buf, unicode.IsSpace)
	if cut < 0 {
		return
	}
	// inclui o espaço; o pedaço depois dele ainda pode crescer
	_, size := firstRuneAt(buf, cut)
	g.release(buf[:cut+size])
	rest := buf[cut+size:]
	g.pending.Reset()
	g.pending.WriteString(rest)
}

// Flush libera o que sobrou no fim da geração.
func (g *StreamGuard) Flush() {
	if g.pending.Len() > 0 {
		g.release(g.pending.String())
		g.pending.Reset()
	}
}

func (g *StreamGuard) release(s string) {
	clean, kinds := Redact(s)
	for _, k := range kinds {
		if g.onBlock != nil {
			g.onBlock(k)
		}
	}
	if clean != "" {
		g.emit(clean)
	}
}

func firstRuneAt(s string, i int) (rune, int) {
	for _, r := range s[i:] {
		return r, len(string(r))
	}
	return 0, 0
}
