# Linklore

[![test](https://github.com/gabrielemastrapasqua/linklore/actions/workflows/test.yml/badge.svg)](https://github.com/gabrielemastrapasqua/linklore/actions/workflows/test.yml)
[![release](https://github.com/gabrielemastrapasqua/linklore/actions/workflows/release.yml/badge.svg)](https://github.com/gabrielemastrapasqua/linklore/actions/workflows/release.yml)
[![coverage](https://img.shields.io/badge/coverage-75.2%25-brightgreen)](#tests--coverage)
[![go report](https://goreportcard.com/badge/github.com/gabrielemastrapasqua/linklore)](https://goreportcard.com/report/github.com/gabrielemastrapasqua/linklore)
[![go version](https://img.shields.io/badge/go-1.25%2B-00ADD8?logo=go)](go.mod)
[![license: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)
[![local-first](https://img.shields.io/badge/local--first-✓-success)](#privacy)

A local-first bookmark manager you actually own. Save links into collections,
read them in a calm reader pane, search across the whole library with
full-text + semantic search, and chat with a local LLM that grounds its
answers on your saved content.

Single binary, single SQLite file. No accounts, no SaaS, no telemetry.

> **Status**: in active development. Schema is stable, every feature listed
> below works today. See `PLAN.md` for the roadmap and §13 for the UI/UX
> upgrade plan that's currently being executed.

---

## Features

### Capture
- **Smart-add input** — paste a page URL or an RSS/Atom feed URL into the
  same field; linklore figures out which one it is and either creates a
  link or subscribes the collection to the feed.
- **RSS/Atom subscription per collection** with auto-discovery from a
  homepage URL (probes `<link rel="alternate">` and the well-known paths).
- **Netscape Bookmark File** import + export — round-trips with Chrome,
  Firefox, Safari, Edge, Pocket, Pinboard, Raindrop.
- **Bookmarklet** at `/bookmarklet` that POSTs to `/api/links`.

### Organize
- **Collections + tags** with drag-and-drop reorder.
- **Drag-and-drop** — drop a row onto a sidebar collection to move it
  across; insertion bar previews the destination index (vertical bar
  in grid layout, horizontal in list/headlines/moodboard).
- **Bulk select toolbar** — checkbox per row, sticky bar with
  Move-to-collection and Delete; or click anywhere on a row body to
  toggle.
- **Sidebar `+` shortcut** next to the "Collections" header — jumps
  straight to the create form, focused.
- **Four view modes** per collection: list / grid / headlines / moodboard
  (Pinterest-style masonry). Persisted server-side.
- **Density toggles** — show/hide titles / summaries / badges. Saved per
  browser via localStorage.
- **Type classifier** on ingest — article / video / image / audio /
  document / book — colour-tinted icon next to the title and a per-page
  filter chip.
- **Per-collection cover image** — paste a URL, render as a 140px banner;
  also tints the collection card on the home page.
- **Duplicates view** at `/duplicates` with URL canonicalisation
  (drops `www.`, trailing slash, fragment, `utm_*`, `fbclid`, `gclid`,
  and 14 other tracker keys).
- **Prune empty collections** in one click on the home page.

### Read
- **Right-pane preview drawer** — clicking a row opens the article
  inline (slide-in from the right). The standalone `/links/:id` page
  is still the deep link.
- **Reader controls** in the drawer: font size S/M/L, width
  narrow/medium/wide, theme light/sepia/dark. Persisted.
- **Reader mode** at `/links/:id/read` — markdown rendered + sanitised
  via bluemonday.

### Search + AI
- **Full-text search (FTS5)** across title, description, summary, and
  extracted markdown body.
- **Semantic search** via embeddings (BLOB-stored `[]float32`, cosine in
  Go). Optional — falls back to BM25-only when no LLM is configured.
- **RAG chat** at `/chat` — streams answers grounded on retrieved
  chunks from your library, with citations linking back to source links.
- **Per-link LLM TL;DR + auto-tags** generated on ingest. Auto-tags
  reuse existing tag slugs (Levenshtein dedupe, capped at 5/link).
- **Right-click "Ask about this"** opens chat with the link's title
  prefilled into the question.
- **Canned-prompt chips** above the chat input.

### UX
- **Right-click context menu** on every row: Preview, Open original,
  Open detail, Copy URL (toast confirms), ✦ Ask, Toggle selection,
  Delete.
- **⌘K command palette** — fuzzy-filter every collection + nav item.
  Prefix `?` routes to the LLM. Empty match offers a search shortcut.
- **Keyboard shortcuts**: `j/k` walk cards, `↵` open, `x` toggle
  selection, `del` delete, `/` focus search, `?` show overlay.
- **Sticky filter bar** with backdrop-blur stays under the topbar
  while scrolling.
- **Toasts** for every bulk action (delete N / move N / pruned N /
  imported N).
- **Worker activity dot** in the topbar — pulsing violet when
  background work is in flight, solid grey when idle.
- **Inter** for UI, **JetBrains Mono** for URLs and code.
- **Light / dark / auto** theme toggle.

### Privacy
- **Local-first by design.** The SQLite file at `./data/linklore.db` is
  the source of truth — back it up via Syncthing / iCloud Drive / scp
  / `sqlite3 .backup`. No cross-device sync built into the app.
- **No telemetry.** Linklore makes outbound HTTP requests only to (1)
  the URL the user pastes (extraction), (2) the LLM backend the user
  configured, (3) RSS/Atom feeds the user subscribes to.
- **No auth, no accounts.** Single-user assumption.

---

## Quick start

### Requirements
- **Go 1.25+** with the `sqlite_fts5` build tag (the Makefile sets this
  for you).
- **An LLM backend** — optional. Linklore boots cleanly without one;
  search and chat degrade gracefully.

### Build + run

```bash
git clone https://github.com/gabrielemastrapasqua/linklore.git
cd linklore
make build               # builds ./bin/linklore (~16 MB)
./bin/linklore serve     # listens on 127.0.0.1:8080 by default
```

Or just:

```bash
make run                 # equivalent to: go run -tags=sqlite_fts5 ./cmd/linklore serve
```

Open http://127.0.0.1:8080 — that's the whole onboarding.

### CLI

```bash
linklore serve   [--config configs/config.yaml]
linklore add     -c <slug> <url>            # ingest from the command line
linklore reindex                            # stub — re-runs summary+embed
```

---

## Configuring the LLM

The shipped `configs/config.yaml` defaults to `backend: "none"` so a
fresh `go install` boots cleanly without trying to reach any server.
You opt in to a backend by editing the file or by passing a different
config path with `--config`.

### Easy mode: Ollama on localhost

There's a ready-made example at `configs/config.example.yaml`. Two
shell commands to a running summary + chat pipeline:

```bash
ollama pull qwen3:14b
ollama pull nomic-embed-text
cp configs/config.example.yaml configs/config.yaml
./bin/linklore serve
```

The full file (annotated) — copy this verbatim and adjust model names
if you've pulled different ones:

```yaml
server:
  addr: "127.0.0.1:8080"

database:
  path: "./data/linklore.db"

llm:
  backend: "ollama"

  ollama:
    host: "http://localhost:11434"
    model: "qwen3:14b"               # used for summary + chat
    embed_model: "nomic-embed-text"  # used for semantic search
    num_ctx: 32768
    timeout_seconds: 600
```

### LiteLLM / OpenAI-compatible gateway

```yaml
llm:
  backend: "litellm"
  litellm:
    base_url: "http://localhost:4000/v1"
    model: "qwen3:14b"
    embed_model: "nomic-embed-text"
    api_key: "${LITELLM_API_KEY}"   # expanded from process env or .env
```

Any server that speaks `/chat/completions` + `/embeddings` works:
LiteLLM itself, llama.cpp's `server`, vLLM with the OpenAI shim,
etc. Authorisation header is set only when `api_key` is non-empty,
so local proxies that don't require one work too.

### Backend options at a glance

| `llm.backend` | Use case |
|---|---|
| `none`     | No LLM. Search degrades to BM25-only, chat is disabled, ingestion still fetches + extracts. |
| `ollama`   | Local Ollama (`OLLAMA_HOST` env override; default `http://localhost:11434`). Uses the `/api/embed` endpoint (Ollama 0.2+). |
| `litellm`  | Any OpenAI-compatible gateway. Standard `/chat/completions` + `/embeddings` endpoints. |

### Environment overrides

```
LINKLORE_ADDR          override server.addr
LINKLORE_DB_PATH       override database.path
LINKLORE_LLM_BACKEND   override llm.backend
LINKLORE_LLM_MODEL     override llm.<backend>.model
LINKLORE_LLM_EMBED_MODEL override llm.<backend>.embed_model
OLLAMA_HOST            override llm.ollama.host
LITELLM_BASE_URL       override llm.litellm.base_url
LITELLM_API_KEY        override llm.litellm.api_key
LINKLORE_WORKER_CONCURRENCY  override worker.concurrency
```

A `.env` file at the project root (or alongside `config.yaml`) is also
loaded. Process env wins over `.env`.

### Running with no LLM

```bash
LINKLORE_LLM_BACKEND=none ./bin/linklore serve
```

The whole UI stays usable; chat shows a "Chat is disabled" banner with
a hint pointing at the right config field.

---

## Keyboard shortcuts

| Key | Action |
|---|---|
| `⌘K` / `Ctrl+K`     | Open command palette |
| `j` / `↓`           | Next card |
| `k` / `↑`           | Previous card |
| `↵`                 | Open focused card in preview drawer |
| `x`                 | Toggle bulk-selection on the focused card |
| `del`               | Delete the focused card |
| `/`                 | Focus the topbar search |
| `?`                 | Show the shortcut overlay |
| `esc`               | Dismiss overlay / drawer / clear selection |
| Right-click on row  | Context menu (Preview, Open original, Copy URL, ✦ Ask, Delete, …) |

---

## Architecture

```
cmd/linklore/        single binary: serve | add | reindex
internal/
  archive/           gzipped raw-HTML snapshots
  chat/              RAG context builder + streaming SSE
  chunking/          paragraph + heading chunker
  classify/          URL → article|video|image|audio|document|book
  config/            YAML + env override loader
  embed/             []float32 ↔ BLOB encode/decode + cosine
  events/            in-process pub/sub for SSE
  extract/           HTTP fetch + readability + html→md
  feed/              outbound Atom export per collection
  feedimport/        gofeed-based RSS/Atom importer + auto-discover
  lang/              language detection
  llm/               Backend interface; ollama/, litellm/, fake/ subdirs
  netscape/          Netscape Bookmark File reader/writer
  reader/            content_md → sanitised HTML for the reader
  search/            FTS5 + cosine hybrid ranking
  server/            http.ServeMux + handlers + html/template
  storage/           SQLite WAL + FTS5 + embeddings (BLOB) + migrations
  summarize/         LLM TL;DR + auto-tags JSON pipeline
  tags/              slugify, dedupe, normalisation
  urlnorm/           URL canonicalisation for duplicate detection
  worker/            background fetch / extract / summary / embed queue
web/
  templates/         html/template — base.html + per-page + partials/
  static/            app.css + handful of small JS files (no build step)
configs/config.yaml
data/linklore.db     created on first boot
```

Everything is stdlib + a small list of third-party libraries documented
in `PLAN.md` §2. No JavaScript build chain; no SaaS dependencies.

---

## Tests + coverage

```bash
make test          # full suite, race-enabled, fts5 tag
make test-fast     # same minus -race
make cover         # writes coverage.html + prints total line
make cover-pkg     # per-package summary, no HTML
```

Current total: **75.2% of statements** across 23 packages. The new
feature code (`urlnorm`, `netscape`, `classify`, server handlers,
storage) sits in the 67–100% band; the only 0%-coverage area is
`runServe` boot orchestration that requires subprocess testing
(see `cmd/linklore/main_test.go` for the existing subprocess
checks).

---

## Project layout decisions

- **HTMX + a few tiny JS files** (no React, no build pipeline). Each JS
  file is < 200 LoC and self-contained: `bulk.js` for multi-select,
  `views.js` for layout/density, `keys.js` for keyboard nav,
  `drawer.js` for the preview pane, `palette.js` for ⌘K, `ctxmenu.js`
  for right-click, `toasts.js` for feedback, `dnd.js` for drag-drop,
  `events.js` for SSE, `lightbox.js` for images.
- **stdlib `net/http.ServeMux`** (Go 1.22+). No router framework.
- **`html/template`** for both pages and HTMX fragments. Custom
  `dict` + `list` template helpers; no other helpers.
- **SQLite via `mattn/go-sqlite3`** with FTS5 + WAL. Embeddings as
  `[]float32` little-endian BLOB; cosine in Go is fast enough below
  ~50k chunks.
- **Plain CSS** with `:root` custom properties for theming. Three
  scales (`--space-*`, `--radius-*`, `--shadow-*`) drive every
  component.

---

## Contributing

This is a personal project, but PRs that fit the spirit
(local-first, single-user, no SaaS, no JS build) are welcome.

1. `make check` — fmt + vet + lint + race test.
2. New features go behind a config flag if their cost is non-trivial.
3. New tests for new features. The bar is "can a regression slip past
   the existing test suite?" — if yes, write the test.

---

## Licence

MIT. See `LICENCE`.

Linklore is named after Linkjam, the proto-bookmark-manager you would
have built if you'd stayed up one Saturday in 2008.
