# Phase 8 — UI plan (sidebar, theme, DnD, LLM-optional)

Themes a few items from "nice to have" up to "now bothering". Each section
ends with a concrete TODO list and a test note.

---

## 1. Sidebar with collections list

Goal: a left rail listing every collection so navigating is one click,
not "back to home → click card → enter". Active collection highlighted.

### Design
- `<aside class="sidebar">` injected from `base.html`. Visible on every
  page except `/links/{id}/read` (reader mode stays distraction-free).
- Order: alphabetical by name; counts (total / processing / failed) as
  a small badge per row, server-rendered (no extra round trip).
- Mobile: collapses to a top-bar dropdown (CSS-only `<details>`).
- Active route is detected by comparing `r.URL.Path` with `/c/{slug}`
  in the renderer wrapper.

### TODOs
- [ ] `internal/server`: extend `renderPageRq` to inject `Sidebar` data
  (`[]CollectionStat` + `ActiveSlug`).
- [ ] `web/templates/partials/sidebar.html`: list with counters; active
  link gets a CSS class.
- [ ] `web/templates/base.html`: two-column layout (`<aside>` + `<main>`),
  responsive (stacks under 720px).
- [ ] `web/static/app.css`: `.sidebar`, `.sidebar a.active`, mobile
  collapse via `<details>`.
- [ ] Keep the topbar; sidebar is for navigation, topbar for global
  actions (search/chat/tags/+/preview-toggle/theme-toggle).

### Tests
- [ ] `TestSidebar_listsActiveCollection`: GET `/c/foo` → body contains
  the sidebar entry for "foo" with the `active` class and the right
  counter ("3 links · 1 processing").
- [ ] `TestSidebar_hiddenInReaderMode`: GET `/links/{id}/read` does NOT
  contain `<aside class="sidebar">`.

---

## 2. Dark / light theme with persisted preference

Goal: toggle in the topbar; persists per user. Default = system
preference (`prefers-color-scheme`); explicit choice overrides.

### Design
- One row in a tiny `preferences` key/value table (already paying for
  it with `show_previews` cookie, but server-side persistence avoids
  losing the choice on cookie purge):

  ```sql
  CREATE TABLE preferences (
      key   TEXT PRIMARY KEY,
      value TEXT NOT NULL,
      updated_at INTEGER NOT NULL
  );
  ```

  Single-user app, no need for per-user scoping yet.
- CSS variables already drive the palette (`--gap`, `--border`, `--bg`,
  `--accent` etc). Add a `[data-theme="dark"]` block on `:root` /
  `body` that overrides the values. No JS to flip variables — just a
  cookie + body attribute.
- Cookie `theme=light|dark|auto` (default `auto`); the server reads it
  in `renderPageRq` and writes `data-theme` on `<html>` so first paint
  is correct.

### TODOs
- [ ] `internal/storage`: `preferences` table + `GetPref(key) (string,
  err)` / `SetPref(key, value)` / `ListPrefs`. Migration via the
  idempotent `addColumns` pattern is fine; preferences is new so a
  plain `CREATE TABLE IF NOT EXISTS` works.
- [ ] `internal/server`: `themeFromRequest(r) string` reading the
  `theme` cookie with `auto` default. Inject into page data as `Theme`.
- [ ] `POST /preferences/theme` flips the cookie + writes to the
  `preferences` table (so it survives even if cookies are cleared).
- [ ] `base.html`: `<html data-theme="{{.Theme}}">`, topbar button
  cycles through `auto → light → dark → auto`.
- [ ] `app.css`: variables for dark mode behind
  `:root[data-theme="dark"]`, plus `@media (prefers-color-scheme:
  dark) { :root[data-theme="auto"] { … } }` so `auto` follows system.

### Tests
- [ ] `TestTheme_defaultIsAuto`: GET `/` returns `data-theme="auto"`.
- [ ] `TestTheme_cookieFlipsAndPersists`: POST toggle, GET back returns
  `data-theme="dark"`, AND `prefs.theme=dark` row in the DB.
- [ ] `TestStorage_PrefsRoundtrip`: set/get/overwrite.

---

## 3. Move a link to another collection

Two surfaces:

- **Per-link "move to…"** menu: dropdown of collections; on submit,
  `POST /links/{id}/move` with `collection_id`.
- **Drag and drop** (optional, see §6) for muscle memory.

### Design
- The dropdown is a tiny `<form>` rendered inline in the link row when
  the user clicks "more ⋯". HTMX swap replaces the row in place; the
  source page re-renders the row with the new state. No JS needed for
  the dropdown alone.
- Storage already supports it (just an `UPDATE links SET collection_id
  = ?`).

### TODOs
- [ ] `internal/storage`: `MoveLink(ctx, linkID, newCollectionID) error`
  — single UPDATE, refuses to move to an unknown collection.
- [ ] `internal/server`:
  - `POST /links/{id}/move` (form-encoded `collection_id`), returns
    the freshly-rendered `link_row` fragment OR `204` when the user is
    on the page that no longer contains the link.
  - HTMX-aware: if the request comes from the source collection page,
    swap the row out (`<div>` removed).
