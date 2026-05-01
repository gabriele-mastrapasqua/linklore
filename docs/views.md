# Views

Linklore renders a collection's links in one of four layouts. Switch
via the **view:** strip on the collection page; the choice persists
per collection in the database (`collections.layout` column) and is
shared across browsers.

| Layout      | Best for                              | Cover | Description |
|-------------|---------------------------------------|-------|-------------|
| **List**    | Reading dense bookmarks day-to-day    | Right by default; `cover:` toggle moves it to the left edge | Title + URL + summary + tags. The default. |
| **Cards**   | Visual scanning of an article-heavy collection | Top of card | A grid of cards. Cover-size slider drives `--card-min`. |
| **Headlines** | Long collections you want to skim flat | None | Single-line rows, ~32 px tall: favicon + title + tag chips. |
| **Moodboard** | Image-heavy collections (videos, design boards) | Top, full-bleed | CSS `column-count` masonry; reflows via `@container main` queries. |

## Per-view controls

The view-mode strip exposes only the controls that apply to the active
layout. JS hides the rest with `[hidden]`.

- **List** → `cover:` (left / right). The cover column gets `order: -1`
  when "left" is active; the action column stays anchored on the right.
- **Cards / Moodboard** → `size:` slider. Drives `--card-min` on
  `#links-list` from 140 px to 360 px in 20 px steps.
- **Headlines** → no extra controls.

All preferences persist in localStorage:

| Key                       | Values                | Used by    |
|---------------------------|-----------------------|------------|
| `linklore.cardSize`       | 140–360 (px)          | Cards / Moodboard |
| `linklore.coverPos`       | `left` / `right`      | List       |
| `linklore.density`        | comma list of fields  | All        |
| `linklore.select`         | `on` / `off`          | All (bulk select) |

## Density

Independent of the layout, `density:` toggles three field groups via
body classes — `density-no-title`, `density-no-summary`,
`density-no-badges`. Useful for the headlines layout when you want
even more rows on screen.

## Container queries

The link list uses `container-type: inline-size` on `<main>` so the
moodboard reflows to 1–2 columns when you open the drawer (which
shrinks `<main>`), not just when the viewport itself shrinks. Avoids
the "open drawer → cards stay 3-wide and overlap" classic.

## Source code

- Templates: `web/templates/links.html`,
  `web/templates/partials/link_row.html`
- CSS: `web/static/app.css` — search for `#links-list.layout-`
- JS: `web/static/views.js`
- Backend: `POST /c/{slug}/layout` in `internal/server/server.go`
