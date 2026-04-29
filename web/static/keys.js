// Keyboard navigation for the collection page.
//
//   j / ↓  → next card
//   k / ↑  → previous card
//   ↵      → open the focused card (deep-link to /links/:id)
//   x      → toggle bulk selection on the focused card
//   delete → delete the focused card (with confirm)
//   /      → focus the global search input
//   ?      → open the shortcut overlay (esc dismisses)
//
// All shortcuts no-op while the user is typing in an <input>,
// <textarea> or contenteditable element so they don't fight typing.

(function () {
	'use strict';
	var ns = (window.linklore = window.linklore || {});

	var FOCUS_CLASS = 'kbd-focus';
	var idx = -1;

	function rows() {
		return Array.prototype.slice.call(
			document.querySelectorAll('#links-list .link-row')
		);
	}

	function setFocus(i) {
		var rs = rows();
		if (rs.length === 0) { idx = -1; return; }
		// Wrap-around at the ends.
		if (i < 0) i = rs.length - 1;
		if (i >= rs.length) i = 0;
		rs.forEach(function (r) { r.classList.remove(FOCUS_CLASS); });
		idx = i;
		var el = rs[i];
		el.classList.add(FOCUS_CLASS);
		el.scrollIntoView({ block: 'nearest', behavior: 'smooth' });
	}

	function isTypingTarget(el) {
		if (!el) return false;
		var tag = el.tagName;
		if (tag === 'INPUT' || tag === 'TEXTAREA' || tag === 'SELECT') return true;
		if (el.isContentEditable) return true;
		return false;
	}

	function focusedRow() {
		var rs = rows();
		if (idx < 0 || idx >= rs.length) return null;
		return rs[idx];
	}

	function openFocused() {
		var row = focusedRow();
		if (!row) return;
		var anchor = row.querySelector('.title a[href]');
		if (anchor) anchor.click();
	}

	function deleteFocused() {
		var row = focusedRow();
		if (!row) return;
		var btn = row.querySelector('button.danger[hx-delete], button.danger');
		if (btn) btn.click();
	}

	function toggleSelectFocused() {
		var row = focusedRow();
		if (!row) return;
		var cb = row.querySelector('.bulk-select');
		if (!cb) return;
		cb.checked = !cb.checked;
		cb.dispatchEvent(new Event('change', { bubbles: true }));
	}

	function showOverlay() {
		var existing = document.getElementById('kbd-overlay');
		if (existing) { existing.remove(); return; }
		var el = document.createElement('div');
		el.id = 'kbd-overlay';
		el.className = 'kbd-overlay';
		el.innerHTML =
			'<div class="kbd-overlay-card">' +
				'<h3>Keyboard shortcuts</h3>' +
				'<table>' +
					'<tr><td><kbd>j</kbd> / <kbd>↓</kbd></td><td>next card</td></tr>' +
					'<tr><td><kbd>k</kbd> / <kbd>↑</kbd></td><td>previous card</td></tr>' +
					'<tr><td><kbd>↵</kbd></td><td>open</td></tr>' +
					'<tr><td><kbd>x</kbd></td><td>toggle selection</td></tr>' +
					'<tr><td><kbd>del</kbd></td><td>delete</td></tr>' +
					'<tr><td><kbd>/</kbd></td><td>focus search</td></tr>' +
					'<tr><td><kbd>?</kbd></td><td>this overlay</td></tr>' +
					'<tr><td><kbd>esc</kbd></td><td>dismiss / clear selection</td></tr>' +
				'</table>' +
				'<small class="muted">Press <kbd>esc</kbd> or <kbd>?</kbd> to dismiss.</small>' +
			'</div>';
		el.addEventListener('click', function () { el.remove(); });
		document.body.appendChild(el);
	}

	document.addEventListener('keydown', function (e) {
		// Always allow esc to clear selection / dismiss overlay even while typing.
		if (e.key === 'Escape') {
			var ov = document.getElementById('kbd-overlay');
			if (ov) { ov.remove(); return; }
			if (typeof ns.bulkClear === 'function') ns.bulkClear();
			return;
		}
		if (isTypingTarget(e.target)) return;

		switch (e.key) {
			case 'j': case 'ArrowDown':
				e.preventDefault(); setFocus(idx + 1); break;
			case 'k': case 'ArrowUp':
				e.preventDefault(); setFocus(idx - 1); break;
			case 'Enter':
				if (focusedRow()) { e.preventDefault(); openFocused(); }
				break;
			case 'x':
				if (focusedRow()) { e.preventDefault(); toggleSelectFocused(); }
				break;
			case 'Delete': case 'Backspace':
				if (focusedRow()) { e.preventDefault(); deleteFocused(); }
				break;
			case '/':
				e.preventDefault();
				var s = document.querySelector('header.topbar input[type="search"]');
				if (s) s.focus();
				break;
			case '?':
				e.preventDefault(); showOverlay(); break;
		}
	});

	// After every HTMX swap, the row at our index may no longer exist —
	// snap back to the first card if so.
	document.body.addEventListener('htmx:afterSwap', function () {
		var rs = rows();
		if (idx >= rs.length) idx = -1;
	});
})();
