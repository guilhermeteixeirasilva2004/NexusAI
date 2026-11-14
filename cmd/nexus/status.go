package main

import (
	"context"
	"fmt"
	"io"
	"time"

	"nexusai/internal/adapters/outbound/llamacpp"
	"nexusai/internal/config"
	"nexusai/internal/core/domain"
)

func printStatus(ctx context.Context, w io.Writer, cfg config.Config, env func(string) (string, bool)) {
	source := "padrões (nenhum arquivo de config encontrado)"
	if cfg.Source != "" {
		source = cfg.Source
	}
	fmt.Fprintln(w, "Nexus", version, "· configuração ok")
	fmt.Fprintln(w, "  config:      ", source)

	m := cfg.Models[domain.RoleMain]
	hctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	err := llamacpp.New(m.URL, domain.RoleMain).Health(hctx)
	cancel()
	home, _ := env("HOME")
	binary := cfg.LlamaServer
	if binary == "" {
		binary = llamacpp.FindBinary(home)
	}
	var state string
	switch {
	case err == nil:
		state = "no ar em " + m.URL
	case m.GGUF != "" && binary != "":
		state = "desligado; eu ligo quando você abrir a conversa"
	case m.GGUF != "":
		state = "desligado, e não achei o llama-server para ligar"
	default:
		state = "nenhum escolhido; na primeira conversa eu mostro os que achar"
	}
	fmt.Fprintf(w, "  modelo:       %s (%s)\n", m.Model, state)
	fmt.Fprintln(w, "  memória:     ", cfg.Storage.Path)
	fmt.Fprintln(w, "  logs:        ", cfg.Log.File)
}
