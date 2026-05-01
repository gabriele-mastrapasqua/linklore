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
		applyViewControls();
	};

	// Cover-size slider (Cards/Moodboard). Drives --card-min on #links-list.
	// Persisted per-browser; range 140–360px in 20px steps.
	ns.setCardSize = function (px) {
		var list = document.getElementById('links-list');
		if (!list) return;
		list.style.setProperty('--card-min', px + 'px');
		localStorage.setItem('linklore.cardSize', String(px));
	};

	// Cover position for List view. Adds .cover-right on #links-list and
	// flips the row direction via CSS. Persisted per-browser.
	ns.setCoverPosition = function (pos) {
		var list = document.getElementById('links-list');
		if (!list) return;
		var left = pos === 'left';
		list.classList.toggle('cover-left', left);
		localStorage.setItem('linklore.coverPos', left ? 'left' : 'right');
		document.querySelectorAll('.cover-pos-opt').forEach(function (b) {
			b.classList.toggle('active', b.getAttribute('data-cover') === (left ? 'left' : 'right'));
		});
	};

	// Hide controls that don't apply to the active view: size slider only
	// for grid/moodboard, cover-position only for list.
	function applyViewControls() {
		var list = document.getElementById('links-list');
		if (!list) return;
		var layout = (list.className.match(/layout-(\w+)/) || [])[1] || 'list';
		var hasCards = layout === 'grid' || layout === 'moodboard';
		var hasCoverPos = layout === 'list';
		document.querySelectorAll('.cover-size-slider, .cover-size-label')
			.forEach(function (el) { el.hidden = !hasCards; });
		document.querySelectorAll('.cover-pos-switch, .cover-pos-label')
			.forEach(function (el) { el.hidden = !hasCoverPos; });
	}

	function applySavedViewState() {
		var list = document.getElementById('links-list');
		if (!list) return;
		var size = localStorage.getItem('linklore.cardSize');
		if (size) {
			list.style.setProperty('--card-min', size + 'px');
			var slider = document.querySelector('.cover-size-slider');
			if (slider) slider.value = size;
		}
		var cover = localStorage.getItem('linklore.coverPos') || 'right';
		ns.setCoverPosition(cover);
		applyViewControls();
	}

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
		applySavedViewState();
	});
	document.body.addEventListener('htmx:afterSwap', function () {
		applyDensity();
		applySelectMode();
		applySavedViewState();
	});
})();
