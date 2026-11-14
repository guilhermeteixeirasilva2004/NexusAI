package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	"nexusai/internal/adapters/inbound/cli"
	"nexusai/internal/adapters/outbound/llamacpp"
	"nexusai/internal/adapters/outbound/sqlitestore"
	"nexusai/internal/adapters/outbound/telemetry"
	"nexusai/internal/adapters/outbound/tools"
	"nexusai/internal/config"
	"nexusai/internal/core/cognition"
	"nexusai/internal/core/domain"
	"nexusai/internal/core/orchestrator"
	"nexusai/internal/core/policy"
	"nexusai/internal/core/ports"
)

// converse monta o Nexus e abre a conversa (ou responde uma pergunta só).
func converse(ctx context.Context, cfg config.Config, log *slog.Logger, term terminal, env func(string) (string, bool), question string) int {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	in := bufio.NewReader(term.in)

	// Ctrl+C antes da conversa começar (subindo o modelo) cancela tudo.
	var current atomic.Pointer[cli.Chat]
	go func() {
		for range term.interr {
			if c := current.Load(); c == nil || !c.Interrupt() {
				cancel()
			}
		}
	}()

	store, err := sqlitestore.Open(ctx, cfg.Storage.Path)
	if err != nil {
		fmt.Fprintln(term.err, "nexus: não consegui abrir a memória:", err)
		return 1
	}
	defer store.Close()

	metrics := telemetry.New()
	model := llamacpp.New(cfg.Models[domain.RoleMain].URL, domain.RoleMain)
	model.Metrics = metrics

	proc, err := ensureMainModel(ctx, &cfg, model, term, in, env, log)
	if err != nil {
		if ctx.Err() == nil {
			fmt.Fprintln(term.err, err)
		}
		return 1
	}
	defer func() {
		if err := proc.Stop(); err != nil {
			log.Warn("parar llama-server", "error", err)
		}
	}()
	caps, err := model.Probe(ctx)
	if err != nil {
		fmt.Fprintln(term.err, "nexus: o modelo respondeu, mas não consegui ler as configurações dele:", err)
		return 1
	}
	log.Info("modelo pronto", "role", "main", "url", model.BaseURL, "context_size", caps.ContextSize)

	stopMetrics := serveMetrics(cfg.Server.Listen, metrics, log)
	defer stopMetrics()

	workdir := cfg.Tools.Workdir
	if workdir == "" {
		workdir, _ = os.Getwd()
	}
	home, _ := env("HOME")
	toolset, err := tools.All(workdir, store)
	if err != nil {
		fmt.Fprintln(term.err, "nexus:", err)
		return 1
	}

	chat := &cli.Chat{Sessions: store, In: in, Out: term.out, Color: term.color, LogPath: cfg.Log.File}
	current.Store(chat)
	var confirmer ports.Confirmer = chat
	if !term.inTTY {
		confirmer = nil // sem ninguém para responder, ações de escrita não rodam
	}
	chat.Asker = &orchestrator.Orchestrator{
		Models:    map[domain.Role]ports.ChatModel{domain.RoleMain: model},
		Sessions:  store,
		Memory:    store,
		Audit:     store,
		Tools:     toolset,
		Codec:     cognition.JSONCodec{},
		Policy:    policy.New(cfg.Tools.Allow...),
		Confirmer: confirmer,
		Metrics:   metrics,
		Log:       log,
		System:    cognition.SystemPrompt(cognition.DisplayPath(workdir, home), time.Now()),
		Limits: orchestrator.Limits{
			MaxSteps:       cfg.Loop.MaxSteps,
			RequestTimeout: cfg.Loop.RequestTimeout.Duration,
			MaxTokens:      cfg.Loop.MaxTokens,
			Temperature:    cfg.Loop.Temperature,
		},
	}

	if question != "" {
		if err := chat.Once(ctx, question); err != nil {
			return 1
		}
		return 0
	}
	if err := chat.Run(ctx); err != nil {
		fmt.Fprintln(term.err, "nexus:", err)
		return 1
	}
	return 0
}

// serveMetrics expõe /metrics nos endereços da config. Se a porta estiver
// ocupada, só registra no log: métrica não pode atrapalhar a conversa.
func serveMetrics(addrs []string, h http.Handler, log *slog.Logger) func() {
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", h)
	var servers []*http.Server
	for _, addr := range addrs {
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			log.Warn("métricas indisponíveis", "addr", addr, "error", err)
			continue
		}
		srv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
		servers = append(servers, srv)
		go func() {
			if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
				log.Warn("servidor de métricas parou", "error", err)
			}
		}()
		log.Info("métricas em /metrics", "addr", addr)
	}
	return func() {
		for _, s := range servers {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			_ = s.Shutdown(ctx)
			cancel()
		}
	}
}

// stateDir é onde ficam os logs dos llama-server e o cache de checksums.
func stateDir(cfg config.Config) string {
	if cfg.Log.File != "" && cfg.Log.File != "stderr" {
		return filepath.Dir(cfg.Log.File)
	}
	return filepath.Join(os.TempDir(), "nexus")
}
