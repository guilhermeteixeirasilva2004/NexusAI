// Package llamacpp é o cliente HTTP do llama-server e o supervisor que sobe o
// processo quando ele não está no ar. Implementa ports.ChatModel.
package llamacpp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"syscall"
	"time"

	"nexusai/internal/core/domain"
	"nexusai/internal/core/ports"
)

// Client fala com um llama-server. Uma geração por vez: as outras esperam na fila.
type Client struct {
	BaseURL string
	Role    domain.Role
	HTTP    *http.Client
	Metrics ports.Metrics // opcional

	slot    chan struct{}
	once    sync.Once
	capsMu  sync.Mutex
	caps    ports.Capabilities
	capsSet bool
}

// New cria um cliente para baseURL (ex.: http://127.0.0.1:8081).
func New(baseURL string, role domain.Role) *Client {
	return &Client{
		BaseURL: strings.TrimRight(baseURL, "/"),
		Role:    role,
		// Sem timeout global: a geração é longa e o contexto decide quando parar.
		HTTP: &http.Client{Transport: &http.Transport{
			DialContext:           (&net.Dialer{Timeout: 3 * time.Second}).DialContext,
			ResponseHeaderTimeout: 10 * time.Minute,
			MaxIdleConns:          4,
		}},
	}
}

func (c *Client) init() {
	c.once.Do(func() { c.slot = make(chan struct{}, 1) })
}

