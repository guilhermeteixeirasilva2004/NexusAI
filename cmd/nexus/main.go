// Command nexus é o composition root: lê a config, cria os adapters e os
// injeta no core. É o único pacote que conhece tipos concretos.
//
// Uso do dia a dia, sem flags:
//
//	nexus                  abre a conversa (Fase 1; hoje mostra o status)
//	nexus "pergunta"       resposta única (Fase 1)
//	nexus status           diz se está tudo certo, em português simples
//
// As flags (-config, -check-config, -version) são para diagnóstico.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"

	"nexusai/internal/config"
	"nexusai/internal/core/domain"
)

// version é preenchida no build: -ldflags "-X main.version=..."
var version = "dev"

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr, os.LookupEnv))
}

func run(args []string, stdout, stderr io.Writer, env func(string) (string, bool)) int {
	fs := flag.NewFlagSet("nexus", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprint(stderr, `Uso:
  nexus                 conversar com o Nexus
  nexus "pergunta"      fazer uma pergunta rápida
  nexus status          ver se está tudo funcionando

Opções de diagnóstico:
`)
		fs.PrintDefaults()
	}
	cfgPath := fs.String("config", "", "usar outro arquivo de config (padrão: ~/.config/nexus/config.json)")
	check := fs.Bool("check-config", false, "imprimir a config final em JSON e sair")
	showVersion := fs.Bool("version", false, "imprimir a versão e sair")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *showVersion {
		fmt.Fprintln(stdout, version)
		return 0
	}

	cfg, err := config.Load(*cfgPath, env)
	if err != nil {
		fmt.Fprintln(stderr, "nexus:", err)
		return 1
	}
	if *check {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(cfg); err != nil {
			fmt.Fprintln(stderr, "nexus:", err)
			return 1
		}
		return 0
	}

	logOut, err := cfg.Log.Open()
	if err != nil {
		fmt.Fprintln(stderr, "nexus: não consegui abrir o arquivo de log:", err)
		return 1
	}
	defer logOut.Close()
	log, err := cfg.Log.NewLogger(logOut)
	if err != nil {
		fmt.Fprintln(stderr, "nexus:", err)
		return 1
	}
	attrs := []any{"version", version, "config", cfg.Source, "listen", cfg.Server.Listen, "storage", cfg.Storage.Path, "max_steps", cfg.Loop.MaxSteps}
	for _, r := range domain.Roles() {
		m := cfg.Models[r]
		attrs = append(attrs, "model_"+string(r), m.Model+" @ "+m.URL)
	}
	log.Info("config carregada", attrs...)

	switch {
	case fs.NArg() == 0 || (fs.NArg() == 1 && fs.Arg(0) == "status"):
		printStatus(stdout, cfg)
		if fs.NArg() == 0 {
			fmt.Fprintln(stdout, "\nA conversa chega na Fase 1. Por enquanto o Nexus só confere a configuração.")
		}
		return 0
	default:
		fmt.Fprintln(stderr, "nexus: perguntas diretas (nexus \"...\") chegam na Fase 1.")
		return 2
	}
}

func printStatus(w io.Writer, cfg config.Config) {
	source := "padrões (nenhum arquivo de config encontrado)"
	if cfg.Source != "" {
		source = cfg.Source
	}
	fmt.Fprintln(w, "Nexus", version, "· configuração ok")
	fmt.Fprintln(w, "  config:      ", source)
	for _, r := range domain.Roles() {
		m := cfg.Models[r]
		fmt.Fprintf(w, "  modelo %-6s %s em %s\n", string(r)+":", m.Model, m.URL)
	}
	fmt.Fprintln(w, "  memória:     ", cfg.Storage.Path)
	fmt.Fprintln(w, "  logs:        ", cfg.Log.File)
}
