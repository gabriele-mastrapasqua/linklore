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

	// Intercept clicks on the link title (and only the title) so the
	// drawer opens instead of navigating. Modifier-keyed clicks fall
	// through to the browser's default — cmd-click new tab still works.
	document.addEventListener('click', function (e) {
		if (e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return;
		if (e.button !== 0) return;
		var a = e.target.closest('.link-row .title a[href^="/links/"]');
		if (!a) return;
		var m = a.getAttribute('href').match(/^\/links\/(\d+)$/);
		if (!m) return;
		e.preventDefault();
		ns.drawerOpen(m[1]);
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
