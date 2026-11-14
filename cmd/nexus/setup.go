package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"nexusai/internal/adapters/outbound/llamacpp"
	"nexusai/internal/config"
	"nexusai/internal/core/domain"
)

// ensureMainModel garante o modelo main no ar. Se já está, usa; se não, sobe
// o llama-server. Sem modelo configurado, oferece os que achar no disco e
// grava a escolha. Devolve o processo iniciado (nil se já estava no ar).
func ensureMainModel(ctx context.Context, cfg *config.Config, client *llamacpp.Client, term terminal, in *bufio.Reader, env func(string) (string, bool), log *slog.Logger) (*llamacpp.Process, error) {
	hctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	err := client.Health(hctx)
	cancel()
	if err == nil {
		log.Info("modelo main já estava no ar", "url", client.BaseURL)
		return nil, nil
	}

	home, _ := env("HOME")
	m := cfg.Models[domain.RoleMain]
	binary := cfg.LlamaServer
	if binary == "" {
		binary = llamacpp.FindBinary(home)
	}

	if m.GGUF == "" {
		chosen, err := chooseModel(term, in, home)
		if err != nil {
			return nil, err
		}
		m.GGUF, m.Model = chosen.Path, chosen.Name()
		cfg.Models[domain.RoleMain] = m
		path := cfg.Source
		if path == "" {
			path = config.DefaultConfigPath(env)
		}
		if path != "" {
			if err := config.SaveModel(path, domain.RoleMain, m, binary); err != nil {
				log.Warn("não consegui gravar a escolha do modelo", "error", err)
			} else {
				fmt.Fprintf(term.out, "Guardei a escolha em %s.\n", path)
			}
		}
	}
	if binary == "" {
		return nil, userMessage(`Não achei o llama-server. Instale o llama.cpp ou diga onde ele está com "llama_server" em ~/.config/nexus/config.json.`)
	}

	if err := llamacpp.VerifyChecksum(m.GGUF, m.GGUFSHA256, stateDir(*cfg)); err != nil {
		return nil, userMessage("Não vou usar este modelo: " + err.Error())
	}

	fmt.Fprintf(term.out, "Ligando o modelo %s… (leva alguns segundos)\n", m.Model)
	start := time.Now()
	proc, err := llamacpp.Start(ctx, llamacpp.ServerSpec{
		Binary:      binary,
		Model:       m.GGUF,
		BaseURL:     m.URL,
		ContextSize: m.ContextSize,
		Alias:       m.Model,
		Args:        m.Args,
		LogPath:     filepath.Join(stateDir(*cfg), "llama-main.log"),
	})
	if err != nil {
		return nil, userMessage("Não consegui ligar o modelo: " + err.Error())
	}
	log.Info("llama-server iniciado", "role", "main", "pid", proc.PID(), "seconds", time.Since(start).Seconds())
	return proc, nil
}

// userMessage é um erro escrito para a pessoa ler: frase completa, com
// maiúscula e ponto, mostrado como está.
type userMessage string

func (m userMessage) Error() string { return string(m) }

// chooseModel mostra os GGUF encontrados e pergunta qual usar. Sem terminal
// para perguntar, escolhe o recomendado.
func chooseModel(term terminal, in *bufio.Reader, home string) (llamacpp.ModelFile, error) {
	found := llamacpp.FindModels(home)
	if len(found) == 0 {
		return llamacpp.ModelFile{}, userMessage(`Não achei nenhum modelo .gguf (procurei em ~/llama.cpp/models, ~/models e ~/Downloads).
Baixe um modelo de chat, por exemplo Qwen2.5 7B Instruct em GGUF, e rode nexus de novo.`)
	}
	rec := recommended(found)
	if !term.inTTY || len(found) == 1 {
		fmt.Fprintf(term.out, "Vou usar o modelo %s.\n", found[rec].Name())
		return found[rec], nil
	}
	fmt.Fprintln(term.out, "Ainda não tenho um modelo escolhido. Encontrei estes no seu computador:")
	for i, f := range found {
		note := ""
		if i == rec {
			note = "  (recomendado)"
		}
		fmt.Fprintf(term.out, "  %d) %-40s %5.1f GB%s\n", i+1, f.Name(), float64(f.Size)/(1<<30), note)
	}
	for {
		fmt.Fprintf(term.out, "Qual usar para conversar? [%d] ", rec+1)
		line, err := in.ReadString('\n')
		line = strings.TrimSpace(line)
		if line == "" {
			if err != nil && !errors.Is(err, io.EOF) {
				return llamacpp.ModelFile{}, err
			}
			return found[rec], nil
		}
		n, convErr := strconv.Atoi(line)
		if convErr == nil && n >= 1 && n <= len(found) {
			return found[n-1], nil
		}
		fmt.Fprintf(term.out, "Digite um número de 1 a %d.\n", len(found))
		if err != nil {
			return found[rec], nil
		}
	}
}

// recommended é o maior modelo de até 8 GB: cabe na RAM de um PC comum e
// conversa melhor que os menores.
func recommended(found []llamacpp.ModelFile) int {
	for i, f := range found {
		if f.Size <= 8<<30 {
			return i
		}
	}
	return len(found) - 1
}
