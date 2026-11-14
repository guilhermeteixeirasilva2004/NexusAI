# Nexus AI

Agente local em Go: um binário (`nexus`) que orquestra modelos servidos pelo
`llama-server`, com memória, ferramentas e segurança próprias. O desenho completo
está no doc **Nexus AI — System Design v0.1**.

Estado: **Fase 0 (fundação)**. Config validada, logs, portão de qualidade e
regras de arquitetura. Servidor, loop ReAct e adapters chegam na Fase 1.

## Uso

Para quem usa, o Nexus é um comando só, sem flags:

| Quero… | Digito |
| --- | --- |
| Conversar | `nexus` (Fase 1; hoje mostra o status) |
| Fazer uma pergunta rápida | `nexus "como acho os arquivos maiores daqui?"` (Fase 1) |
| Saber se está tudo funcionando | `nexus status` |

Não é preciso config para começar. O Nexus procura `~/.config/nexus/config.json`
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
```

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
`go test -race` · `govulncheck`. As versões ficam fixadas no `Makefile` e a
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
