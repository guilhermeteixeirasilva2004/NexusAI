# 0003 — Config em JSON, só com a stdlib

Data: 2026-11-14 · Status: aceita

**Decisão.** Arquivo JSON lido com `encoding/json` e `DisallowUnknownFields`, sobrescrito por variáveis `NEXUS_*`. A validação junta todos os erros com `errors.Join`.

**Por quê.** Nenhuma dependência nova na Fase 0, e um campo digitado errado vira erro em vez de ser ignorado. JSON não aceita comentários; o `nexus.example.json` e o README documentam os campos.

**Revisitar** se a config crescer a ponto de comentários fazerem falta (TOML seria a opção).
