// Right-pane preview drawer. Clicking a link's title in the row
// (without modifier keys) intercepts the navigation and slides in
// the drawer with the article rendered. The deep link (/links/:id)
// stays intact for cmd-click / middle-click / paste-URL.
//
// Reader controls (size / width / theme) flip data-attrs on the
// drawer; CSS keys off them. Choices persist per-browser via
// localStorage.

(function () {
	'use strict';
	var ns = (window.linklore = window.linklore || {});

	var KEY_SIZE  = 'linklore.drawer.size';   // s | m | l (default m)
	var KEY_WIDTH = 'linklore.drawer.width';  // narrow | medium | wide
	var KEY_THEME = 'linklore.drawer.theme';  // light | sepia | dark

	function drawer()  { return document.getElementById('drawer'); }
	function backdrop() { return document.getElementById('drawer-backdrop'); }
	function content() { return document.getElementById('drawer-content'); }

	function applySaved() {
		var d = drawer();
		if (!d) return;
		d.setAttribute('data-size',  localStorage.getItem(KEY_SIZE)  || 'm');
		d.setAttribute('data-width', localStorage.getItem(KEY_WIDTH) || 'medium');
		d.setAttribute('data-theme', localStorage.getItem(KEY_THEME) || 'sepia');
		// Highlight the active controls inside the drawer (after every load).
		[['size',  KEY_SIZE,  'm'     ],
		 ['width', KEY_WIDTH, 'medium'],
		 ['theme', KEY_THEME, 'sepia' ]].forEach(function (row) {
			var attr = row[0], cur = localStorage.getItem(row[1]) || row[2];
			d.querySelectorAll('.drawer-toolbar .seg-switch .seg-opt').forEach(function () {});
			d.querySelectorAll('.drawer-toolbar .seg-switch').forEach(function (sw) {
				// Each seg-switch's onclick wires one of the three families;
				// match by the call signature drawer.html embeds.
				var btns = sw.querySelectorAll('.seg-opt');
				btns.forEach(function (b) {
					var oc = b.getAttribute('onclick') || '';
					if (oc.indexOf('drawer'+attr.charAt(0).toUpperCase()+attr.slice(1)+'(') < 0) return;
					var v = oc.match(/'([^']+)'/);
					if (v && v[1] === cur) b.classList.add('active');
					else b.classList.remove('active');
				});
			});
		});
	}

	ns.drawerOpen = function (linkID) {
		var d = drawer(), b = backdrop(), c = content();
		if (!d || !b || !c) return;
		d.removeAttribute('hidden');
		b.removeAttribute('hidden');
		// Defer to next paint so the slide-in transition kicks in.
		requestAnimationFrame(function () {
			d.classList.add('drawer-open');
			b.classList.add('drawer-open');
		});
		c.innerHTML = '<div class="drawer-loading muted">Loading…</div>';
		if (window.htmx) {
			htmx.ajax('GET', '/links/' + linkID + '/preview', {
				target: '#drawer-content',
				swap: 'innerHTML',
			}).then(applySaved);
		} else {
			fetch('/links/' + linkID + '/preview').then(function (r) { return r.text(); })
			   .then(function (html) { c.innerHTML = html; applySaved(); });
		}
	};

	ns.drawerClose = function () {
		var d = drawer(), b = backdrop();
		if (!d || !b) return;
		d.classList.remove('drawer-open');
		b.classList.remove('drawer-open');
		setTimeout(function () {
			d.setAttribute('hidden', '');
			b.setAttribute('hidden', '');
		}, 220);
	};

	ns.drawerSize  = function (v) { drawer().setAttribute('data-size',  v);  localStorage.setItem(KEY_SIZE,  v); applySaved(); };
	ns.drawerWidth = function (v) { drawer().setAttribute('data-width', v); localStorage.setItem(KEY_WIDTH, v); applySaved(); };
	ns.drawerTheme = function (v) { drawer().setAttribute('data-theme', v); localStorage.setItem(KEY_THEME, v); applySaved(); };

	// Toggle full-width drawer mode by flipping data-drawer on <body>;
	// CSS scales width: min(820px, 96vw) → 100vw under [data-drawer="full"].
	ns.drawerMaximize = function () {
		var b = document.body;
		if (b.getAttribute('data-drawer') === 'full') {
			b.removeAttribute('data-drawer');
		} else {
			b.setAttribute('data-drawer', 'full');
		}
	};

	// Mark the clicked tab as active so users see which tab they're on.
	// We listen on the tab strip itself so the listener survives drawer
	// content swaps (the strip lives in #drawer-content, not #drawer-tab-body).
	document.addEventListener('click', function (e) {
		var tab = e.target.closest('.drawer-tab');
		if (!tab) return;
		var strip = tab.parentElement;
		strip.querySelectorAll('.drawer-tab').forEach(function (t) {
			t.classList.remove('drawer-tab-active');
			t.removeAttribute('aria-selected');
		});
		tab.classList.add('drawer-tab-active');
		tab.setAttribute('aria-selected', 'true');
	});

	// Intercept clicks anywhere on a row body that isn't an interactive
	// element. Title click → drawer (deep link still works for cmd/middle
	// click). Body click on the URL line, summary, description, badges
	// → drawer. Buttons / inputs / external "open link" anchors / "ask"
	// chat link / "add note" anchor / kind icon all keep their own
	// behaviour because they sit inside <a>, <button>, <input>, <label>,
	// <img>.
	var DRAWER_IGNORE_TAGS = { A: 1, BUTTON: 1, INPUT: 1, SELECT: 1, TEXTAREA: 1, IMG: 1, LABEL: 1 };
	function isInteractiveTarget(target, row) {
		for (var el = target; el && el !== row; el = el.parentElement) {
			if (DRAWER_IGNORE_TAGS[el.tagName]) return true;
		}
		return false;
	}

	document.addEventListener('click', function (e) {
		if (e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return;
		if (e.button !== 0) return;
		// Title link → open drawer instead of navigating.
		var titleAnchor = e.target.closest('.link-row .title a[href^="/links/"]');
		if (titleAnchor) {
			var m = titleAnchor.getAttribute('href').match(/^\/links\/(\d+)$/);
			if (m) {
				e.preventDefault();
				ns.drawerOpen(m[1]);
				return;
			}
		}
		// Body click (anywhere on the row that isn't a link/button/input)
		// → open drawer. Mirrors the title-click behaviour without forcing
		// the user to hit a small target. Selection state lives in the
		// checkbox alone now (see bulk.js).
		var row = e.target.closest('.link-row');
		if (!row) return;
		if (isInteractiveTarget(e.target, row)) return;
		// Avoid hijacking native text-selection: if the user dragged to
		// highlight, leave them alone.
		var sel = window.getSelection && window.getSelection();
		if (sel && sel.toString().length > 0) return;
		var id = row.getAttribute('data-link-id');
		if (!id) return;
		e.preventDefault();
		ns.drawerOpen(id);
	});

	// esc dismisses (and beats keys.js's overlay path because that one
	// returns early when an overlay is present, not when the drawer is).
	document.addEventListener('keydown', function (e) {
		if (e.key !== 'Escape') return;
		var d = drawer();
		if (d && !d.hasAttribute('hidden')) {
			e.preventDefault();
			ns.drawerClose();
		}
	});

	document.addEventListener('DOMContentLoaded', applySaved);
})();
