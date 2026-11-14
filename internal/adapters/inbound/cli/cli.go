// Package cli é a conversa no terminal: o jeito do dia a dia de falar com o
// Nexus. Também implementa ports.Confirmer, perguntando "(s/n)" na mesma tela.
package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"

	"nexusai/internal/core/domain"
	"nexusai/internal/core/orchestrator"
	"nexusai/internal/core/ports"
)

// Asker é o que a conversa precisa do core.
type Asker interface {
	Ask(ctx context.Context, sessionID, text string, sink func(orchestrator.Event)) (orchestrator.Reply, error)
}

// Chat é a conversa no terminal.
type Chat struct {
	Asker    Asker
	Sessions ports.SessionStore
	In       *bufio.Reader
	Out      io.Writer
	Color    bool   // cores e a linha "pensando…" (só em terminal)
	LogPath  string // citado nas mensagens de erro

	mu      sync.Mutex // protege a escrita na tela
	once    sync.Once
	lines   chan lineMsg // um só leitor da entrada, para conversa e confirmações
	busy    atomic.Bool
	cancel  atomic.Pointer[context.CancelFunc]
	session string
	render  *renderer
}

type lineMsg struct {
	line string
	err  error
}

// readLine lê a próxima linha, ou desiste quando ctx acaba. A leitura em si
// continua num goroutine só, então nenhuma linha digitada se perde.
func (c *Chat) readLine(ctx context.Context) (string, error) {
	c.once.Do(func() {
		c.lines = make(chan lineMsg)
		go func() {
			for {
				line, err := c.In.ReadString('\n')
				c.lines <- lineMsg{line, err}
				if err != nil {
					close(c.lines)
					return
				}
			}
		}()
	})
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case m, ok := <-c.lines:
		if !ok {
			return "", io.EOF
		}
		if m.err != nil && m.line != "" {
			return m.line, nil
		}
		return m.line, m.err
	}
}

// Interrupt trata um Ctrl+C: com uma resposta em andamento, para só ela e
// devolve true; sem nada em andamento, devolve false (quem chamou encerra).
func (c *Chat) Interrupt() bool {
	if !c.busy.Load() {
		return false
	}
	if f := c.cancel.Load(); f != nil {
		(*f)()
	}
	return true
}

// Run abre a conversa e lê mensagens até /sair, sair ou fim da entrada.
func (c *Chat) Run(ctx context.Context) error {
	resumed, err := c.openSession(ctx)
	if err != nil {
		return err
	}
	c.say(c.style(bold, "Nexus") + " · pode falar. " + c.style(dim, "(/ajuda mostra os comandos)"))
	if resumed > 0 {
		c.say(c.style(dim, fmt.Sprintf("Continuando a conversa anterior (%d mensagens). /nova começa outra.", resumed)))
	}
	for {
		c.write("\n" + c.style(bold, "você › "))
		line, err := c.readLine(ctx)
		if err != nil {
			if errors.Is(err, io.EOF) || ctx.Err() != nil {
				c.say("\nAté logo!")
				return nil
			}
			return err
		}
		text := strings.TrimSpace(line)
		switch strings.ToLower(text) {
		case "":
			continue
		case "/sair", "sair":
			c.say("Até logo!")
			return nil
		case "/ajuda", "/help", "ajuda":
			c.help()
			continue
		case "/nova":
			sess, err := c.Sessions.Create(ctx)
			if err != nil {
				c.say(c.friendly(err))
				continue
			}
			c.session = sess.ID
			c.say("Conversa nova. Pode falar.")
			continue
		}
		if strings.HasPrefix(text, "/") {
			c.say(fmt.Sprintf("Não conheço o comando %s. Digite /ajuda.", text))
			continue
		}
		c.ask(ctx, text)
		if ctx.Err() != nil {
			return nil
		}
	}
}

// Once responde uma pergunta só, na conversa mais recente.
func (c *Chat) Once(ctx context.Context, question string) error {
	if _, err := c.openSession(ctx); err != nil {
		return err
	}
	if !c.ask(ctx, question) {
		return errors.New("a pergunta não foi respondida")
	}
	return nil
}

