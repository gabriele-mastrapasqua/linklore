# Responsive layout

Linklore's UI is designed to behave from a 360 px phone to a 1920 px
desktop without media-query fanouts or JS layout work. Two ideas
underpin the responsive design:

1. **Grid shell, container queries inside.** The main shell is one CSS
   grid; the link list sits inside a container so it responds to its
   own width, not the viewport.
2. **Off-canvas sidebar via the checkbox-hack.** No JS handles the
   sidebar transform — a hidden `#sidebar-toggle` checkbox plus a
   `<label class="menu-btn">` in the topbar drives the slide-in.

## Breakpoints

| Width       | Behaviour |
|-------------|-----------|
| `> 760 px`  | Desktop shell. Sidebar visible at `clamp(220px, 22vw, 280px)`. Topbar shows brand wordmark, kbd hint, status pills, full pref-btn labels. |
| `≤ 760 px`  | Topbar compaction: hide wordmark, ⌘K hint, worker / LLM status pills. Pref-btns shrink to dot-indicator pills. |
| `≤ 720 px`  | Sidebar becomes off-canvas (transform-only). Hamburger label appears in the topbar, scrim dims `<main>`. |
| `≤ 600 px`  | Topbar gap and padding tighten further. |
| `≤ 360 px`  | Pref-btns drop entirely. Main content padding shrinks to `var(--space-3)`. |

## Container queries

`<main>` carries `container-type: inline-size`. The moodboard layout
flips columns at `(max-width: 1100px)` and `(max-width: 700px)` of the
**container**, not the viewport. So opening the right-pane drawer
(which shrinks `<main>`) reflows the moodboard automatically — the
cards never overlap or get pushed off-screen.

## Off-canvas sidebar

`base.html` has the structure:

```html
<input type="checkbox" id="sidebar-toggle">
<label class="sidebar-scrim" for="sidebar-toggle"></label>
<header class="topbar">
  <label class="menu-btn" for="sidebar-toggle"></label>
  …
</header>
<div class="layout">
  <aside class="sidebar">…</aside>
  <main>…</main>
</div>
```

CSS uses sibling selectors:

- `#sidebar-toggle:checked ~ .layout .sidebar { transform: translateX(0); }`
- `#sidebar-toggle:checked ~ .sidebar-scrim { opacity: 1; pointer-events: auto; }`

`events.js` adds a 5-line click delegation that unchecks the toggle
when a sidebar link is followed (HTMX nav doesn't reload, so the
checkbox state would otherwise persist).

## Tokens that drive the geometry

```
--sidebar-w: clamp(220px, 22vw, 280px);
--topbar-h:  56px;
--space-{0,1,2,3,4,6}
--radius-{sm,md,lg,pill}
--shadow-{1,2,pop,card}
```

Change one token, the whole shell follows.

## Things that are NOT responsive

- The drawer toolbar (size / width / theme) wraps to two lines on
  tight viewports — that's by design. It's reading mode; people who
  open it on a phone almost always want maximize anyway.
- The link list density toggles (titles / summary / badges) don't
  change automatically with viewport. They're a deliberate user
  preference.

## Source code

- CSS: `web/static/app.css` — search for `@media`, `@container main`,
  `#sidebar-toggle`
- Template: `web/templates/base.html`
- JS (5 lines): `web/static/events.js`
