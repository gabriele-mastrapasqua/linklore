# Linklore

[![test](https://github.com/gabriele-mastrapasqua/linklore/actions/workflows/test.yml/badge.svg)](https://github.com/gabriele-mastrapasqua/linklore/actions/workflows/test.yml)
[![release](https://github.com/gabriele-mastrapasqua/linklore/actions/workflows/release.yml/badge.svg)](https://github.com/gabriele-mastrapasqua/linklore/actions/workflows/release.yml)
[![coverage](https://img.shields.io/badge/coverage-75%25-brightgreen)](#tests)
[![go report](https://goreportcard.com/badge/github.com/gabriele-mastrapasqua/linklore)](https://goreportcard.com/report/github.com/gabriele-mastrapasqua/linklore)
[![go version](https://img.shields.io/badge/go-1.25%2B-00ADD8?logo=go)](go.mod)
[![license: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)
[![local-first](https://img.shields.io/badge/local--first-✓-success)](#local-first-privacy-first)

> **Bookmarks you actually own.** A local-first, single-binary library for
> the links you save — full-text + semantic search across everything,
> a private RAG chat grounded on your own content, four view modes,
> RSS/Atom subscriptions, dead-link checker, drag-and-drop. SQLite under
> the hood. No accounts. No SaaS. No telemetry.
>
> **One Go binary. One SQLite file.** No Node, no npm, no JS build
> chain, no Docker compose, no Postgres, no Redis. The whole thing
> is ~14 MB and starts in under a second.

![Linklore — grid view](docs/screenshots/00-hero.png)

> Screenshots in this README are taken from a curated public demo
> library — generate yours with `make seed-demo`.

---

## Local-first, in one paragraph

Your data is one SQLite file. Migration is `cp`. No accounts, no SaaS,
no telemetry. The LLM is optional and points at any OpenAI-compatible
server you run yourself (vLLM, llama.cpp, LM Studio, LiteLLM proxy,
Ollama via `/v1`) — or skip the LLM entirely and search falls back to
BM25. Binds to `127.0.0.1` by default; you decide if it ever leaves
the machine.

---

## Why

Browser bookmarks rot. Read-later apps lock your data behind an account.
Self-hosted bookmark managers ship a Docker compose, a Postgres, a
Redis, and a JS build chain.

Linklore is one Go binary and one SQLite file. Open it, paste a URL,
forget it. When you want to find something — type, or ask the AI in
plain language. When you want to leave — copy the SQLite file. That's
the migration.

---

## Features

- **Smart capture** — paste a link or RSS/Atom feed; the same field
  handles both. Bookmarklet + Netscape import/export.
- **Four views per collection** — list / grid / headlines /
  Pinterest-style moodboard, persisted server-side.
- **Hybrid search** — FTS5 + semantic (cosine on stored embeddings),
  with a clean BM25 fallback when no LLM is around.
- **RAG chat** with inline `[src:N]` citations and a sources rail that
  dims retrieved-but-unused chunks.
- **Per-link AI** — TL;DR + auto-tags on ingest. Optional; off by
  default.
- **Reader drawer** — in-place preview with size / width /
  light-sepia-dark.
- **Type filter** — article / video / image / audio / document / book
  chips per collection.
- **⌘K palette**, `j/k` keyboard nav, right-click menu, drag-and-drop
  reorder, light/dark/auto theme.
- **Duplicates view** with URL canonicalisation (strips `www.`,
  trailing slash, fragment, `utm_*`, 17 other tracker keys).

---

## Screenshots

The two views the project is built around: a Pinterest-style moodboard
of your saved links (the hero above), and a RAG chat that answers
questions in the language of your library and cites the exact source
chunk it used.

<table>
<tr>
<td colspan="2"><strong>Chat with your library</strong> — streaming answer with inline <code>[src:N]</code> citations. The sources rail on the right highlights the chunks the model actually used and dims the ones that were retrieved but irrelevant. Powered by hybrid FTS5 + embedding retrieval over your own SQLite.<br><img src="docs/screenshots/02-chat.png" alt="RAG chat with inline citations"></td>
</tr>
<tr>
<td width="50%"><strong>Collection — list view</strong><br>Favicon + LLM summary + tags + a single big hero image per row.<br><img src="docs/screenshots/03-collection.png" alt="Collection list view"></td>
<td width="50%"><strong>Reader drawer</strong><br>Slide-in preview with size / width / theme controls + a TL;DR card.<br><img src="docs/screenshots/04-drawer.png" alt="Reader drawer"></td>
</tr>
<tr>
<td width="50%"><strong>Type filter</strong><br>Per-collection chips: article / video / image / audio / document / book.<br><img src="docs/screenshots/07-types.png" alt="Type filter chips"></td>
<td width="50%"><strong>⌘K command palette</strong><br>Fuzzy-search every link, every collection, every page.<br><img src="docs/screenshots/05-palette.png" alt="Command palette"></td>
</tr>
</table>

---

## Install

### Pre-built binaries (recommended)

Grab a release archive from the
[releases page](https://github.com/gabriele-mastrapasqua/linklore/releases/latest)
for your OS/arch — `linux-amd64`, `linux-arm64`, `darwin-amd64`,
`darwin-arm64`, or `windows-amd64`. Each archive bundles the binary,
`README.md`, `LICENSE`, and `configs/config.yaml`.

```bash
# example: macOS arm64
curl -LO https://github.com/gabriele-mastrapasqua/linklore/releases/latest/download/linklore-<version>-darwin-arm64.tar.gz
tar -xzf linklore-*-darwin-arm64.tar.gz
./linklore-*-darwin-arm64 serve
```

Open `http://127.0.0.1:8080`. That's the whole onboarding.

### From source

You need **Go 1.25+** and `make`. An LLM is optional — Linklore boots
cleanly without one.

```bash
git clone git@github.com:gabriele-mastrapasqua/linklore.git
cd linklore
make build           # ./bin/linklore (~14 MB)
make run             # serves on http://127.0.0.1:8080
```

Want a local LLM (RAG chat + per-link TL;DR)? Two extra steps:

```bash
make env-template    # cp .env.example → .env (skips if .env exists)
$EDITOR .env         # uncomment the openai or ollama block
```

See **[Configuration](#configuration)** below for the env-var values.

### Dev loop

```bash
make dev             # reset DB + build + run (handy when iterating)
make test            # full suite, race-enabled, fts5 tag — what CI runs
make check           # fmt + vet + lint + test (run before commit)
make smoke           # spin up server, hit health + create paths, tear down
make seed-demo       # populate ./data/linklore-demo.db with curated public links
make help            # show every target
```

### CLI

```bash
./bin/linklore serve   [--config configs/config.yaml]
./bin/linklore add     -c <slug> <url>
./bin/linklore reindex
```

---

## Configuration

Linklore splits config across two files with **strict, non-overlapping
responsibilities** so a `git add .` can't ever leak a secret:

| File | Holds | Tracked in git? |
|---|---|---|
| `configs/config.yaml` | Non-secret tunables. | ✓ committed |
| `.env`                | Secrets + per-machine values: LLM endpoint, API key, model names. | gitignored |

The `/settings` page reads the live config and writes LLM changes back
to `.env` only — yaml is **never** touched by the UI save path.

### What's in `configs/config.yaml`

Edit when you want to change a defaults; otherwise ignore it.

```yaml
server.addr                 # default 127.0.0.1:8080
database.path               # default ./data/linklore.db
worker.{concurrency, embed_batch_size, fetch_timeout_seconds}
extract.{headless_fallback, archive_html, min_readable_chars}
chunking.{target_tokens, overlap_tokens, min_tokens}   # RAG knobs
tags.{max_per_link, active_cap, reuse_distance}        # auto-tag caps
ui.{show_images_default, reader_font, reader_width}    # cosmetic defaults
```

### Pick an LLM backend (or skip)

`make env-template` (or `cp .env.example .env`) then uncomment one of
the blocks below. **Use `openai` for any local LLM** — it's the
canonical choice and works with vLLM, llama.cpp, LM Studio, LiteLLM
proxy, OpenAI itself, and Ollama (via its `/v1` endpoint).

**Ollama via OpenAI-compatible API** (recommended even for Ollama):

```bash
ollama pull qwen3:14b
ollama pull nomic-embed-text
```

```ini
LINKLORE_LLM_BACKEND=openai
OPENAI_BASE_URL=http://localhost:11434/v1
OPENAI_API_KEY=ollama        # any non-empty string
LINKLORE_LLM_MODEL=qwen3:14b
LINKLORE_LLM_EMBED_MODEL=nomic-embed-text
```

**Other OpenAI-compatible servers** (vLLM, llama.cpp, LiteLLM, …):
just point `OPENAI_BASE_URL` at the right port. Same shape.

**Ollama native API** (legacy — only if you need Ollama options that
aren't on `/v1`):

```ini
LINKLORE_LLM_BACKEND=ollama
OLLAMA_HOST=http://localhost:11434
LINKLORE_LLM_MODEL=qwen3:14b
LINKLORE_LLM_EMBED_MODEL=nomic-embed-text
```

**No LLM** (default — search degrades to BM25, chat shows a friendly
disabled banner, ingestion still fetches and extracts):

```ini
LINKLORE_LLM_BACKEND=none
```

> **Deprecated**: `LINKLORE_LLM_BACKEND=litellm`, `LITELLM_BASE_URL`,
> `LITELLM_API_KEY` still work as aliases — they're silently rewritten
> to the canonical `openai` / `OPENAI_*` names at startup.

### Precedence

```
process env  >  .env  >  configs/config.yaml  >  built-in defaults
```

A one-shot override always wins:

```bash
LINKLORE_LLM_BACKEND=none ./bin/linklore serve
```

### All env vars

```
LINKLORE_LLM_BACKEND          none | openai | ollama   (litellm = openai alias)
LINKLORE_LLM_MODEL            chat/summary model
LINKLORE_LLM_EMBED_MODEL      embedding model
OPENAI_BASE_URL               OpenAI-compatible base URL (canonical)
OPENAI_API_KEY                bearer token (any non-empty for local servers)
OLLAMA_HOST                   Ollama daemon URL (native /api/* path)
LINKLORE_ADDR                 server bind address
LINKLORE_DB_PATH              SQLite path
LINKLORE_WORKER_CONCURRENCY   parallel ingest workers

# Deprecated aliases (still work, mapped to the canonical names above):
LITELLM_BASE_URL              → OPENAI_BASE_URL
LITELLM_API_KEY               → OPENAI_API_KEY
LINKLORE_LLM_BACKEND=litellm  → openai
```

---

## Keyboard shortcuts

| Key | Action |
|---|---|
| `⌘K` / `Ctrl+K` | Command palette |
| `j` / `↓`        | Next card |
| `k` / `↑`        | Previous card |
| `↵`              | Open in preview drawer |
| `x`              | Toggle bulk-selection on focused card |
| `del`            | Delete focused card |
| `/`              | Focus the search |
| `?`              | Show shortcut overlay |
| `esc`            | Dismiss overlay / drawer / clear selection |
| Right-click row  | Context menu (Preview / Open / Copy URL / ✦ Ask / Delete) |

---

## Tests

```bash
make test       # full suite, -race, fts5 tag, count=1 — what CI runs
make cover      # writes coverage.html + prints total
make cover-pkg  # per-package summary, no HTML
```

Current total: ~75% across 23 packages. The 0%-coverage areas are
the `runServe` boot orchestration (subprocess-only) and the LLM
client wrappers that are exercised end-to-end via `make smoke`.

---

## Docs

| Topic | Where |
|-------|-------|
| Architecture, package map, stack | [`docs/architecture.md`](docs/architecture.md) |
| The four view modes | [`docs/views.md`](docs/views.md) |
| Search engine and popover | [`docs/search.md`](docs/search.md) |
| Preview drawer + 5 tabs | [`docs/preview.md`](docs/preview.md) |
| Responsive layout (breakpoints, off-canvas) | [`docs/responsive.md`](docs/responsive.md) |
| Drag-and-drop chip + insertion bar | [`docs/dnd.md`](docs/dnd.md) |
| Keyboard shortcuts | [`docs/keyboard.md`](docs/keyboard.md) |
| LLM modes (`none` / `openai` / `ollama`) | [`docs/llm-modes.md`](docs/llm-modes.md) |

---

## Contributing

Personal project, but PRs that fit the spirit (local-first,
single-user, no SaaS, no JS build) are welcome. Package layout and
stack notes live in [`docs/architecture.md`](docs/architecture.md).

1. `make check` — fmt + vet + lint + race test.
2. New features go behind a config flag if their cost is non-trivial.
3. New tests for new features. Bar: "could a regression slip past the
   existing suite?" If yes, add the test.

---

## License

MIT. See [`LICENSE`](LICENSE).

Linklore is named after Linkjam, the proto-bookmark-manager you would
have built if you'd stayed up one Saturday in 2008.
