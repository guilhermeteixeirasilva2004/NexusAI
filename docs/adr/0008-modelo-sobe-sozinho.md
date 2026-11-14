# 0008 — O Nexus sobe e desce o llama-server

Data: 2026-11-14 · Status: aceita

**Decisão.** Ao abrir a conversa, o Nexus testa o `/health` do modelo main. Se ele não responde, o Nexus sobe o `llama-server` com `-np 1 --no-webui` (o `-ngl` fica no padrão `auto`) e o desliga ao sair. No Linux, `Pdeathsig` garante que o servidor morre junto se o Nexus levar `kill -9`.

Sem modelo configurado, o Nexus lista os GGUF de chat encontrados no disco, recomenda o maior de até 8 GB e grava a escolha em `~/.config/nexus/config.json`. Com `gguf_sha256` na config, ele confere o checksum antes de subir; o resultado fica em cache por caminho, tamanho e data.

**Custo conhecido.** Ligar o 7B leva de 10 a 35 s, e isso acontece a cada `nexus`. Manter o servidor vivo entre conversas (por exemplo, via systemd na Fase 3) resolve isso.
