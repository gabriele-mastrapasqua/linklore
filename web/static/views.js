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

	document.addEventListener('DOMContentLoaded', applyDensity);
	document.body.addEventListener('htmx:afterSwap', applyDensity);
})();
