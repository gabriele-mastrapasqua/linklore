// Minimal drag-and-drop for linklore. Native HTML5 DnD only — no Alpine,
// no Sortable. Two flows:
//
//   1. Drag a link row onto a sidebar collection link → POST /links/{id}/move
//      with the destination collection_slug. The OOB swap handles the row
//      removal and counter refresh.
//
//   2. (Future) Drag a row above/below another row inside the same
//      collection to reorder. Stubbed; needs a server endpoint that
//      accepts an order_idx.
//
// We deliberately keep the file under 80 lines so it's auditable at a
// glance. HTMX still drives every actual mutation; this script just
// translates pointer events into fetch() calls.

(function () {
	'use strict';

	function clearDropHints() {
		document.querySelectorAll('.dnd-drop-target').forEach(function (el) {
			el.classList.remove('dnd-drop-target');
		});
	}

	function onDragStart(ev) {
		var row = ev.target.closest('[data-link-id]');
		if (!row) return;
		ev.dataTransfer.effectAllowed = 'move';
		ev.dataTransfer.setData('text/plain', row.dataset.linkId);
		row.classList.add('dnd-dragging');
	}

	function onDragEnd(ev) {
		var row = ev.target.closest('[data-link-id]');
		if (row) row.classList.remove('dnd-dragging');
		clearDropHints();
	}

	function onDragOver(ev) {
		var target = ev.target.closest('[data-collection-slug]');
		if (!target) return;
		ev.preventDefault();
		ev.dataTransfer.dropEffect = 'move';
		clearDropHints();
		target.classList.add('dnd-drop-target');
	}

	function onDrop(ev) {
		var target = ev.target.closest('[data-collection-slug]');
		if (!target) return;
		ev.preventDefault();
		clearDropHints();
		var linkId = ev.dataTransfer.getData('text/plain');
		if (!linkId) return;
		var slug = target.dataset.collectionSlug;
		var body = new URLSearchParams({ collection_slug: slug });
		fetch('/links/' + encodeURIComponent(linkId) + '/move', {
			method: 'POST',
			headers: { 'Content-Type': 'application/x-www-form-urlencoded' },
			body: body,
		}).then(function (resp) {
			if (!resp.ok) return;
			// Pull HTMX into the response so OOB swaps for the stats card
			// (and the optional empty-state) take effect on the current page.
			resp.text().then(function (html) {
				if (window.htmx) {
					window.htmx.swap(document.body, html, { swapStyle: 'beforeend' });
				}
			});
		});
	}

	document.addEventListener('dragstart', onDragStart);
	document.addEventListener('dragend', onDragEnd);
	document.addEventListener('dragover', onDragOver);
	document.addEventListener('drop', onDrop);
})();
