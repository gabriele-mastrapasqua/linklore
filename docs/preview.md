# Preview drawer

Click any link's title in a list and the right-pane drawer slides in
with five tabs. The drawer is a single fragment served by
`/links/{id}/preview`; tab swaps load partial bodies via
`/links/{id}/drawer/{tab}`.

## Tabs

| Tab     | Backed by                       | What it shows |
|---------|----------------------------------|---------------|
| **Edit**    | `drawer_edit.html`         | Cover thumb, title, description, Note, Collection selector, Tags chips, URL, saved-at, Delete button. |
| **Preview** | `drawer_preview_body.html` | Linklore-styled render of the extracted markdown. The default tab. |
| **Web**     | `drawer_web.html`          | Sandboxed iframe of the live URL. Falls back to "open in new tab" when the site sets `X-Frame-Options: DENY`. |
| **Archive** | `drawer_archive.html`      | Three Wayback Machine links: all snapshots, latest, save now. |
| **Chat**    | `drawer_chat.html`         | Visible only when `llm.backend != none`. Three suggested prompts plus a free-text input that opens `/chat?link=<id>&ask=…`. |

## Why "Preview" uses our CSS, not the source site's

Two reasons:

1. **Privacy** — rendering the original site's CSS would mean the
   browser fetching the source's stylesheets, fonts, and trackers.
   Linklore's preview is a clean read of saved markdown; nothing leaves
   your machine.
2. **Consistency** — every saved article reads with the same
   typography (Inter base, 1.6 line-height, 68ch measure). You build
   reading muscle memory across sources.

The pipeline is `extract.Fetch → readability.Parse →
html-to-markdown → renderer.Render → drawer-article`. The drawer's
`data-size` / `data-width` / `data-theme` attributes pivot to a few
typographic presets without re-rendering.

## Reader controls

In the Preview tab toolbar: **size** (S/M/L), **width** (narrow /
medium / wide → 56ch / 68ch / 92ch), **theme** (light / sepia / dark).
Persisted in localStorage. Theme is independent of the site theme so
you can read sepia inside dark mode.

## Maximize

The `⤢` button toggles `body[data-drawer="full"]`. CSS widens the
drawer from `min(820px, 96vw)` to `100vw`. Esc still closes.

## Image handling

- Inline article images come from the source URL — never blobbed into
  linklore's storage (CLAUDE.md: "Images are links, not blobs").
- The site-wide `previews: on/off` toggle in the topbar also hides
  drawer-article images. Useful for low-bandwidth or distraction-free
  reading.
- Hero images are tightened: the first image in the article gets
  `margin-top: 0` so it doesn't sit in a half-empty band.

## Source code

- Templates: `web/templates/partials/preview_drawer.html`,
  `drawer_edit.html`, `drawer_web.html`, `drawer_archive.html`,
  `drawer_chat.html`
- CSS: `web/static/app.css` — search for `.drawer`, `.drawer-tab`,
  `.drawer-article`
- JS: `web/static/drawer.js`
- Backend: `handlePreview`, `handleDrawerTab` in
  `internal/server/server.go`
