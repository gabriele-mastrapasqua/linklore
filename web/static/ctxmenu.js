// Right-click context menu on .link-row. One singleton menu element
// is repositioned per click — keeps the DOM small and keeps the
// active row scoped. Closes on click-outside, scroll, esc, or any
// menu-item click.
//
// Items map to existing UI affordances (the same delete button, the
// same /chat?ask= link, etc.) so we don't duplicate logic.

(function () {
	'use strict';

	function ensure() {
		var m = document.getElementById('ctx-menu');
		if (m) return m;
		m = document.createElement('div');
		m.id = 'ctx-menu';
		m.className = 'ctx-menu';
		m.hidden = true;
		document.body.appendChild(m);
		return m;
	}

	function close() {
		var m = document.getElementById('ctx-menu');
		if (m) m.hidden = true;
	}

	function buildItems(row) {
		var id = row.getAttribute('data-link-id');
		var titleAnchor = row.querySelector('.title a[href^="/links/"]');
		var openOrig = row.querySelector('.actions a[target="_blank"]');
		var origURL = openOrig ? openOrig.getAttribute('href') : '#';
		var deepLink = titleAnchor ? titleAnchor.getAttribute('href') : '/links/' + id;
		var titleText = titleAnchor ? titleAnchor.textContent.trim() : '';

		return [
			{ icon: '👁', label: 'Preview', action: function () {
				if (window.linklore && window.linklore.drawerOpen) {
					window.linklore.drawerOpen(id);
				}
			}},
			{ icon: '↗',  label: 'Open original', action: function () {
				window.open(origURL, '_blank', 'noopener,noreferrer');
			}},
			{ icon: '🔗', label: 'Open detail page', action: function () {
				window.location.href = deepLink;
			}},
			{ icon: '📋', label: 'Copy URL',     action: function () {
				navigator.clipboard.writeText(origURL).then(function () {
					if (window.linklore && window.linklore.toast) {
						window.linklore.toast('URL copied to clipboard', 'ok');
					}
				});
			}},
			{ icon: '✦',  label: 'Ask about this', cls: 'ai-link', action: function () {
				var q = "What's interesting about \"" + (titleText || origURL) + "\"?";
				window.location.href = '/chat?ask=' + encodeURIComponent(q) + '&link=' + id;
			}},
			{ icon: '☐',  label: 'Toggle selection', action: function () {
				var cb = row.querySelector('.bulk-select');
				if (cb) {
					cb.checked = !cb.checked;
					cb.dispatchEvent(new Event('change', { bubbles: true }));
				}
			}},
			{ separator: true },
			{ icon: '🗑',  label: 'Delete', cls: 'danger', action: function () {
				var btn = row.querySelector('button.danger[hx-delete]');
				if (btn) btn.click();
			}},
		];
	}

	function open(row, x, y) {
		var m = ensure();
		m.innerHTML = '';
		buildItems(row).forEach(function (it) {
			if (it.separator) {
				var sep = document.createElement('div');
				sep.className = 'ctx-sep';
				m.appendChild(sep);
				return;
			}
			var b = document.createElement('button');
			b.type = 'button';
			b.className = 'ctx-item' + (it.cls ? ' ' + it.cls : '');
			b.innerHTML = '<span class="ctx-icon">' + it.icon + '</span>' +
			              '<span class="ctx-label"></span>';
			b.querySelector('.ctx-label').textContent = it.label;
			b.addEventListener('click', function () { it.action(); close(); });
			m.appendChild(b);
		});
		m.hidden = false;
		// Reposition so the menu doesn't fall off the viewport.
		m.style.left = '0px';
		m.style.top  = '0px';
		var rect = m.getBoundingClientRect();
		var maxX = window.innerWidth  - rect.width  - 8;
		var maxY = window.innerHeight - rect.height - 8;
		if (x > maxX) x = maxX;
		if (y > maxY) y = maxY;
		m.style.left = Math.max(4, x) + 'px';
		m.style.top  = Math.max(4, y) + 'px';
	}

	document.addEventListener('contextmenu', function (e) {
		var row = e.target.closest && e.target.closest('.link-row');
		if (!row) return;
		// Preserve native menu in interactive elements (links, inputs)
		// where users still expect the browser's "open in new tab" etc.
		var t = e.target;
		if (t.closest('a[href^="http"]') || t.closest('input, button, select, textarea')) return;
		e.preventDefault();
		open(row, e.clientX, e.clientY);
	});

	['click', 'scroll', 'resize'].forEach(function (ev) {
		document.addEventListener(ev, function (e) {
			var m = document.getElementById('ctx-menu');
			if (!m || m.hidden) return;
			if (ev === 'click' && m.contains(e.target)) return;
			close();
		}, { capture: true, passive: true });
	});
	document.addEventListener('keydown', function (e) {
		if (e.key === 'Escape') close();
	});
})();
