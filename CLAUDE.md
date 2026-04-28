# Linklore

Local-first link/bookmark manager. Go + SQLite (WAL + FTS5 + BLOB embeddings) + HTMX UI + LLM (Ollama / LiteLLM+vLLM on DGX) for summary, auto-tags, and RAG chat.

See `PLAN.md` for full architecture, schema, and phased TODO list.

## Project layout

```
cmd/linklore/        # single binary: serve | ingest-url | reindex
internal/
  config/            # YAML + env override
  storage/           # SQLite WAL + FTS5 + migrations
  llm/               # Backend iface + ollama, litellm (mirrors graphrag)
  extract/           # HTTP fetch + readability + html→md
  summarize/         # LLM TL;DR + auto-tags
  embed/             # embedding service (BLOB) + cosine
  search/            # hybrid FTS5 + vector ranking
  chat/              # RAG context + streaming endpoint
  server/            # http.ServeMux + handlers + html/template
  worker/            # background fetch/summary/embed queue
web/templates/       # html/template
web/static/          # htmx + pico.css from CDN; minimal local app.css
configs/config.yaml
data/linklore.db
```

## Key commands

```bash
make build           # build ./bin/linklore
make run             # go run ./cmd/linklore serve
make test            # go test -race ./...
make check           # fmt + vet + lint + test
make migrate         # apply DB migrations (idempotent)
./bin/linklore serve --config ./configs/config.yaml
```

## Tech choices (HARD)

- **Go 1.25+**, stdlib `net/http.ServeMux` (no chi/gin), stdlib `html/template`.
- **SQLite via `github.com/mattn/go-sqlite3`** with WAL, FTS5, BLOB embeddings. No `sqlite-vec` — cosine in Go is fine at this scale.
- **HTMX + pico.css from CDN.** No Node, no npm, no JS build chain. Alpine.js only if strictly needed.
- **LLM**: `Backend` interface mirrored from graphrag (`Generate`, `GenerateStream`, `Embed`). Ollama + LiteLLM+vLLM backends; switchable via config.
- **HTML→Markdown** before sending to LLM (smaller context, better signal). Library: `JohannesKaufmann/html-to-markdown/v2`.
- **Readability**: `go-shiori/go-readability`.
- **OG/meta tags**: `PuerkitoBio/goquery`.

## Code style

- **Small, flat packages.** Keep `internal/<domain>` shallow. No unneeded interfaces. Define an interface at the consumer site only when there's a real second implementation or a fake for tests.
- **No premature abstraction.** Three similar lines beats a generic helper. Don't add config knobs for things that aren't varying yet.
- **Error handling**: wrap with `fmt.Errorf("doing X: %w", err)`. Never swallow. Don't log+return — pick one (return up, log at the boundary).
- **Context first**: every function that does I/O takes `ctx context.Context` as the first parameter. Pass it through; do not stash it in structs.
- **No globals** for state (loggers and metrics OK). Pass dependencies (DB, LLM backend, logger) explicitly.
- **Concurrency**: `errgroup.WithContext().SetLimit(N)` for bounded fan-out. Avoid raw goroutines in handlers.
- **SQL**: parameterized always. Migrations are inline in `storage.initSchema()` using `CREATE ... IF NOT EXISTS` (graphrag pattern). One file per logical migration if it grows.
- **Naming**: exported = `CamelCase`, package-private = `camelCase`. Receivers short (`s *Store`, `b *Backend`). No `IFoo`, no `FooImpl`.
- **Comments**: default to none. Add only when WHY is non-obvious (workaround, hidden invariant, surprising behavior). Never restate WHAT the code does. No file-header banners.
- **Tests**: table-driven. Use `:memory:` SQLite for storage tests. Fake `llm.Backend` for summarize/chat tests. Golden files in `testdata/` for extract.
- **HTTP handlers**: thin. Parse → call service → render template / JSON. No business logic in handlers.
- **Templates**: server-rendered, HTMX returns fragments (not full pages) for partial updates. One fragment per file under `web/templates/partials/`.
- **No emojis** in code, comments, or commit messages.

## Memory (Claude — what matters here)

When working in this repo, remember:

- **Mirror graphrag patterns** for LLM backend, SQLite setup, config loading. File pointers in `PLAN.md` §2 and §7.
- **Do NOT port the graph layer** from graphrag. Linklore is FTS5 + cosine + plain SQL. No PPR, no entity extraction, no graph traversal.
- **Default LLM target**: LiteLLM+vLLM on DGX (faster than Ollama per user). Ollama is the fallback. Backend selection lives in `configs/config.yaml` under `llm.backend`.
- **HTML cleaning happens before the LLM**, not after. Always `readability → html-to-markdown` first; never send raw HTML.
- **Images are links, not blobs.** Store `image_url` only. UI has a show/hide toggle.
- **Single-user, no auth.** Don't add login flows unless asked.

## Configuration

`configs/config.yaml` controls server addr, DB path, LLM backend (ollama|litellm), worker concurrency, and UI defaults. Env overrides are whitelisted: `LINKLORE_DB_PATH`, `LINKLORE_ADDR`, `OLLAMA_HOST`, `LINKLORE_LLM_BACKEND`, `LITELLM_BASE_URL`, `LITELLM_API_KEY`.

## Where the LLM is used

| Phase | What | Model | Required? |
|-------|------|-------|-----------|
| Ingest (per link) | Embedding (summary + content head) | nomic-embed-text | Yes |
| Ingest (per link) | TL;DR + auto-tags (JSON) | qwen3:4b (or vLLM equivalent) | Yes |
| Search | Query embedding | nomic-embed-text | Yes (for hybrid) |
| Chat | RAG answer (streaming) | qwen3:4b | Yes (for chat only) |
