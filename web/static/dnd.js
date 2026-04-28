// Minimal drag-and-drop for linklore. Native HTML5 DnD, ~150 lines, no
// libraries. Two flows:
//
//   1. Drag a link row over a SIDEBAR collection link → POST
//      /links/{id}/move with the destination slug. Whole-row indicator.
//
//   2. Drag a link row over ANOTHER ROW in the same list → POST
//      /links/{id}/reorder with pivot_id and position=before|after.
//      A blue insertion bar shows where the row will land BEFORE drop;
//      the moved row then animates into place after the server confirms.
//
// We deliberately avoid setDragImage with a custom canvas because that
// disconnects the cursor from the visual; the native ghost works fine
// once we set a tiny offset.

(function () {
	'use strict';

	var INDICATOR_ID = 'dnd-insertion-indicator';

	function ensureIndicator() {
		var el = document.getElementById(INDICATOR_ID);
		if (!el) {
			el = document.createElement('div');
			el.id = INDICATOR_ID;
			el.className = 'dnd-insertion-bar';
			document.body.appendChild(el);
		}
		return el;
	}

	function hideIndicator() {
		var el = document.getElementById(INDICATOR_ID);
		if (el) el.style.display = 'none';
	}

	function clearDropHints() {
		document.querySelectorAll('.dnd-drop-target').forEach(function (el) {
			el.classList.remove('dnd-drop-target');
		});
		hideIndicator();
	}

	function dragSourceID(ev) {
		return ev.dataTransfer ? ev.dataTransfer.getData('text/plain') : null;
	}

	// Build (once) a tiny custom drag image — a chip showing the link's
	// title or URL prefixed with a "↕" icon. Way less visually heavy
	// than dragging the entire card.
	function buildDragImage(row) {
		var chip = document.createElement('div');
		chip.className = 'dnd-drag-image';
		var label = '';
		var titleEl = row.querySelector('.title a, .title');
		if (titleEl) label = (titleEl.textContent || '').trim();
		if (label.length > 60) label = label.slice(0, 57) + '…';
		if (!label) label = 'link #' + row.dataset.linkId;
		chip.innerHTML = '<span class="dnd-drag-icon">↕</span><span class="dnd-drag-label"></span>';
		chip.querySelector('.dnd-drag-label').textContent = label;
		// Must be on-screen for setDragImage to render it; we pull it
		// off-screen with negative top while the drag is in flight, then
		// remove it on dragend.
		chip.style.position = 'absolute';
		chip.style.top = '-9999px';
		chip.style.left = '-9999px';
		document.body.appendChild(chip);
		return chip;
	}

	function onDragStart(ev) {
		var row = ev.target.closest('[data-link-id]');
		if (!row) return;
		ev.dataTransfer.effectAllowed = 'move';
		ev.dataTransfer.setData('text/plain', row.dataset.linkId);
		// Tiny drag chip — doesn't carry the full row, leaves the
		// cursor visually free over the layout while dragging.
		var chip = buildDragImage(row);
		row._dndChip = chip;
		try {
			// Offset so the chip sits to the lower-right of the cursor,
			// not under it (so the user can still see what's underneath).
			ev.dataTransfer.setDragImage(chip, -8, -8);
		} catch (_) { /* ignore — fallback is the browser default */ }
		row.classList.add('dnd-dragging');
	}

	function onDragEnd(ev) {
		var row = ev.target.closest('[data-link-id]');
		if (row) {
			row.classList.remove('dnd-dragging');
			if (row._dndChip && row._dndChip.parentNode) {
				row._dndChip.parentNode.removeChild(row._dndChip);
			}
			row._dndChip = null;
		}
		clearDropHints();
	}

	function onDragEnter(ev) {
		// Browsers treat <a href> as "open this URL" drop targets unless
		// the entered handler also calls preventDefault, which is the
		// only way to flip them into programmable drop zones. Calling
		// it on every enter is cheap and idempotent.
		var sidebarTarget = ev.target.closest('[data-collection-slug]');
		var rowTarget = ev.target.closest('[data-link-id]');
		if (sidebarTarget || rowTarget) {
			ev.preventDefault();
		}
	}

	function onDragOver(ev) {
		var sourceId = dragSourceID(ev);

		// Sidebar collection target: highlight whole row.
		var sidebarTarget = ev.target.closest('[data-collection-slug]');
		if (sidebarTarget) {
			ev.preventDefault();
			if (ev.dataTransfer) ev.dataTransfer.dropEffect = 'move';
			clearDropHints();
			sidebarTarget.classList.add('dnd-drop-target');
			return;
		}

		// In-list reorder target: another link row.
		var rowTarget = ev.target.closest('[data-link-id]');
		if (rowTarget && rowTarget.dataset.linkId !== sourceId) {
			ev.preventDefault();
			if (ev.dataTransfer) ev.dataTransfer.dropEffect = 'move';
			clearDropHints();
			showInsertionBar(rowTarget, ev.clientY);
			return;
		}
	}

	function showInsertionBar(rowEl, clientY) {
		var rect = rowEl.getBoundingClientRect();
		var midpoint = rect.top + rect.height / 2;
		var above = clientY < midpoint;
		var ind = ensureIndicator();
		ind.style.display = 'block';
		ind.style.left = rect.left + 'px';
		ind.style.width = rect.width + 'px';
		ind.style.top = (above ? rect.top : rect.bottom) + window.scrollY - 1 + 'px';
		ind.dataset.targetId = rowEl.dataset.linkId;
		ind.dataset.position = above ? 'before' : 'after';
	}

	function applyOOBHTML(html) {
		// HTMX only processes hx-swap-oob attributes during real HX
		// responses — not when we feed it a fragment via htmx.swap.
		// We do the OOB pass ourselves: parse the fragment, walk every
		// element carrying hx-swap-oob (or being a known OOB root),
		// look up the existing element by id, and replace it.
		if (!html) return;
		var tpl = document.createElement('template');
		tpl.innerHTML = html.trim();
		// Two forms supported:
		//   1) the element itself has hx-swap-oob (preferred — the
		//      standard HTMX form, e.g. our sidebar entry).
		//   2) a wrapping <div id="X" hx-swap-oob="outerHTML"> contains
		//      the new content (the stats card uses this pattern).
		var nodes = Array.prototype.slice.call(tpl.content.children);
		nodes.forEach(function (node) {
			if (!node || node.nodeType !== 1) return;
			var oob = node.getAttribute('hx-swap-oob');
			if (!oob) return;
			var id = node.id;
			if (!id) {
				// Wrapper form: replace its id-bearing first-child.
				var inner = node.firstElementChild;
				if (!inner || !inner.id) return;
				id = inner.id;
			}
			var existing = document.getElementById(id);
			if (!existing) return;
			// outerHTML replace. If the source was the wrapper form
			// (<wrapper hx-swap-oob><inner id="…">…</inner></wrapper>),
			// we replace the existing element with the inner — that's
			// what the wrapper means.
			var replacement = node;
			if (node.id !== id) replacement = node.firstElementChild;
			if (!replacement) return;
			replacement.removeAttribute('hx-swap-oob');
			existing.replaceWith(replacement);
		});
	}

	function postForm(url, params) {
		return fetch(url, {
			method: 'POST',
			headers: { 'Content-Type': 'application/x-www-form-urlencoded' },
			body: new URLSearchParams(params),
		}).then(function (resp) {
			if (!resp.ok) return null;
			return resp.text().then(applyOOBHTML);
		});
	}

	function onDrop(ev) {
		var linkId = dragSourceID(ev);
		if (!linkId) return;
		// Always prevent the browser's default "navigate to dropped URL"
		// behaviour the moment we know it's our DnD.
		ev.preventDefault();
		ev.stopPropagation();

		// Drop is ACCEPTED only when our UI is showing a highlight:
		//   - sidebar entry has .dnd-drop-target  → cross-collection move
		//   - insertion bar is visible            → in-list reorder
		// Anywhere else (background, header, etc) is treated as cancel.
		// Use the highlighted element rather than ev.target because the
		// user often releases just outside the strict target rectangle.
		var highlightedSidebar = document.querySelector(
			'.sidebar-link.dnd-drop-target[data-collection-slug]');
		if (highlightedSidebar) {
			clearDropHints();
			var sourceRow = document.getElementById('link-' + linkId);
			if (sourceRow) {
				sourceRow.classList.add('dnd-leaving');
				setTimeout(function () {
					if (sourceRow.parentNode) sourceRow.parentNode.removeChild(sourceRow);
				}, 220);
			}
			postForm('/links/' + encodeURIComponent(linkId) + '/move',
				{ collection_slug: highlightedSidebar.dataset.collectionSlug });
			return;
		}

		// Reorder is accepted only when the insertion bar is visible AND
		// has a target id. That means the cursor was over a real row.
		var ind = document.getElementById(INDICATOR_ID);
		var hasIndicator = ind && ind.style.display === 'block' && ind.dataset.targetId;
		if (!hasIndicator || ind.dataset.targetId === linkId) {
			clearDropHints();
			return;
		}
		var rowTarget = document.getElementById('link-' + ind.dataset.targetId);
		if (!rowTarget) {
			clearDropHints();
			return;
		}

		var position = ind.dataset.position || 'after';
		var pivotId = rowTarget.dataset.linkId;

		// Optimistic DOM move so the user sees the result immediately;
		// the server response is a status check only.
		var sourceRow = document.getElementById('link-' + linkId);
		if (sourceRow) {
			sourceRow.classList.add('dnd-just-moved');
			if (position === 'before') {
				rowTarget.parentNode.insertBefore(sourceRow, rowTarget);
			} else {
				rowTarget.parentNode.insertBefore(sourceRow, rowTarget.nextSibling);
			}
			setTimeout(function () { sourceRow.classList.remove('dnd-just-moved'); }, 600);
		}
		clearDropHints();

		postForm('/links/' + encodeURIComponent(linkId) + '/reorder',
			{ pivot_id: pivotId, position: position });
	}

	document.addEventListener('dragstart', onDragStart);
	document.addEventListener('dragend', onDragEnd);
	document.addEventListener('dragenter', onDragEnter);
	document.addEventListener('dragover', onDragOver);
	document.addEventListener('drop', onDrop);
	document.addEventListener('dragleave', function (ev) {
		// Browsers fire dragleave when crossing a child boundary too; only
		// hide the indicator if we've truly left the layout area.
		if (ev.relatedTarget && document.contains(ev.relatedTarget)) return;
		clearDropHints();
	});
})();
