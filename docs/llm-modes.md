# LLM modes

Linklore's LLM is **opt-in**. The core (saving links, fetch + extract,
BM25 search, tags, drag-and-drop, highlights, reminders) all run with
no LLM at all. When the LLM is disabled the UI degrades gracefully:
banners instead of broken pages.

The `LINKLORE_LLM_BACKEND` env var (or the legacy `llm.backend` yaml
field) picks one of three values. **Pick `openai` unless you have a
specific reason not to.**

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
| Ollama (`/v1`) | `http://localhost:11434/v1`           |
| llama.cpp      | `http://localhost:8080/v1`            |
| vLLM           | `http://localhost:8000/v1`            |
| LM Studio      | `http://localhost:1234/v1`            |
| LiteLLM proxy  | `http://localhost:4000/v1`            |
| OpenAI itself  | `https://api.openai.com/v1`           |

Env vars:

- `OPENAI_BASE_URL`
- `OPENAI_API_KEY` (use any non-empty string for local servers that
  don't auth — `ollama`, `lm-studio`, etc.)
- `LINKLORE_LLM_MODEL` — the chat model name your server advertises
- `LINKLORE_LLM_EMBED_MODEL` — the embedding model

Switching servers means changing one URL. The old `llm.backend=litellm`
value is accepted as a deprecated alias and silently rewritten to
`openai` at startup; `LITELLM_BASE_URL` / `LITELLM_API_KEY` work as
aliases of the canonical `OPENAI_*` names.

### `ollama` — legacy native API

This backend uses Ollama's **native** `/api/generate`, `/api/embed`,
`/api/tags` endpoints — _not_ the OpenAI-compatible `/v1/...` path.
It exists for backward compatibility and for the rare case where you
want native Ollama options that aren't surfaced over `/v1`.

For most users **prefer `openai` with `OPENAI_BASE_URL=http://localhost:11434/v1`**
— same daemon, less divergence in linklore code.

Env vars: `OLLAMA_HOST`, `LINKLORE_LLM_MODEL`, `LINKLORE_LLM_EMBED_MODEL`.

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
`llmConfigHint()` produces a backend-specific "how to enable" string
(OPENAI_* vs OLLAMA_HOST).

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
  - `internal/llm/ollama/` — Ollama native API client.
  - `internal/llm/fake/` — test fake.
- Config: `internal/config/config.go` — `canonicaliseLLM` resolves
  `litellm` → `openai` and mirrors `LiteLLM` ↔ `OpenAI` structs.
- Health probe: `internal/worker/health.go`.
