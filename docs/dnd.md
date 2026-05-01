# Drag and drop

Linklore uses native HTML5 drag-and-drop — no library, ~150 LOC in
`web/static/dnd.js`. Two flows are supported:

1. **Cross-collection move** — drag a row over a sidebar collection
   entry; releases land via `POST /links/{id}/move`.
2. **In-list reorder** — drag a row over another row in the same
   collection; releases land via `POST /links/{id}/reorder` with
   `pivot_id` and `position=before|after`.

## The drag chip

We replace the browser's default "ghost of the whole card" with a
small floating pill via `dataTransfer.setDragImage(chip, -8, -8)`.
The chip:

- **Single row drag** — favicon (or `↕` glyph fallback) + the row's
  title (truncated to 60 chars). Neutral palette pill.
- **Multi-row drag** (when ≥2 rows have their bulk-select checkbox
  ticked) — favicon + `N items`. Violet (`--accent`) pill.

CSS lives at the bottom of `app.css` under `.dnd-drag-image`. The
chip uses `--shadow-pop` for the lift.

## Drop targets

- **Sidebar collection link** (`.sidebar-link[data-collection-slug]`)
  → `dnd-drop-target` class added on `dragover`, removed on `dragleave`
  / `drop` / `dragend`.
- **Another row** → an absolutely-positioned insertion bar
  (`#dnd-insertion-indicator`) appears at the top or bottom edge of
  the targeted row depending on which half of the row the cursor is
  in. In Cards layout (`#links-list.layout-grid`) the bar rotates
  vertical and sits between two cards instead.

## Performance details

- `dragover` fires at ~60 Hz. We throttle DOM writes to one
  `requestAnimationFrame` per visual change, gated by a `lastTargetRowId`
  + `lastSide` skip-when-unchanged check. Without this, the indicator
  flickered while the cursor moved.
- The indicator transitions `top / left / width` over 80 ms, so when
  the cursor crosses a row boundary the bar slides between gaps rather
  than jumping.

## Optimistic UI

On drop, the source row is reordered in the DOM immediately and gets
`.dnd-just-moved` (a 600 ms highlight). The server response is parsed
for HTMX out-of-band swaps (sidebar count refresh, etc.) — there's no
spinner because we trust the local state and reconcile via the SSE
event stream if anything diverges.

## Source code

- JS: `web/static/dnd.js`
- CSS: `web/static/app.css` — search for `.dnd-`
- Server: `handleMoveLink`, `handleReorderLink` in
  `internal/server/server.go`
