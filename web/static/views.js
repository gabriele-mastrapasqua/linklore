// View-mode + density toggles for the collection page. The view-mode
// click also POSTs the choice to /c/:slug/layout (HTMX does that part)
// so the change persists. Density is purely client-side: a flag on
// <body> that CSS keys off.

(function () {
	'use strict';
	var ns = (window.linklore = window.linklore || {});

	ns.setLayout = function (name) {
		var list = document.getElementById('links-list');
		if (!list) return;
		list.classList.remove('layout-list', 'layout-grid', 'layout-headlines', 'layout-moodboard');
		list.classList.add('layout-' + name);
		var sw = document.getElementById('layout-switch');
		if (sw) {
			sw.querySelectorAll('.seg-opt').forEach(function (b) {
				b.classList.toggle('active', b.getAttribute('data-layout') === name);
			});
			sw.setAttribute('data-current', name);
		}
	};

	function applyDensity() {
		var saved = (localStorage.getItem('linklore.density') || '').split(',').filter(Boolean);
		document.body.classList.remove('density-no-title', 'density-no-summary', 'density-no-badges');
		saved.forEach(function (k) { document.body.classList.add('density-no-' + k); });
		document.querySelectorAll('.density-toggle').forEach(function (b) {
			var key = b.getAttribute('data-density');
			b.classList.toggle('density-off', saved.indexOf(key) >= 0);
		});
	}

	ns.toggleDensity = function (key) {
		var saved = (localStorage.getItem('linklore.density') || '').split(',').filter(Boolean);
		var i = saved.indexOf(key);
		if (i >= 0) saved.splice(i, 1); else saved.push(key);
		localStorage.setItem('linklore.density', saved.join(','));
		applyDensity();
	};

	// Select mode: when off (default), per-row checkboxes are hidden via
	// CSS. Flipping it on shows the checkboxes so the user can bulk-pick
	// rows and move/delete them. Persisted per browser.
	function applySelectMode() {
		var on = localStorage.getItem('linklore.select') === 'on';
		document.body.classList.toggle('select-on', on);
		document.body.classList.toggle('select-off', !on);
		var btn = document.getElementById('select-toggle');
		if (btn) {
			btn.textContent = on ? 'on' : 'off';
			btn.classList.toggle('active', on);
		}
		if (!on && typeof ns.bulkClear === 'function') ns.bulkClear();
	}
	ns.toggleSelectMode = function () {
		var cur = localStorage.getItem('linklore.select') === 'on';
		localStorage.setItem('linklore.select', cur ? 'off' : 'on');
		applySelectMode();
	};

	document.addEventListener('DOMContentLoaded', function () {
		applyDensity();
		applySelectMode();
	});
	document.body.addEventListener('htmx:afterSwap', function () {
		applyDensity();
		applySelectMode();
	});
})();
