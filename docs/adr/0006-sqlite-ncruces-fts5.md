# 0006 — SQLite com ncruces/go-sqlite3, FTS5 e WAL

Data: 2026-11-14 · Status: aceita

**Decisão.** O store usa `github.com/ncruces/go-sqlite3` v0.36.0, sem cgo, com a extensão FTS5 registrada em cada conexão. O banco roda em modo WAL com `synchronous=NORMAL`. As migrações são arquivos SQL embutidos, controlados por `PRAGMA user_version`.

**Por quê.** É o caminho que o design prevê para o sqlite-vec sem cgo; a mesma versão já traz `ext/vec1`, útil na Fase 2. Em WAL, uma transação confirmada sobrevive a `kill -9`, e cada turno é gravado numa transação só. Fixei uma versão com mais de duas semanas de lançada.

**Verificado por** `test/integration/kill_test.go`: 3 rodadas de `kill -9` em momento aleatório. Todas terminaram com o banco íntegro, só com turnos completos e pelo menos 20 turnos.
