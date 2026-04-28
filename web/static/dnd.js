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

	function onDragStart(ev) {
		var row = ev.target.closest('[data-link-id]');
		if (!row) return;
		ev.dataTransfer.effectAllowed = 'move';
		ev.dataTransfer.setData('text/plain', row.dataset.linkId);
		// Bind the drag image to the actual row at the user's pointer offset
		// so the cursor stays where the click started — no detachment.
		var rect = row.getBoundingClientRect();
		try {
			ev.dataTransfer.setDragImage(row, ev.clientX - rect.left, ev.clientY - rect.top);
		} catch (_) { /* not all browsers honour this; ignore */ }
		row.classList.add('dnd-dragging');
	}

	function onDragEnd(ev) {
		var row = ev.target.closest('[data-link-id]');
		if (row) row.classList.remove('dnd-dragging');
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
		// Reuse htmx so OOB swaps in the response (stats card + empty state)
		// take effect on the current page without reloading.
		if (window.htmx && html) {
			window.htmx.swap(document.body, html, { swapStyle: 'beforeend' });
		}
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

		// Sidebar drop → cross-collection move. Visual: row disappears
		// from current page (OOB), the sidebar count refreshes, and we
		// also fade out the source row immediately so the user sees it.
		var sidebarTarget = ev.target.closest('[data-collection-slug]');
		if (sidebarTarget) {
			clearDropHints();
			var sourceRow = document.getElementById('link-' + linkId);
			if (sourceRow) {
				sourceRow.classList.add('dnd-leaving');
				setTimeout(function () {
					if (sourceRow.parentNode) sourceRow.parentNode.removeChild(sourceRow);
				}, 220);
			}
			postForm('/links/' + encodeURIComponent(linkId) + '/move',
				{ collection_slug: sidebarTarget.dataset.collectionSlug });
			return;
		}

		// Row drop → reorder.
		var rowTarget = ev.target.closest('[data-link-id]');
		if (!rowTarget || rowTarget.dataset.linkId === linkId) {
			clearDropHints();
			return;
		}

		// Use the indicator's last computed position so dropping just
		// "outside" the row's strict midpoint still does the right thing.
		var ind = document.getElementById(INDICATOR_ID);
		var position = (ind && ind.dataset.position) || 'after';
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
