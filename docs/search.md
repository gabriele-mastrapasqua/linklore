# Search

Linklore's search is **plain-text BM25 over a SQLite FTS5 index** plus
optional cosine re-rank when an embedding model is configured. The
query supports a small **facet syntax** (`tag:`, `kind:`, `in:`,
`-tag:`) on top of free text — facets are extracted and applied as
post-filters on the BM25 hits.

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

## Facet syntax

These tokens are parsed out of the query string, applied as filters,
and rendered as chips on the result header so the user sees what was
understood:

| Token | Effect | Example |
|-------|--------|---------|
| `tag:foo` | Result must carry tag with slug or display name `foo` | `react tag:hooks` |
| `-tag:foo` | Result must NOT carry that tag | `react -tag:legacy` |
| `kind:video` | Result's kind must be `video` (one of `article`, `video`, `image`, `audio`, `document`, `book`) | `kind:video tutorial` |
| `in:title` | Hint that the user only wants title matches (currently a UI hint; not yet enforced at the FTS level) | `in:title react` |
| `in:url` | Same as above for URL | `in:url localhost` |

Multiple facets compose with AND. Facets are extracted from the
residual text by the `search.ParseFacets` helper before BM25 runs;
the leftover text is what FTS sees. So `react tag:hooks
kind:article` searches FTS for "react" and post-filters down to
links tagged `hooks` with kind `article`.

Implementation: `internal/search/facets.go`. Filters are applied in
Go after FTS returns hits. At linklore's single-user scale (low tens
of thousands of links max) this is cheap and avoids growing the SQL
surface.

## Sort and scope

`/search` accepts two extra query params:

- `sort=relevance` (default) | `sort=date` — date order surfaces the
  most-recently-saved link first, useful for re-finding something you
  saved earlier today.
- `scope=<collection-slug>` — when set, the search runs only inside
  that collection. The topbar input on a `/c/<slug>` page passes the
  scope automatically; the popover offers "Search everywhere" as a
  one-click pivot.

## What's deliberately NOT supported

- **Boolean operators** (`AND` / `OR` / `NOT`). Plain `apple banana`
  already runs as an FTS5 AND under the hood. Quoting (`"red kettle"`)
  does work and is passed through to FTS5.
- **Range facets** (`created:>2026-01-01`, `before:`, `after:`).
  Could be added; not yet on anyone's hot path.
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
