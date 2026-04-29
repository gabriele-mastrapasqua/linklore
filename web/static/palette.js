// Cmd/Ctrl+K command palette. Centred modal with a fuzzy-filterable
// list combining (1) every collection in the sidebar and (2) a fixed
// set of navigation + AI commands. Arrow keys navigate, ↵ runs.
//
// Bootstrap is purely client-side: collections come from the sidebar
// DOM, fixed commands are baked in here. No round-trip needed.

(function () {
	'use strict';
	var ns = (window.linklore = window.linklore || {});

	function fixedItems() {
		return [
			{ label: 'Home — collections',     hint: '/',           href: '/' },
			{ label: 'Tags',                   hint: '/tags',       href: '/tags' },
			{ label: 'Duplicates',             hint: '/duplicates', href: '/duplicates' },
			{ label: 'Bookmarklet',            hint: '/bookmarklet',href: '/bookmarklet' },
			{ label: '✦ Chat',                 hint: '/chat',       href: '/chat',       cls: 'ai-link' },
			{ label: '✦ Ask the LLM…',          hint: 'enter to send', kind: 'ask', cls: 'ai-link' },
		];
	}

	function collectionItems() {
		var nav = document.getElementById('sidebar-collections');
		if (!nav) return [];
		var out = [];
		nav.querySelectorAll('a.sidebar-link').forEach(function (a) {
			var name = (a.querySelector('.sidebar-name') || a).textContent.trim();
			if (!name) name = a.textContent.trim();
			out.push({
				label: 'Open ' + name,
				hint:  a.getAttribute('href'),
				href:  a.getAttribute('href'),
			});
		});
		return out;
	}

	function ensure() {
		var p = document.getElementById('palette');
		if (p) return p;
		p = document.createElement('div');
		p.id = 'palette';
		p.className = 'palette';
		p.hidden = true;
		p.innerHTML =
			'<div class="palette-card">' +
				'<input class="palette-input" type="text" placeholder="Type a command — collection, page, or ✦ ask…" autocomplete="off">' +
				'<ul class="palette-list" role="listbox"></ul>' +
				'<div class="palette-foot muted">' +
					'<kbd>↑</kbd><kbd>↓</kbd> navigate · <kbd>↵</kbd> run · <kbd>esc</kbd> dismiss' +
				'</div>' +
			'</div>';
		p.addEventListener('click', function (e) { if (e.target === p) close(); });
		document.body.appendChild(p);
		return p;
	}

	var allItems = [], visibleItems = [], cursor = 0;

	function fuzzy(needle, hay) {
		if (!needle) return true;
		needle = needle.toLowerCase(); hay = hay.toLowerCase();
		var ni = 0;
		for (var i = 0; i < hay.length && ni < needle.length; i++) {
			if (hay[i] === needle[ni]) ni++;
		}
		return ni === needle.length;
	}

	function render(input) {
		var p = ensure();
		var ul = p.querySelector('.palette-list');
		var q = (input || '').trim();
		// Special prefix: starting with "?" routes everything to the LLM.
		var aiMode = q.startsWith('?');
		if (aiMode) q = q.slice(1).trimStart();

		visibleItems = aiMode
			? [{ label: '✦ Ask the LLM: "' + q + '"', kind: 'ask-now', q: q, cls: 'ai-link' }]
			: allItems.filter(function (it) { return fuzzy(q, it.label); });
		if (visibleItems.length === 0 && q !== '') {
			visibleItems = [{ label: 'Search "' + q + '"', kind: 'search', q: q, hint: '/search?q=' + encodeURIComponent(q) }];
		}
		cursor = 0;
		ul.innerHTML = '';
		visibleItems.forEach(function (it, i) {
			var li = document.createElement('li');
			li.className = 'palette-item' + (it.cls ? ' ' + it.cls : '') + (i === cursor ? ' active' : '');
			li.setAttribute('data-index', String(i));
			li.innerHTML = '<span class="palette-label"></span>' +
			               '<span class="palette-hint muted"></span>';
			li.querySelector('.palette-label').textContent = it.label;
			li.querySelector('.palette-hint').textContent  = it.hint || '';
			li.addEventListener('mouseenter', function () { setCursor(i); });
			li.addEventListener('click', function () { run(it); });
			ul.appendChild(li);
		});
	}

	function setCursor(i) {
		cursor = i;
		var p = ensure();
		p.querySelectorAll('.palette-item').forEach(function (el, idx) {
			el.classList.toggle('active', idx === i);
		});
	}

	function run(it) {
		if (!it) return;
		if (it.kind === 'ask' || it.kind === 'ask-now') {
			var q = (it.q || ensure().querySelector('.palette-input').value || '').trim();
			if (q.startsWith('?')) q = q.slice(1).trimStart();
			window.location.href = '/chat?ask=' + encodeURIComponent(q);
			return;
		}
		if (it.kind === 'search') {
			window.location.href = '/search?q=' + encodeURIComponent(it.q);
			return;
		}
		if (it.href) window.location.href = it.href;
	}

	function open() {
		var p = ensure();
		allItems = fixedItems().concat(collectionItems());
		render('');
		p.hidden = false;
		var inp = p.querySelector('.palette-input');
		inp.value = '';
		inp.focus();
	}

	function close() {
		var p = document.getElementById('palette');
		if (p) p.hidden = true;
	}

	ns.paletteOpen  = open;
	ns.paletteClose = close;

	document.addEventListener('keydown', function (e) {
		var open_ = function () { e.preventDefault(); ns.paletteOpen(); };
		// Cmd+K (mac) or Ctrl+K (others).
		if ((e.metaKey || e.ctrlKey) && e.key === 'k') return open_();
		var p = document.getElementById('palette');
		if (!p || p.hidden) return;
		if (e.key === 'Escape')    { e.preventDefault(); close(); return; }
		if (e.key === 'ArrowDown') { e.preventDefault(); setCursor(Math.min(cursor + 1, visibleItems.length - 1)); return; }
		if (e.key === 'ArrowUp')   { e.preventDefault(); setCursor(Math.max(cursor - 1, 0)); return; }
		if (e.key === 'Enter')     { e.preventDefault(); run(visibleItems[cursor]); return; }
	});

	document.addEventListener('input', function (e) {
		if (!e.target || e.target.classList === undefined) return;
		if (!e.target.classList.contains('palette-input')) return;
		render(e.target.value);
	});
})();
