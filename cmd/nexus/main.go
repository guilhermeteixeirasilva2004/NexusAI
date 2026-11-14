// Command nexus é o composition root: lê a config, cria os adapters e os
// injeta no core. É o único pacote que conhece tipos concretos.
//
// Uso do dia a dia, sem flags:
//
//	nexus                  conversar
//	nexus "pergunta"       resposta única
//	nexus status           diz se está tudo certo, em português simples
//
// As flags (-config, -check-config, -version) são para diagnóstico.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"nexusai/internal/config"
)

// version é preenchida no build: -ldflags "-X main.version=..."
var version = "dev"

// terminal descreve a entrada e a saída do processo.
type terminal struct {
	in     io.Reader
	out    io.Writer
	err    io.Writer
	inTTY  bool // dá para perguntar coisas à pessoa
	color  bool // cores e indicador "pensando…"
	interr <-chan os.Signal
}

func main() {
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM)
	term := terminal{
		in: os.Stdin, out: os.Stdout, err: os.Stderr,
		inTTY:  isTTY(os.Stdin),
		color:  isTTY(os.Stdout) && os.Getenv("NO_COLOR") == "" && os.Getenv("TERM") != "dumb",
		interr: sigs,
	}
	os.Exit(run(context.Background(), os.Args[1:], term, os.LookupEnv))
}

func isTTY(f *os.File) bool {
	st, err := f.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}

func run(ctx context.Context, args []string, term terminal, env func(string) (string, bool)) int {
	fs := flag.NewFlagSet("nexus", flag.ContinueOnError)
	fs.SetOutput(term.err)
	fs.Usage = func() {
		fmt.Fprint(term.err, `Uso:
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
		fmt.Fprintln(term.out, version)
		return 0
	}

	cfg, err := config.Load(*cfgPath, env)
	if err != nil {
		fmt.Fprintln(term.err, "nexus:", err)
		return 1
	}
	if *check {
		enc := json.NewEncoder(term.out)
		enc.SetIndent("", "  ")
		if err := enc.Encode(cfg); err != nil {
			fmt.Fprintln(term.err, "nexus:", err)
			return 1
		}
		return 0
	}

	logOut, err := cfg.Log.Open()
	if err != nil {
		fmt.Fprintln(term.err, "nexus: não consegui abrir o arquivo de log:", err)
		return 1
	}
	defer logOut.Close()
	log, err := cfg.Log.NewLogger(logOut)
	if err != nil {
		fmt.Fprintln(term.err, "nexus:", err)
		return 1
	}
	log = log.With("version", version)

	if fs.NArg() == 1 && fs.Arg(0) == "status" {
		printStatus(ctx, term.out, cfg, env)
		return 0
	}
	question := strings.TrimSpace(strings.Join(fs.Args(), " "))
	return converse(ctx, cfg, log, term, env, question)
}
