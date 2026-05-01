# LLM modes

`config.yaml` has an `llm.backend` field with three valid values. The
choice is **opt-in** — you can run linklore with no LLM at all and the
core link-management features still work. Where features depend on the
LLM, the UI degrades cleanly (banner instead of broken state).

## The three modes

### `none`

No LLM. Linklore behaves as a pure FTS5 bookmark manager:

- ✅ Save links, fetch + extract content, render Preview.
- ✅ Search via BM25 (no semantic re-rank).
- ✅ Tag, organise into collections, drag-and-drop, bulk move.
- ❌ No TL;DR or auto-tags during ingest.
- ❌ No semantic-similarity re-rank in search.
- ❌ Chat tab in the drawer is hidden, and `/chat` shows a
  configuration banner explaining how to enable it.

### `ollama`

A local Ollama daemon at `OLLAMA_HOST` (default `http://127.0.0.1:11434`).
Configured in `llm.ollama` of `config.yaml`. All requests stay on the
same machine. Embedding model and chat model are independent — you
can run a tiny embed model alongside a heavier chat model.

### `litellm`

Any OpenAI-compatible gateway: LiteLLM proxy, vLLM,
`llama.cpp/server`, etc. Configured in `llm.litellm`. The
`base_url` + `api_key` env overrides are `LITELLM_BASE_URL` and
`LITELLM_API_KEY`. The model name in `llm.litellm.model` is what gets
sent in the chat completion request.

## Where the LLM is called

| Phase         | Operation                                | Required when |
|---------------|------------------------------------------|---------------|
| Ingest        | Per-link: chunk embeddings               | Optional. Skipped silently when backend is `none`. |
| Ingest        | Per-link: TL;DR + auto-tags (JSON)       | Optional. Skipped silently when backend is `none`. |
| Search        | Query-side embedding for cosine re-rank  | Optional. Search degrades to BM25-only. |
| Chat          | RAG answer (streaming via SSE)           | Required. The Chat tab is hidden when `none`; `/chat` shows a banner. |

## Health checks

Two endpoints expose status:

- `GET /healthz/llm` → renders a small badge in the topbar. Probes
  the configured backend with a short timeout.
- `GET /worker/status` → background-task status. Refreshed every 5 s
  by the SSE stream.

The `worker.LLMHealth()` cache is queried by `llmHealthSnapshot()` in
`internal/server/server.go` and surfaces both a boolean and a
backend-specific hint string ("set `OLLAMA_HOST`…" vs "set
`llm.litellm.base_url`…").

## Switching backends

Edit `config.yaml`, restart the server. The configured backend is
shown on `/settings`. Existing data is unaffected — embeddings already
in the DB stay valid until you change the embed model dimensions
(linklore stores the dim in the BLOB and rejects mismatches).

## Source code

- Interface: `internal/llm/llm.go` (`Backend` interface: `Generate`,
  `GenerateStream`, `Embed`).
- Implementations: `internal/llm/ollama/`, `internal/llm/litellm/`,
  `internal/llm/fake/` (test only).
- Config: `internal/config/config.go`.
- Health probe: `internal/worker/health.go` (cached snapshot).
