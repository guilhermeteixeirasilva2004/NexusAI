# Nexus AI

Agente local em Go: um binário (`nexus`) que orquestra modelos servidos pelo
`llama-server`, com memória, ferramentas e segurança próprias. O desenho completo
está no doc **Nexus AI — System Design v0.1**.

Estado: **Fase 1 (núcleo de produção)**. Conversa no terminal com loop ReAct,
4 ferramentas (`read_file`, `list_dir`, `memory_search`, `memory_save`),
memória em SQLite com FTS5, política de segurança, `/metrics` e evals. A API
REST + SSE chega na Fase 3.

## Uso

Para quem usa, o Nexus é um comando só, sem flags:

| Quero… | Digito |
| --- | --- |
| Conversar | `nexus` |
| Fazer uma pergunta rápida | `nexus "o que tem no arquivo notas.md?"` |
| Saber se está tudo funcionando | `nexus status` |

Na primeira vez, se nenhum modelo estiver configurado, o Nexus mostra os
arquivos `.gguf` que achar (em `~/llama.cpp/models`, `~/models`, `~/Downloads`),
recomenda um e grava a escolha. Depois disso, ele liga o `llama-server` sozinho
quando você abre a conversa e desliga ao sair.

Na conversa: fale normalmente. `/nova` começa outra conversa, `/ajuda` mostra
os comandos, `sair` ou Ctrl+D encerram, e Ctrl+C durante uma resposta só para a
resposta. As ferramentas de arquivo enxergam só a pasta de onde você chamou o
`nexus`, e guardar algo na memória sempre pergunta antes: "Posso guardar na
memória: ...? (s/n)".

Fora isso, não é preciso config para começar. O Nexus procura `~/.config/nexus/config.json`
e, sem ele, usa os padrões. Os dados ficam em lugares fixos, seja qual for a
pasta de onde você roda o comando:

| O quê | Onde |
| --- | --- |
| Config (opcional) | `~/.config/nexus/config.json` (modelo: `nexus.example.json`) |
| Memória (SQLite) | `~/.local/share/nexus/nexus.db` |
| Logs JSON | `~/.local/state/nexus/nexus.log`, nunca na tela da conversa |

As regras de experiência de uso estão em `docs/adr/0005-uso-sem-flags.md`.

## Desenvolvimento

```bash
make check     # portão de qualidade (instala as ferramentas em .tools/ na 1ª vez)
make build     # gera bin/nexus
make evals     # tool calling e injeção com modelo real (llama-server em :8081)
```

Metas de saída da Fase 1, medidas em 2026-11-14:

| Meta | Resultado |
| --- | --- |
| 20 turnos sobrevivem a `kill -9` | 3 rodadas, kill em momento aleatório depois de 26 a 32 turnos: banco íntegro, só turnos completos |
| 100% das ferramentas auditadas | toda chamada vai para `tool_runs`, inclusive negadas e repetidas; sem auditoria, o resultado é descartado |
| Tool calling ≥ 85% | 20/20 com Qwen2.5 7B Q4_K_M |
| Zero escrita sem confirmação em 20 ataques | 0/20 com modelo "obediente" (faz tudo que a injeção manda) e 0/20 com o 7B real (7 tentativas, todas barradas) |
| Cobertura do core ≥ 80% | 98,4% (`make cover`, dentro do `make check`) |

Flags de diagnóstico: `-config <arquivo>`, `-check-config` (imprime a config
final em JSON) e `-version`.

Precedência da config: padrões → arquivo → variáveis de ambiente. Campos
desconhecidos são erro, e todos os problemas aparecem de uma vez.

| Variável | Campo |
| --- | --- |
| `NEXUS_CONFIG` | caminho do arquivo de config |
| `NEXUS_LISTEN` | `server.listen` (lista separada por vírgula) |
| `NEXUS_MODEL_MAIN_URL`, `_AUX_URL`, `_EMBED_URL` | `models.<papel>.url` |
| `NEXUS_MAX_STEPS` | `loop.max_steps` |
| `NEXUS_STORAGE_PATH` | `storage.path` |
| `NEXUS_LOG_LEVEL`, `NEXUS_LOG_FORMAT`, `NEXUS_LOG_FILE` | `log.level`, `log.format`, `log.file` (`stderr` = terminal) |

Regras de segurança já aplicadas: o `nexus` só escuta em IP explícito (nunca
`0.0.0.0`), e cada `llama-server` precisa estar no loopback.

## Portão de qualidade (`make check`)

`go vet` · `golangci-lint` (staticcheck, errcheck, gofmt, depguard) ·
`go test -race` · cobertura do core ≥ 80% · `govulncheck`. As versões ficam fixadas no `Makefile` e a
toolchain no `go.mod`.

Regras de dependência, verificadas em dois lugares: depguard (`.golangci.yml`)
e `internal/archlint`, que roda no `go test`:

1. `internal/core/**` importa só a stdlib e o próprio core.
2. Um adapter não importa outro.
3. Fora de `internal/adapters`, só `cmd/` e `test/` importam adapters.

## Estrutura

```
cmd/nexus/          composition root
internal/core/      domain, ports, orchestrator, cognition, policy (só stdlib)
internal/adapters/  inbound/{httpapi,cli}, outbound/{llamacpp,sqlitestore,tools,telemetry}
internal/config/    arquivo + variáveis de ambiente
internal/archlint/  checador das regras de import
test/               integration, evals
deploy/             systemd, WireGuard (Fase 3)
docs/adr/           decisões
```
