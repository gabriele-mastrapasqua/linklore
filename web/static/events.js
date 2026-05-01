// Server-Sent Events client. Replaces the per-element hx-trigger="every Ns"
// polling: a single long-lived connection to /events delivers worker
// updates, and the page issues targeted GETs to refresh just the
// affected fragments.
//
// Why not htmx-sse? It exists, but the swap rules are limited to one
// target per stream. We need to refresh multiple targets per event
// (the link row, the link detail header, the collection stats card,
// the sidebar entry, the topbar worker badge), so a tiny custom handler
// is simpler and easier to reason about.

(function () {
	'use strict';

	// Close the off-canvas sidebar when a link inside it is followed.
	// Pure click delegation — htmx navigations don't reload the page so
	// :checked state would otherwise persist forever on phones.
	document.addEventListener('click', function (e) {
		var t = e.target.closest && e.target.closest('.sidebar a, .sidebar-link');
		if (!t) return;
		var cb = document.getElementById('sidebar-toggle');
		if (cb && cb.checked) cb.checked = false;
	});

	if (!window.EventSource) {
		// Older browsers — leave the page static, the manual buttons still work.
		return;
	}

	function fetchAndReplace(url, targetId) {
		fetch(url, { credentials: 'same-origin' })
			.then(function (r) { return r.ok ? r.text() : null; })
			.then(function (html) {
				if (!html) return;
				var existing = document.getElementById(targetId);
				if (!existing) return;
				var tpl = document.createElement('template');
				tpl.innerHTML = html.trim();
				var replacement = tpl.content.firstElementChild;
				if (!replacement) return;
				existing.replaceWith(replacement);
			});
	}

	// Refresh helpers: each maps a backend event to the DOM fragments
	// the page should re-fetch. Missing targets are silently ignored
	// (the user might be on a different page).
	function refreshLink(linkId, collectionId) {
		var rowId = 'link-' + linkId;
		if (document.getElementById(rowId)) {
			fetchAndReplace('/links/' + linkId + '/row', rowId);
		}
		var headerId = 'link-header-' + linkId;
		if (document.getElementById(headerId)) {
			fetchAndReplace('/links/' + linkId + '/header', headerId);
		}
		if (collectionId) refreshCollectionStats(collectionId);
	}

	function refreshCollectionStats(collectionId) {
		// Find slug from the existing element so we don't have to keep a
		// global id→slug map.
		var statsEl = document.getElementById('collection-stats-' + collectionId);
		if (statsEl && statsEl.dataset.collectionSlug) {
			fetchAndReplace('/c/' + statsEl.dataset.collectionSlug + '/stats',
				'collection-stats-' + collectionId);
		}
		// Sidebar entry refresh: re-render via /c/{slug}/stats won't
		// touch the sidebar. Pull the home page's sidebar fragment via
		// a dedicated endpoint when present.
		// (For now, sidebar updates ride on the OOB swap from /move + /reorder.)
	}

	function refreshTopbar() {
		var ws = document.getElementById('worker-status');
		if (ws) fetchAndReplace('/worker/status', 'worker-status');
		var lh = document.getElementById('llm-status');
		if (lh) fetchAndReplace('/healthz/llm', 'llm-status');
	}

	// Reconnect with exponential backoff on disconnect; the browser's
	// EventSource does this automatically but only on a clean close —
	// we still want to manage hello/error logging.
	function connect() {
		var es = new EventSource('/events');
		es.addEventListener('hello', function () {
			// Stream is open — refresh once so we don't show stale state.
			refreshTopbar();
		});
		es.addEventListener('link_updated', function (msg) {
			var data = JSON.parse(msg.data);
			refreshLink(data.link_id, data.collection_id);
			refreshTopbar();
		});
		es.addEventListener('link_removed', function (msg) {
			var data = JSON.parse(msg.data);
			var row = document.getElementById('link-' + data.link_id);
			if (row && row.parentNode) row.parentNode.removeChild(row);
			if (data.collection_id) refreshCollectionStats(data.collection_id);
			refreshTopbar();
		});
		es.addEventListener('stats_changed', function (msg) {
			var data = JSON.parse(msg.data);
			if (data.collection_id) refreshCollectionStats(data.collection_id);
			refreshTopbar();
		});
		es.onerror = function () {
			// Browser will retry automatically; nothing to do.
		};
		return es;
	}
	connect();
})();