type chatRequest struct {
	Messages      []chatMessage  `json:"messages"`
	Stream        bool           `json:"stream"`
	MaxTokens     int            `json:"max_tokens,omitempty"`
	Temperature   float64        `json:"temperature"`
	Stop          []string       `json:"stop,omitempty"`
	Grammar       string         `json:"grammar,omitempty"`
	CachePrompt   bool           `json:"cache_prompt"`
	StreamOptions map[string]any `json:"stream_options,omitempty"`
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chunk struct {
	Choices []struct {
		FinishReason *string `json:"finish_reason"`
		Delta        struct {
			Content *string `json:"content"`
		} `json:"delta"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
	Error *apiError `json:"error"`
}

type apiError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// Generate implementa ports.ChatModel com streaming SSE.
func (c *Client) Generate(ctx context.Context, req ports.GenerateRequest, onToken func(string)) (ports.Completion, error) {
	c.init()
	waitStart := time.Now()
	select {
	case c.slot <- struct{}{}:
	case <-ctx.Done():
		return ports.Completion{}, ctx.Err()
	}
	defer func() { <-c.slot }()
	c.observe("nexus_inference_queue_wait_seconds", time.Since(waitStart).Seconds())

	body := chatRequest{
		Stream:        true,
		MaxTokens:     req.MaxTokens,
		Temperature:   req.Temperature,
		Stop:          req.Stop,
		Grammar:       req.Grammar,
		CachePrompt:   true,
		StreamOptions: map[string]any{"include_usage": true},
	}
	for _, m := range req.Messages {
		body.Messages = append(body.Messages, chatMessage{Role: m.Role, Content: m.Content})
	}
	resp, err := c.post(ctx, "/v1/chat/completions", body)
	if err != nil {
		return ports.Completion{}, err
	}
	defer resp.Body.Close()

	var out ports.Completion
	var text strings.Builder
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := sc.Text()
		data, ok := strings.CutPrefix(line, "data: ")
		if !ok {
			continue
		}
		if data == "[DONE]" {
			break
		}
		var ch chunk
		if err := json.Unmarshal([]byte(data), &ch); err != nil {
			return out, fmt.Errorf("llama-server: chunk inválido: %w", err)
		}
		if ch.Error != nil {
			return out, fmt.Errorf("llama-server: %s", ch.Error.Message)
		}
		if ch.Usage != nil {
			out.PromptTokens, out.CompletionTokens = ch.Usage.PromptTokens, ch.Usage.CompletionTokens
		}
		for _, choice := range ch.Choices {
			if choice.Delta.Content != nil && *choice.Delta.Content != "" {
				text.WriteString(*choice.Delta.Content)
				if onToken != nil {
					onToken(*choice.Delta.Content)
				}
			}
			if choice.FinishReason != nil {
				out.FinishReason = *choice.FinishReason
			}
		}
	}
	if err := sc.Err(); err != nil {
		if ctx.Err() != nil {
			return out, ctx.Err()
		}
		return out, fmt.Errorf("llama-server: stream interrompido: %w", err)
	}
	if ctx.Err() != nil {
		return out, ctx.Err()
	}
	out.Text = text.String()
	return out, nil
}

// CountTokens implementa ports.ChatModel com /tokenize.
func (c *Client) CountTokens(ctx context.Context, text string) (int, error) {
	resp, err := c.post(ctx, "/tokenize", map[string]any{"content": text})
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	var out struct {
		Tokens []json.RawMessage `json:"tokens"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return 0, fmt.Errorf("llama-server /tokenize: %w", err)
	}
	return len(out.Tokens), nil
}

// Capabilities implementa ports.ChatModel. Antes de Probe, assume 4096 tokens.
func (c *Client) Capabilities() ports.Capabilities {
	c.capsMu.Lock()
	defer c.capsMu.Unlock()
	if !c.capsSet {
		return ports.Capabilities{ContextSize: 4096, Grammar: true}
	}
	return c.caps
}

// Probe lê /props e guarda o tamanho real da janela de contexto.
func (c *Client) Probe(ctx context.Context) (ports.Capabilities, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/props", nil)
	if err != nil {
		return ports.Capabilities{}, err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return ports.Capabilities{}, c.wrapDial(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ports.Capabilities{}, readError(resp)
	}
	var props struct {
		Settings struct {
			NCtx int `json:"n_ctx"`
		} `json:"default_generation_settings"`
		Caps struct {
			Tools bool `json:"supports_tool_calls"`
		} `json:"chat_template_caps"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&props); err != nil {
		return ports.Capabilities{}, fmt.Errorf("llama-server /props: %w", err)
	}
	if props.Settings.NCtx <= 0 {
		return ports.Capabilities{}, errors.New("llama-server /props: n_ctx ausente")
	}
	caps := ports.Capabilities{ContextSize: props.Settings.NCtx, Grammar: true, NativeTools: props.Caps.Tools}
	c.capsMu.Lock()
	c.caps, c.capsSet = caps, true
	c.capsMu.Unlock()
	return caps, nil
}

// Health diz se o servidor está pronto. Carregando o modelo conta como não.
func (c *Client) Health(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/health", nil)
	if err != nil {
		return err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return c.wrapDial(err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%w: %s respondeu %d (ainda carregando?)", domain.ErrModelDown, c.BaseURL, resp.StatusCode)
	}
	return nil
}

func (c *Client) post(ctx context.Context, path string, body any) (*http.Response, error) {
	buf, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+path, bytes.NewReader(buf))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, c.wrapDial(err)
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		return nil, readError(resp)
	}
	return resp, nil
}

func (c *Client) wrapDial(err error) error {
	if errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.ECONNRESET) {
		return fmt.Errorf("%w: nada respondendo em %s", domain.ErrModelDown, c.BaseURL)
	}
	var nerr net.Error
	if errors.As(err, &nerr) && nerr.Timeout() {
		return fmt.Errorf("%w: %s não respondeu a tempo", domain.ErrModelDown, c.BaseURL)
	}
	return fmt.Errorf("llama-server: %w", err)
}

func readError(resp *http.Response) error {
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	var e struct {
		Error apiError `json:"error"`
	}
	msg := strings.TrimSpace(string(data))
	if json.Unmarshal(data, &e) == nil && e.Error.Message != "" {
		msg = e.Error.Message
	}
	if resp.StatusCode == http.StatusServiceUnavailable {
		return fmt.Errorf("%w: %s", domain.ErrModelDown, msg)
	}
	if strings.Contains(msg, "exceeds the available context") || strings.Contains(msg, "context size") {
		return fmt.Errorf("%w: %s", domain.ErrContextBudget, msg)
	}
	return fmt.Errorf("llama-server respondeu %d: %s", resp.StatusCode, msg)
}

func (c *Client) observe(name string, v float64) {
	if c.Metrics != nil {
		c.Metrics.Observe(name, v, "role", string(c.Role))
	}
}
