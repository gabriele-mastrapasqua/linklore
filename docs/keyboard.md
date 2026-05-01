# Keyboard shortcuts

Three sources contribute keys: `web/static/keys.js` (global navigation),
`web/static/palette.js` (command palette), and `web/static/drawer.js`
(reader mode).

## Global

| Key       | Action |
|-----------|--------|
| `/`       | Focus the topbar search input. |
| `⌘K` / `Ctrl+K` | Open the command palette (search + jumps + actions). |
| `?`       | Show the shortcut overlay. |
| `Esc`     | Close any open overlay or drawer. |

## Command palette (⌘K)

The palette is a modal `.kbd-overlay` rendered above everything else.
Type to filter. Hits include:

- **Collections** — direct jumps, ranked by name match.
- **Recent links** — last 20 links by `fetched_at`.
- **Live search results** — backed by `/search/live` once you've
  typed ≥ 2 characters.
- **Actions** — toggle theme, toggle previews, toggle select mode,
  open chat, open settings.

Arrow keys move the highlight; `↵` activates; `Esc` closes.

## Drawer

Open a drawer (click any link's title) and these work:

| Key       | Action |
|-----------|--------|
| `Esc`     | Close the drawer. |

The drawer's `Tab` order goes: close → maximize → open original →
each tab → tab body. Maximize toggles `body[data-drawer="full"]`.

## Bulk selection

When `select: on` is active in the toolbar, every row gets a
checkbox. Clicking the checkbox toggles selection; the bulk bar at
the top shows the count and offers Move / Delete forms. Drag a
selected row → the drag chip switches to "N items" and dropping on a
sidebar collection moves the whole batch.

## Source code

- `web/static/keys.js` — global key handlers, `?` overlay.
- `web/static/palette.js` — command palette implementation.
- `web/static/drawer.js` — drawer-specific handlers (`Esc` close).
- `web/static/bulk.js` — bulk selection state.
