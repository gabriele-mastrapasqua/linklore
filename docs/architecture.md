# Architecture

## Project layout

```
cmd/linklore/        single binary: serve | add | reindex
internal/
  archive/           gzipped raw-HTML snapshots
  chat/              RAG context builder + streaming SSE
  chunking/          paragraph + heading chunker
  classify/          URL -> article|video|image|audio|document|book
  config/            yaml + env override loader (LLM = env-only)
  embed/             []float32 <-> BLOB encode + cosine in Go
  events/            in-process pub/sub for SSE
  extract/           HTTP fetch + readability + html->md
  feed/              outbound Atom export per collection
  feedimport/        gofeed-based RSS/Atom importer + auto-discover
  llm/               Backend interface: openai (canonical), ollama (legacy native), fake
  netscape/          Netscape Bookmark File reader/writer
  reader/            content_md -> sanitised HTML
  search/            FTS5 + cosine hybrid ranking
  server/            http.ServeMux + handlers + html/template
  storage/           SQLite WAL + FTS5 + embeddings (BLOB) + migrations
  summarize/         LLM TL;DR + auto-tags JSON pipeline
  tags/              slugify, dedupe, normalisation
  urlnorm/           URL canonicalisation for duplicate detection
  worker/            background fetch / extract / summary / embed queue
web/
  templates/         html/template - base.html + per-page + partials/
  static/            app.css + small JS files (no build step)
configs/config.yaml  non-secret tunables
.env                 LLM endpoint + secrets (gitignored)
data/linklore.db     created on first boot
```

## Stack

Go stdlib `net/http.ServeMux` (Go 1.22+ pattern routing), `html/template`,
SQLite via `mattn/go-sqlite3` with FTS5 + WAL, HTMX + a handful of small
JS modules under `web/static/`. No Node, no npm, no JS build chain.
