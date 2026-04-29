// Cmd/Ctrl+K command palette. Centred modal with a fuzzy-filterable
// list combining (1) every collection in the sidebar, (2) live FTS link
// hits, (3) slash-prefixed commands, and (4) a fixed set of navigation
// + AI commands. Arrow keys navigate, ↵ runs.
//
// Bootstrap is purely client-side: collections come from the sidebar
// DOM, fixed commands are baked in here. Free-text input also fires a
// /search/live request to surface link hits inline.

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

	// Slash-command catalogue. The `match` function decides whether the
	// command should appear for a given trimmed input (still has the
	// leading slash, e.g. "/ask why"). The `run` builds the URL or
	// performs the action.
	function slashCommands() {
		return [
			{
				cmd: '/ask', label: '/ask <q>', hint: 'open chat with prefilled question', cls: 'ai-link',
				match: function (q) { return q === '/' || q.indexOf('/ask') === 0 || '/ask'.indexOf(q) === 0; },
				run: function (q) {
					var rest = q.replace(/^\/ask\s*/, '').trim();
					window.location.href = '/chat' + (rest ? '?ask=' + encodeURIComponent(rest) : '');
				},
			},
			{
				cmd: '/new', label: '/new <name>', hint: 'create a new collection',
				match: function (q) { return q === '/' || q.indexOf('/new') === 0 || '/new'.indexOf(q) === 0; },
				run: function (q) {
					var name = q.replace(/^\/new\s*/, '').trim();
					if (!name) { window.location.href = '/#new-collection'; return; }
					var fd = new FormData();
					fd.append('name', name);
					fetch('/collections', { method: 'POST', body: fd, headers: { 'HX-Request': 'true' } })
						.then(function () { window.location.href = '/'; })
						.catch(function () { window.location.href = '/#new-collection'; });
				},
			},
			{
				cmd: '/duplicates', label: '/duplicates', hint: '/duplicates',
				match: function (q) { return q === '/' || '/duplicates'.indexOf(q) === 0 || q.indexOf('/dup') === 0; },
				run: function () { window.location.href = '/duplicates'; },
			},
			{
				cmd: '/tags', label: '/tags', hint: '/tags',
				match: function (q) { return q === '/' || '/tags'.indexOf(q) === 0; },
				run: function () { window.location.href = '/tags'; },
			},
			{
				cmd: '/chat', label: '/chat', hint: '/chat', cls: 'ai-link',
				match: function (q) { return q === '/' || '/chat'.indexOf(q) === 0; },
				run: function () { window.location.href = '/chat'; },
			},
			{
				cmd: '/bookmarklet', label: '/bookmarklet', hint: '/bookmarklet',
				match: function (q) { return q === '/' || '/bookmarklet'.indexOf(q) === 0 || q.indexOf('/book') === 0; },
				run: function () { window.location.href = '/bookmarklet'; },
			},
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
				'<input class="palette-input" type="text" placeholder="Type a command — collection, page, /slash, or ✦ ask…" autocomplete="off">' +
				'<ul class="palette-list" role="listbox"></ul>' +
				'<div class="palette-foot muted">' +
					'<kbd>↑</kbd><kbd>↓</kbd> navigate · <kbd>↵</kbd> run · <kbd>esc</kbd> dismiss' +
				'</div>' +
			'</div>';
		// Click outside the card (anywhere on the dim backdrop) closes
		// the palette. The card itself swallows clicks via .closest().
		p.addEventListener('click', function (e) {
			if (!e.target.closest('.palette-card')) close();
		});
		document.body.appendChild(p);
		return p;
	}

	var allItems = [], visibleItems = [], cursor = 0;
	var ftsAbort = null;
	var ftsToken = 0;

	function fuzzy(needle, hay) {
		if (!needle) return true;
		needle = needle.toLowerCase(); hay = hay.toLowerCase();
		var ni = 0;
		for (var i = 0; i < hay.length && ni < needle.length; i++) {
			if (hay[i] === needle[ni]) ni++;
		}
		return ni === needle.length;
	}

	function paint(items) {
		var p = ensure();
		var ul = p.querySelector('.palette-list');
		visibleItems = items;
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

	// Pull the top N link hits out of the search_results fragment HTML.
	function parseFTSHits(html, limit) {
		var out = [];
		try {
			var doc = new DOMParser().parseFromString(html, 'text/html');
			var rows = doc.querySelectorAll('.link-row');
			for (var i = 0; i < rows.length && out.length < limit; i++) {
				var a = rows[i].querySelector('.title a[href]');
				if (!a) continue;
				var url = (rows[i].querySelector('.url') || {}).textContent || '';
				out.push({
					label: '🔗 ' + (a.textContent.trim() || a.getAttribute('href')),
					hint: url.trim(),
					href: a.getAttribute('href'),
				});
			}
		} catch (e) { /* defensive: drop FTS section silently */ }
		return out;
	}

	function fetchFTS(q) {
		var token = ++ftsToken;
		if (ftsAbort) { try { ftsAbort.abort(); } catch (e) {} }
		var ac = (typeof AbortController === 'function') ? new AbortController() : null;
		ftsAbort = ac;
		fetch('/search/live?q=' + encodeURIComponent(q), {
			signal: ac ? ac.signal : undefined,
			headers: { 'HX-Request': 'true' },
		})
			.then(function (r) { return r.ok ? r.text() : ''; })
			.then(function (html) {
				if (token !== ftsToken) return; // stale response — discard
				var hits = parseFTSHits(html, 5);
				if (hits.length) render(currentInput(), hits);
			})
			.catch(function () { /* network/abort: skip silently */ });
	}

	function currentInput() {
		var p = document.getElementById('palette');
		if (!p) return '';
		var inp = p.querySelector('.palette-input');
		return inp ? inp.value : '';
	}

	function render(input, ftsHits) {
		var p = ensure();
		var raw = (input || '');
		var q = raw.trim();

		// Special prefix: starting with "?" routes everything to the LLM.
		if (q.startsWith('?')) {
			var aq = q.slice(1).trimStart();
			paint([{ label: '✦ Ask the LLM: "' + aq + '"', kind: 'ask-now', q: aq, cls: 'ai-link' }]);
			return;
		}

		// Slash mode: surface matching slash commands first, nothing else
		// fuzzy-matched (would dilute the discoverability).
		if (q.startsWith('/')) {
			var slashes = slashCommands().filter(function (s) { return s.match(q); });
			var items = slashes.map(function (s) {
				return { label: s.label, hint: s.hint, kind: 'slash', cmd: s.cmd, run: s.run, cls: s.cls };
			});
			if (items.length === 0) {
				items = [{ label: 'Search "' + q + '"', kind: 'search', q: q, hint: '/search?q=' + encodeURIComponent(q) }];
			}
			paint(items);
			return;
		}

		// Free-text mode: slash-command stubs (when input is empty we
		// already showed allItems) → FTS hits → collection fuzzy →
		// fixed nav → empty fallback.
		var sections = [];

		// Slash command stubs only when something has been typed AND it
		// fuzzy-matches a slash command's name (rare, but lets users
		// discover them by typing e.g. "ask").
		if (q !== '') {
			var slashStubs = slashCommands().filter(function (s) {
				return fuzzy(q, s.cmd);
			}).map(function (s) {
				return { label: s.label, hint: s.hint, kind: 'slash', cmd: s.cmd, run: s.run, cls: s.cls };
			});
			sections = sections.concat(slashStubs);
		}

		if (ftsHits && ftsHits.length) sections = sections.concat(ftsHits);

		var collections = collectionItems().filter(function (it) { return fuzzy(q, it.label); });
		sections = sections.concat(collections);

		var fixed = fixedItems().filter(function (it) { return fuzzy(q, it.label); });
		sections = sections.concat(fixed);

		if (sections.length === 0 && q !== '') {
			sections = [{ label: 'Search "' + q + '"', kind: 'search', q: q, hint: '/search?q=' + encodeURIComponent(q) }];
		}
		paint(sections);

		// Kick off (or re-kick) the FTS fetch when we have a non-trivial
		// query and weren't called with hits already in hand.
		if (!ftsHits && q.length >= 2) fetchFTS(q);
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
		if (it.kind === 'slash' && typeof it.run === 'function') {
			it.run(currentInput().trim());
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

	// Keydown: capture-phase so the palette gets first crack at Escape /
	// Enter / arrows BEFORE keys.js or any other listener can swallow
	// the event (keys.js's bulkClear-on-esc was eating our dismiss).
	document.addEventListener('keydown', function (e) {
		// Cmd+K (mac) or Ctrl+K (others) — open the palette.
		if ((e.metaKey || e.ctrlKey) && e.key === 'k') {
			e.preventDefault();
			e.stopImmediatePropagation();
			ns.paletteOpen();
			return;
		}
		var p = document.getElementById('palette');
		if (!p || p.hidden) return;
		if (e.key === 'Escape')    { e.preventDefault(); e.stopImmediatePropagation(); close(); return; }
		if (e.key === 'ArrowDown') { e.preventDefault(); e.stopImmediatePropagation(); setCursor(Math.min(cursor + 1, visibleItems.length - 1)); return; }
		if (e.key === 'ArrowUp')   { e.preventDefault(); e.stopImmediatePropagation(); setCursor(Math.max(cursor - 1, 0)); return; }
		if (e.key === 'Enter')     { e.preventDefault(); e.stopImmediatePropagation(); run(visibleItems[cursor]); return; }
	}, true);

	document.addEventListener('input', function (e) {
		if (!e.target || e.target.classList === undefined) return;
		if (!e.target.classList.contains('palette-input')) return;
		render(e.target.value);
	});
})();
