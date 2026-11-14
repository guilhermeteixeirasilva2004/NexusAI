# 0002 — Regras de dependência verificadas no build

Data: 2026-11-14 · Status: aceita

**Decisão.** O core importa só a stdlib e o próprio core. Adapters não se importam entre si. Só `cmd/` e `test/` importam adapters.

**Como é verificado.** Em duas camadas: o depguard no golangci-lint (regra do core) e `internal/archlint` no `go test` (as três regras). O archlint é só stdlib e roda mesmo sem o linter instalado. Um fixture em `testdata` garante que ele detecta violações.
