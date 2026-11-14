# 0004 — Toolchain e ferramentas fixadas por projeto

Data: 2026-11-14 · Status: aceita

**Decisão.** O `go.mod` fixa `toolchain go1.27.4`. golangci-lint v2.15.0 e govulncheck v1.9.0 são instalados em `.tools/`, compilados com essa mesma toolchain.

**Por quê.** Com o Go 1.22.1 do sistema, o govulncheck acusou 7 vulnerabilidades da stdlib alcançáveis pelo código (net/netip, go/parser, os, entre outras). Instalar em `.tools/` evita trocar as versões globais de `$GOPATH/bin`, que outros projetos usam.

**Para atualizar:** mude a versão no `go.mod` ou no `Makefile` e rode `make tools`.
