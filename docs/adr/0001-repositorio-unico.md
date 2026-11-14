# 0001 — Um repositório e um go.mod

Data: 2026-11-14 · Status: aceita

**Decisão.** Um repositório e um módulo Go (`nexusai`).

**Por quê.** Monolito modular é um binário. Com vários go.mod, cada mudança de porta vira tag e atualização em vários repositórios, para um dev só. A fronteira entre módulos é protegida pelo pacote, por `internal/` e pelo lint de imports.

**Revisitar quando** existir um segundo consumidor de algum pacote. Extrair é mover a pasta e criar o go.mod.