- [ ] `link_row.html`: small "⋯" menu opening an HTMX `<form>` with
  `<select>` of collections.
- [ ] `link_detail.html`: add the same "move to" form below the actions.

### Tests
- [ ] `TestStorage_MoveLink`: link reappears in the new collection,
  disappears from the old; foreign-key cascade still works.
- [ ] `TestStorage_MoveLink_unknownDestRejects`.
- [ ] `TestServer_handleMoveLink_returnsRowFragment`.

---

## 4. Counter / "no links yet" / "X processing" not refreshing after add

Bug: when the user adds a link, the row is appended via HTMX swap, but
the **counters in the page header** (e.g. "0 links · 0 processing") and
the **"No links yet" empty-state placeholder** stay stale until F5.

### Root cause
`POST /c/{slug}/links` returns only the `link_row` partial. The
counters (`{{.Stats.Total}}` etc.) live in a separate block and are
rendered server-side once, on page load.

### Fix options
- **A — out-of-band swap**: include an OOB block in the response that
  re-renders the counters card. HTMX picks it up automatically.
- **B — header self-poll**: the counter card polls `GET
  /c/{slug}/stats` every 2s while there's any in-progress link. Same
  approach already used by `link_header`.

Option A is the right default because it's instant and silent; option
B is the safety net for status flips happening on the worker tick.

### TODOs
- [ ] `web/templates/partials/collection_stats.html`: the counter card
  + "No links yet" hint, indexed by collection. Renders alone.
- [ ] `links.html`: include `{{template "collection_stats" .}}` as the
  card the page already shows + `id="collection-stats-{{.Collection.ID}}"`
  for HTMX targeting.
- [ ] `POST /c/{slug}/links`: response now contains
  `link_row` followed by `<div hx-swap-oob="outerHTML"
  id="collection-stats-…">…rerendered stats…</div>`.
- [ ] Add `GET /c/{slug}/stats` returning just the partial; the stats
  block self-polls every 2s while `InProgress > 0`. Stops when
  everything is `summarized` or `failed`.
- [ ] Same OOB pattern on `DELETE /links/{id}` (counters drop) and on
  `POST /links/{id}/move` (counters move between collections).

### Tests
- [ ] `TestServer_addLink_sendsOOBStats`: response body contains
  `hx-swap-oob` for the stats block, with the new total.
- [ ] `TestServer_collectionStats_pollsTillIdle`: stats partial carries
  `hx-trigger="every 2s"` while `InProgress > 0`, drops it when 0.
- [ ] `TestServer_deleteLink_recountsViaOOB`.

---

## 5. Search "no links found" while inside a collection

Bug: live search-bar inside `/c/{slug}` sometimes shows "No matches"
while rows are clearly present in the same page. Likely the
sanitiser/prefix logic is fine globally but the live-search handler
isn't scoped to the active collection (or the candidate set is too
small).

### TODOs
- [ ] Reproduce: add 2 links in a fresh collection, type a known word,
  observe whether `/search/live` returns rows. Capture the actual
  request payload (browser devtools / server log).
- [ ] Verify `SearchLinksFTS` runs against the right candidate set —
  currently it ignores `collection_id` filter (server-side scoping
  applies only to `chunks_fts`). Decide:
  - keep it global and rely on the search page filter, or
  - add an optional `collectionID > 0` clause to `SearchLinksFTS`.
- [ ] Add a guard in `runSearch` that, on empty result, retries with
  the raw (un-prefixed) terms when the prefixed query yielded zero —
  some FTS5 builds choke on `*` when `tokenize=unicode61` strips
  trailing punctuation differently.
- [ ] If the bug is template-side: ensure live-search returns
  `Results: nil` only when the engine actually returned empty, not
  when the engine errored (we currently log + drop).

### Tests
- [ ] `TestServer_searchLive_findsJustAddedLink`: POST /collections,
  POST a link, GET /search/live?q=<word from URL> → results contain it.
- [ ] `TestEngine_searchLinks_emptyOnUnknownTerm_butNotOnKnownPrefix`:
  guard against the regression class.

---

## 6. Drag & drop (minimal)

Goal: a la Todoist — drag a link card up/down within a collection to
reorder, OR drag onto a sidebar collection to move.

### Constraints
- "minimal, alpine.js at most, CDN" — no React, no Sortable.js bundle.
  Native HTML5 DnD is the baseline; Alpine only used to wire the
  event listeners cleanly.
- Persist order only when it actually moves. Reordering writes to a
  new `links.order_idx` column (sparse, REAL so we can insert
  in-between without renumbering).

### TODOs
- [ ] `internal/storage`: `links.order_idx REAL DEFAULT 0` (idempotent
  ALTER). Default ordering goes from `created_at DESC` to `order_idx
  DESC, created_at DESC` so unmodified collections keep current order.
- [ ] `MoveLinkOrder(ctx, linkID, newOrder float64)` (the caller picks
  a midpoint between neighbours — no renumber).
- [ ] `MoveLink(ctx, linkID, newCollectionID, newOrder float64)`
  unifies §3 + DnD-cross-collection.
- [ ] `link_row.html` becomes `draggable="true"`, with
  `data-link-id` + `data-collection-id` attributes.
