# LLM modes

Linklore's LLM is **opt-in**. The core (saving links, fetch + extract,
BM25 search, tags, drag-and-drop, highlights, reminders) all run with
no LLM at all. When the LLM is disabled the UI degrades gracefully:
banners instead of broken pages.

The `LINKLORE_LLM_BACKEND` env var picks the backend. **Use `openai`
for any local LLM — it covers every common server through the
OpenAI-compatible `/v1` API.** `none` opts out entirely.

## The backends

### `none`

No LLM. Linklore behaves as a pure FTS5 bookmark manager:

- ✅ Save links, fetch + extract, render Preview, render highlights.
- ✅ Search via BM25 (no semantic re-rank).
- ✅ Tags, collections, drag-and-drop, bulk move, reminders.
- ❌ No TL;DR or auto-tags during ingest.
- ❌ No semantic-similarity re-rank in search.
- ❌ Chat tab in the drawer is hidden; `/chat` shows a config banner.

### `openai` — the canonical choice

Any **OpenAI-compatible** HTTP API. This single backend covers every
common local LLM server, because they all expose the same
`/chat/completions` + `/embeddings` shape:

| Server         | Typical `OPENAI_BASE_URL`             |
|----------------|---------------------------------------|
| llama.cpp      | `http://localhost:8080/v1`            |
| vLLM           | `http://localhost:8000/v1`            |
| LM Studio      | `http://localhost:1234/v1`            |
| OpenAI itself  | `https://api.openai.com/v1`           |

Env vars:

- `OPENAI_BASE_URL`
- `OPENAI_API_KEY` (use any non-empty string for local servers that
  don't auth)
- `LINKLORE_LLM_MODEL` — the chat model name your server advertises
- `LINKLORE_LLM_EMBED_MODEL` — the embedding model

Switching servers means changing one URL.

<details>
<summary>Using Ollama? Two options.</summary>

The recommended path is **`openai`** — Ollama exposes an
OpenAI-compatible endpoint at `/v1`, so the linklore code path is
identical to every other server:

```ini
LINKLORE_LLM_BACKEND=openai
OPENAI_BASE_URL=http://localhost:11434/v1
OPENAI_API_KEY=ollama
```

A separate `LINKLORE_LLM_BACKEND=ollama` exists that uses Ollama's
native `/api/generate`, `/api/embed`, `/api/tags` endpoints. It's only
useful if you need a flag the `/v1` shim doesn't surface — otherwise
prefer `openai`. Env vars: `OLLAMA_HOST`, `LINKLORE_LLM_MODEL`,
`LINKLORE_LLM_EMBED_MODEL`.

</details>

## Where the LLM is called

| Phase   | Operation                              | Required? |
|---------|----------------------------------------|-----------|
| Ingest  | Per-link chunk embeddings              | Optional. Skipped silently when backend is `none`. |
| Ingest  | Per-link TL;DR + auto-tags             | Optional. Skipped silently when backend is `none`. |
| Search  | Query-side embedding for cosine re-rank| Optional. Falls back to BM25-only. |
| Chat    | RAG answer (streamed via SSE)          | **Required.** Chat tab is hidden + `/chat` shows a banner when `none`. |

## Health checks

- `GET /healthz/llm` — renders the topbar status badge. Probes the
  configured backend with a short timeout.
- `GET /worker/status` — background-task status; SSE-pushed every 5 s.

`worker.LLMHealth()` caches the last result and the server's
`llmConfigHint()` produces a "how to enable" string for the configured
backend.

## Switching backends

Edit `.env` (or `/settings` in the UI), restart. The new backend takes
over for fresh ingests, and the chat. Existing embeddings keep
working until you change the embed model dimensions — linklore stores
the dim in the BLOB and rejects mismatches.

## Source code

- Interface: `internal/llm/backend.go` (`Backend.Generate`,
  `GenerateStream`, `Embed`; constants `BackendOpenAI`, `BackendOllama`,
  `BackendNone`).
- Implementations:
  - `internal/llm/litellm/` — the OpenAI-compatible client (used for
    `backend=openai`). The package keeps its historical name; only
    the user-facing config value changed.
  - `internal/llm/ollama/` — Ollama native API client (legacy).
  - `internal/llm/fake/` — test fake.
- Config: `internal/config/config.go`.
- Health probe: `internal/worker/health.go`.
