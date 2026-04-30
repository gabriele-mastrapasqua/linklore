// Bulk-selection helper for the collection page. Tracks which link rows
// are checked, shows/hides the bulk action bar, and serialises selected
// IDs into hidden form inputs right before the HTMX submit.
//
// State lives in the DOM (the checkboxes themselves), not in a JS Set —
// htmx swaps rows in/out and the source of truth has to follow.

(function () {
	'use strict';

	var ns = (window.linklore = window.linklore || {});

	function selectedIDs() {
		var out = [];
		document.querySelectorAll('.bulk-select:checked').forEach(function (cb) {
			var id = cb.getAttribute('data-link-id');
			if (id) out.push(id);
		});
		return out;
	}

	function refreshBar() {
		var bar = document.getElementById('bulk-bar');
		if (!bar) return;
		var ids = selectedIDs();
		var count = document.getElementById('bulk-count');
		if (count) count.textContent = String(ids.length);
		if (ids.length > 0) bar.removeAttribute('hidden');
		else bar.setAttribute('hidden', '');
	}

	ns.bulkPopulate = function (form) {
		var ids = selectedIDs();
		if (ids.length === 0) {
			alert('No links selected.');
			return false;
		}
		var hidden = form.querySelector('.bulk-ids');
		if (hidden) hidden.value = ids.join(',');
		return true;
	};

	ns.bulkClear = function () {
		document.querySelectorAll('.bulk-select:checked').forEach(function (cb) {
			cb.checked = false;
		});
		refreshBar();
	};

	// Delegate change events so newly-swapped-in rows just work.
	document.addEventListener('change', function (e) {
		var t = e.target;
		if (t && t.classList && t.classList.contains('bulk-select')) {
			refreshBar();
		}
	});

	// Click-on-row-to-toggle-selection was confusing: users expected
	// the click to OPEN the link (drawer.js already does that). Selection
	// now only happens via the explicit checkbox, which itself is hidden
	// unless the user enabled "Select mode" via the topbar/header toggle.

	// After any HTMX swap (e.g. row removal from a bulk action), the
	// remaining selection state lives entirely in the surviving
	// checkboxes — just re-read it.
	document.body.addEventListener('htmx:afterSwap', refreshBar);
	document.body.addEventListener('htmx:oobAfterSwap', refreshBar);

	// First paint after page load.
	document.addEventListener('DOMContentLoaded', refreshBar);
})();
