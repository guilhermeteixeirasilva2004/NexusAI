# 0007 — Qwen2.5 como família e JSONCodec tolerante

Data: 2026-11-14 · Status: aceita

**Decisão.** A família dos modelos é a Qwen2.5 (7B main, 3B aux, 0.5B/1.5B como rascunho), que já está baixada e compartilha o tokenizer. O `JSONCodec` pede `{"tool", "args"}`, mas aceita na leitura os formatos que o Qwen usa de fato:
- `{"name", "arguments"}`;
- a tag `<tool_call>`;
- cercas de código;
- `nome({...})`;
- uma frase antes da chamada, desde que a chamada feche a saída.

**Por quê.** No primeiro eval, o 7B escreveu `memory_save({...})` e "Vou ler… {json}". O codec rígido tratava isso como resposta, e o JSON cru ia para a tela. Para quem usa, isso quebra a conversa.

**Regras para não confundir resposta com chamada.** Uma chamada só vale no começo de uma linha e ocupando o resto da saída. Bloco de código e JSON sem `"tool"` continuam sendo resposta. O portão de streaming avalia o começo de cada linha, então um JSON nunca aparece na tela.

**Resultado (Qwen2.5 7B Q4_K_M):** tool calling 20/20. O caso do README aceita `list_dir` como primeira escolha, porque o nome exato do arquivo não é dado.
