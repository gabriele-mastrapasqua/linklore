# Search

Linklore's search is **plain-text BM25 over a SQLite FTS5 index** plus
optional cosine re-rank when an embedding model is configured. There is
no facet syntax (`type:article`, `tag:foo`, etc.) at the query level —
you express those filters by clicking a kind chip or a tag, not by
typing them. The query string is treated as text.

## What gets indexed

Two FTS5 virtual tables: `links_fts` (title, description, URL, tags)
and `chunks_fts` (extracted markdown split into ~500-token chunks).
A search runs both and unions the hits. Tag-prefix matches are added
on top with a small synthetic BM25 so a saved link tagged `go` shows
up when you type `go`.

## How a query is processed

1. **`SearchLinks(q)`** in `internal/search/search.go` sanitises the
   query (drops FTS5 punctuation), then queries:
   - `links_fts` (title/desc/URL/tags fields)
   - `chunks_fts` (article body)
   - `SearchLinksByTagPrefix(q)` (tag slug or name starts with q)
2. The three result sets are unioned by link ID, taking the best
   (lowest) BM25 per link.
3. If an embedding model is configured, the top N candidates are
   re-ranked by cosine similarity between the query embedding and the
   chunk embeddings. Without one, the BM25 ranking is the final order.

## Surfaces

- **Topbar input** — focus opens a popover. The popover is served by
  `GET /search/suggest` and shows: matched collections (substring on
  name or slug), matched tags (prefix on slug or name), and a "Search
  ↵" entry that submits the form to `/search`. Press `↵` to run the
  full query against the FTS index.
- **/search page** — full results, 20 per page. Uses the same backend
  as the popover entry.
- **Command palette (⌘K)** — see `web/static/palette.js`. Calls
  `/search/live` directly.

## Score breakdown shown in the UI

Each search result row shows a `score X.XX` badge — that's the final
hybrid score (BM25 squashed, plus the re-rank delta). Lower BM25 →
higher final score. Useful when triaging "why did this hit come up
first?"

## What's deliberately NOT supported

- **Facet syntax in the query** (`tag:`, `kind:`, `before:`). These
  are filters, not queries — the UI surfaces them as chips on the
  collection page.
- **Boolean operators**. Plain `apple banana` already runs as an FTS5
  AND under the hood. Quoting (`"red kettle"`) does work and is passed
  through to FTS5.
- **Cross-tenant search**. Linklore is single-user (CLAUDE.md, hard
  rule); there's no concept of shared scopes.

## Source code

- Engine: `internal/search/search.go`
- Storage: `internal/storage/storage.go` (`SearchLinksFTS`,
  `SearchChunksFTS`, `SearchLinksByTagPrefix`, `SearchTagsByPrefix`)
- HTTP: `internal/server/server.go` — `handleSearchPage`,
  `handleSearchLive`, `handleSearchSuggest`
- Templates: `web/templates/search.html`,
  `web/templates/partials/search_results.html`,
  `web/templates/partials/search_suggest.html`