- [ ] `web/static/dnd.js` (10–30 lines, no Alpine if we can avoid it):
  - `dragstart` sets `dataTransfer` with the link id.
  - `dragover` on a row: visually shows the insertion bar (CSS class).
  - `drop` on a row → `fetch('/links/{id}/move', {…order_idx:…})`.
  - `drop` on a sidebar entry → `fetch('/links/{id}/move',
    {…collection_id:…})`.
- [ ] Consider Alpine.js (~15 KB CDN) only if vanilla becomes
  unreadable.
- [ ] Touch fallback: `<select>` "move to position…" hidden in the ⋯
  menu, so iOS users without a mouse aren't stuck.

### Tests
- [ ] `TestStorage_MoveLinkOrder_within`: order swap reflected on next
  list call.
- [ ] `TestStorage_MoveLinkOrder_crossCollection`: collection_id +
  order_idx both updated atomically.
- [ ] `TestServer_handleMove_acceptsOrderIdx`.

---

## 7. LLM-optional graceful degradation

Goal: linklore must work as a plain bookmark manager even if the LLM
gateway is unreachable / not configured.

### What works without an LLM today
- Add link → fetch → readability → markdown → save (works).
- Search BM25 only (works).
- Read-mode (works).

### What breaks today
- Auto-summary, auto-tags: the worker spirals on auth/network errors;
  the link gets stuck in `failed` if `isPermanentLLMError` triggers,
  or in `fetched` indefinitely if not.
- Chat: handler returns 503.

### Design
- Treat "no LLM configured" as a first-class state, not an error.
  Worker checks backend health on startup (single `/v1/models` GET);
  if it fails or no backend is configured, sets `s.llmHealthy=false`.
- When `!llmHealthy`:
  - Worker still does fetch+extract → status `fetched`.
  - Worker DOES NOT attempt summary / embed.
  - UI shows a small banner on the link detail: "no summary yet —
    LLM not configured. Configure `LITELLM_API_KEY` / `litellm.base_url`
    in config and click 'Generate summary'."
  - "Generate summary" button on each link's detail page → `POST
    /links/{id}/summarize` triggers a one-shot ProcessOne with a
    fresh health probe; success → status flips to `summarized`.
- Periodic re-probe (every 60s) so adding the key without restart
  brings the worker back online automatically.

### TODOs
- [ ] `internal/llm`: small `Backend.Healthcheck(ctx) error` method on
  the interface (LiteLLM hits `/v1/models`, Ollama hits `/api/tags`).
- [ ] `internal/worker`: track `lastHealthErr`, skip summary/embed
  when set; clear on next successful probe.
- [ ] `internal/server`:
  - `GET /links/{id}` shows the banner when summary is missing AND
    the worker has logged a recent health failure.
  - `POST /links/{id}/summarize` (alias for `reindex` but with a
    clearer name when the user is in the "no LLM yet" flow).
  - `GET /healthz/llm` returns "ok" / "down: <reason>" so the topbar
    can flag the gateway state.
- [ ] Topbar badge: when the LLM is configured but health-probing
  fails, show a yellow "LLM offline" badge next to the model name.
- [ ] Documentation: README/CLAUDE note that linklore is fully usable
  without an LLM, the bits that come back when one is configured.

### Tests
- [ ] `TestWorker_skipsLLMStepsWhenUnhealthy`: feed a backend whose
  Healthcheck always errors; tick → link reaches `fetched`, no
  summary, no chunks.
- [ ] `TestWorker_recoversWhenLLMComesBack`: backend goes from
  unhealthy → healthy between two ticks; link advances to
  `summarized` on the second tick.
- [ ] `TestServer_summarizeButton_gated`: button only appears on
  status=`fetched` (and never on `summarized`).
- [ ] `TestServer_summarizeButton_503withClearMessage` when no LLM is
  configured at all (cfg.LLM.Backend == "" or backend.New errored at
  startup).

---

## Order of work

Single-day-each chunks; items inside a section are usually 1–3 hours.

1. **§7 LLM-optional** — biggest UX cliff right now; unblocks people
   trying linklore without configuring the gateway.
2. **§4 Counters / empty-state OOB swap** — small, big quality-of-life,
   removes the "did my click work?" feeling.
3. **§5 Search regression** — tied to §4 (often shows up at the same
   time the user adds links rapidly).
4. **§1 Sidebar** — once §4 is in, the sidebar gets the same OOB swap
   for free.
5. **§3 Move-to-collection** — server-side first (dropdown). Easy ship.
6. **§2 Theme** — small, contained, but needs the new `preferences`
   table that §7 also benefits from.
7. **§6 Drag & drop** — last. Native HTML5 DnD; revisit Alpine only
   if vanilla wiring becomes ugly.

## Test discipline

- Every section above lists explicit handler-level tests (httptest)
  alongside any storage helpers. No new feature without at least one
  green regression test.
- `make test` stays the canonical single command (race + count=1 +
  sqlite_fts5).
- Rule of thumb: the moment a bug ships in the UI, the next commit
  contains a test that reproduces it BEFORE the fix.
