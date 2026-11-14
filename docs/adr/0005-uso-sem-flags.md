# 0005 — Uso do dia a dia sem flags

Data: 2026-11-14 · Status: aceita

**Decisão.** Para quem usa, o Nexus é um comando só. `nexus` abre a conversa, `nexus "pergunta"` dá uma resposta única e `nexus status` diz se está tudo certo. Flags existem apenas para diagnóstico e desenvolvimento.

**Regras que valem para toda fase:**

- Zero configuração para começar. A config é procurada sozinha (`NEXUS_CONFIG`, depois `~/.config/nexus/config.json`); sem arquivo, valem os padrões.
- Caminhos fixos no padrão XDG: memória em `~/.local/share/nexus/`, logs em `~/.local/state/nexus/`. Nada depende da pasta de onde o comando roda.
- Os modelos sobem sozinhos (Fase 1). Se o `llama-server` não estiver no ar, o Nexus inicia ou diz em uma frase o que falta.
- A conversa continua de onde parou; `/nova` começa outra.
- A resposta aparece token a token. Os passos do ReAct viram uma linha curta ("lendo notas.md…"), sem JSON.
- Confirmações em português simples: "Posso apagar 3 arquivos em ~/fotos? (s/n)".
- Erros em uma frase com o que fazer; o detalhe técnico vai para o log.
- Logs JSON vão para arquivo, nunca para a tela da conversa.
- Poucos comandos: `/nova`, `/ajuda`, `/sair`; `sair`, Ctrl+D e Ctrl+C também encerram.

**Por quê.** O Nexus é para conversar de forma natural. Flags e caminhos relativos obrigam a pessoa a lembrar de detalhes técnicos a cada uso.

**Consequência.** A meta de saída da Fase 1 inclui conversar só digitando `nexus`, sem flags nem arquivo de config.