func (c *Chat) openSession(ctx context.Context) (int, error) {
	sess, err := c.Sessions.Latest(ctx)
	if errors.Is(err, domain.ErrNotFound) {
		sess, err = c.Sessions.Create(ctx)
		if err != nil {
			return 0, err
		}
		c.session = sess.ID
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	c.session = sess.ID
	hist, err := c.Sessions.History(ctx, sess.ID, 0)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, m := range hist {
		if m.Origin == domain.OriginUser || (m.Origin == domain.OriginAssistant && m.ToolName == "") {
			n++
		}
	}
	return n, nil
}

// ask envia uma mensagem e mostra a resposta. Devolve se deu certo.
func (c *Chat) ask(ctx context.Context, text string) bool {
	rctx, cancel := context.WithCancel(ctx)
	defer cancel()
	c.cancel.Store(&cancel)
	c.busy.Store(true)
	defer c.busy.Store(false)

	c.render = &renderer{chat: c}
	c.render.thinking()
	reply, err := c.Asker.Ask(rctx, c.session, text, c.render.event)
	c.render.endLine()
	switch {
	case err == nil:
		return true
	case errors.Is(err, domain.ErrStepLimit):
		c.say(reply.Text)
	default:
		c.say(c.friendly(err))
	}
	return false
}

func (c *Chat) friendly(err error) string {
	switch {
	case errors.Is(err, context.Canceled):
		return c.style(dim, "(interrompido)")
	case errors.Is(err, context.DeadlineExceeded):
		return "Demorou demais e eu parei. Tente de novo ou peça algo menor."
	case errors.Is(err, domain.ErrModelDown):
		return "Não consegui falar com o modelo. Rode `nexus status` para ver o que falta."
	case errors.Is(err, domain.ErrContextBudget):
		return "Isso não cabe na memória de trabalho do modelo. Tente um trecho menor ou comece com /nova."
	}
	msg := "Algo deu errado: " + err.Error() + "."
	if c.LogPath != "" && c.LogPath != "stderr" {
		msg += " Detalhes em " + c.LogPath
	}
	return msg
}

func (c *Chat) help() {
	c.say(`Fale normalmente: pergunte, peça para ler um arquivo daqui, ou peça para eu lembrar de algo.
  /nova    começa uma conversa nova
  /ajuda   mostra isto
  /sair    encerra (sair, Ctrl+D e Ctrl+C também)
Ctrl+C durante uma resposta só interrompe a resposta.`)
}

// Confirm implementa ports.Confirmer: pergunta na tela e lê s/n.
func (c *Chat) Confirm(ctx context.Context, req ports.ConfirmRequest) (bool, error) {
	if c.render != nil {
		c.render.clearThinking()
		c.render.endLine()
	}
	c.write(c.style(yellow, "  ? ") + req.Question + " (s/n) ")
	line, err := c.readLine(ctx)
	if err != nil {
		c.say("")
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		return false, nil
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "s", "sim", "y", "yes", "pode":
		if c.render != nil {
			c.render.thinking()
		}
		return true, nil
	}
	return false, nil
}

func (c *Chat) write(s string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, _ = io.WriteString(c.Out, s)
}

func (c *Chat) say(s string) { c.write(s + "\n") }

const (
	bold   = "\033[1m"
	dim    = "\033[2m"
	yellow = "\033[33m"
	reset  = "\033[0m"
)

func (c *Chat) style(code, s string) string {
	if !c.Color {
		return s
	}
	return code + s + reset
}

// renderer desenha os eventos de uma resposta.
type renderer struct {
	chat        *Chat
	showingWait bool // a linha "pensando…" está na tela
	answering   bool // o prefixo "nexus › " já saiu
	midLine     bool // o cursor não está no começo da linha
}

func (r *renderer) thinking() {
	if !r.chat.Color || r.showingWait || r.answering {
		return
	}
	r.chat.write(r.chat.style(dim, "pensando…"))
	r.showingWait = true
}

func (r *renderer) clearThinking() {
	if r.showingWait {
		r.chat.write("\r\033[K")
		r.showingWait = false
	}
}

func (r *renderer) endLine() {
	r.clearThinking()
	if r.midLine {
		r.chat.write("\n")
		r.midLine = false
	}
}

func (r *renderer) event(ev orchestrator.Event) {
	switch ev.Kind {
	case orchestrator.EventToolCall:
		r.clearThinking()
		if r.midLine {
			r.chat.write("\n")
			r.midLine = false
		}
		r.chat.say(r.chat.style(dim, "  · "+ev.Summary+"…"))
		r.answering = false
		r.thinking()
	case orchestrator.EventToolResult:
		if ev.Denied && ev.Text == "denied_user" {
			r.clearThinking()
			r.chat.say(r.chat.style(dim, "  · ok, não fiz."))
			r.thinking()
		}
	case orchestrator.EventToken:
		r.clearThinking()
		if !r.answering {
			r.chat.write(r.chat.style(bold, "nexus › "))
			r.answering = true
		}
		r.chat.write(ev.Text)
		r.midLine = !strings.HasSuffix(ev.Text, "\n")
	}
}
