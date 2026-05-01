// F1 — Highlights selection UI for the drawer Preview tab.
//
// Flow:
//   1. User selects text inside .drawer-article.
//   2. We compute the (start, end) character offsets relative to the
//      article's plaintext and float a small "Highlight" button next
//      to the cursor.
//   3. On click, POST /links/{id}/highlights with the offsets, the
//      selected text, and an optional note.
//   4. The server-side render pipeline wraps the selection in <mark>
//      on the next preview load. For instant feedback we also wrap it
//      locally with surroundContents() — the canonical render replays
//      from the DB next time the drawer reopens.
//
// On click on an existing <mark.hl>: open a tiny edit popover with
// "delete" + "save note".

(function () {
	'use strict';
	var ns = (window.linklore = window.linklore || {});

	function articleEl() { return document.querySelector('#drawer-content .drawer-article'); }
	function linkID() {
		// Pull from the deeplink anchor in the drawer head — it carries
		// /links/{id}.
		var a = document.querySelector('#drawer-content .drawer-deeplink');
		if (!a) return 0;
		var m = (a.getAttribute('href') || '').match(/^\/links\/(\d+)/);
		return m ? Number(m[1]) : 0;
	}

	// offsetWithinArticle walks from the article root to `node` and
	// counts characters using textContent (matches what the server's
	// markdown→plaintext transform gives us, close enough for the
	// fuzzy fallback to recover from any drift).
	function offsetWithinArticle(article, node, nodeOffset) {
		var n = 0;
		var walker = document.createTreeWalker(article, NodeFilter.SHOW_TEXT, null);
		var cur;
		while ((cur = walker.nextNode())) {
			if (cur === node) return n + nodeOffset;
			n += cur.textContent.length;
		}
		return -1;
	}

	function showFloatingButton(rect, payload) {
		removeFloatingButton();
		var btn = document.createElement('button');
		btn.type = 'button';
		btn.id = 'hl-float';
		btn.className = 'hl-float';
		btn.textContent = '✎ Highlight';
		btn.style.position = 'fixed';
		btn.style.top = (rect.top - 36) + 'px';
		btn.style.left = Math.max(8, rect.left) + 'px';
		btn.addEventListener('click', function (e) {
			e.preventDefault(); e.stopPropagation();
			createHighlight(payload).then(removeFloatingButton);
		});
		document.body.appendChild(btn);
	}
	function removeFloatingButton() {
		var b = document.getElementById('hl-float');
		if (b && b.parentNode) b.parentNode.removeChild(b);
	}

	function createHighlight(p) {
		var lid = linkID();
		if (!lid) return Promise.resolve();
		var fd = new FormData();
		fd.set('start', String(p.start));
		fd.set('end', String(p.end));
		fd.set('text', p.text);
		return fetch('/links/' + lid + '/highlights', { method: 'POST', body: fd })
			.then(function (r) { return r.ok ? r.json() : null; })
			.then(function (data) {
				if (!data) return;
				// Local mark — wrap the live selection in a <mark.hl>. The
				// server will redo this on the next preview load.
				try {
					var sel = window.getSelection();
					if (sel && sel.rangeCount > 0) {
						var mark = document.createElement('mark');
						mark.className = 'hl';
						mark.dataset.hid = String(data.id);
						sel.getRangeAt(0).surroundContents(mark);
						sel.removeAllRanges();
					}
				} catch (_) { /* surroundContents fails across element
					boundaries — the server-side render will catch it on
					reload. Silent fallback. */ }
			});
	}

	function onSelection() {
		var article = articleEl();
		if (!article) return;
		var sel = window.getSelection();
		if (!sel || sel.isCollapsed) { removeFloatingButton(); return; }
		var range = sel.getRangeAt(0);
		if (!article.contains(range.startContainer) || !article.contains(range.endContainer)) {
			removeFloatingButton();
			return;
		}
		var text = sel.toString();
		if (!text || text.length < 3) { removeFloatingButton(); return; }
		var start = offsetWithinArticle(article, range.startContainer, range.startOffset);
		var end = offsetWithinArticle(article, range.endContainer, range.endOffset);
		if (start < 0 || end < 0 || end <= start) { removeFloatingButton(); return; }
		var rect = range.getBoundingClientRect();
		showFloatingButton(rect, { start: start, end: end, text: text });
	}

	function onMarkClick(e) {
		var mark = e.target.closest('mark.hl');
		if (!mark) return;
		e.preventDefault();
		var hid = mark.dataset.hid;
		if (!hid) return;
		// Lightweight inline action: shift-click or alt-click to delete
		// without a confirm. Plain click toggles a small "delete" button
		// next to the mark.
		if (e.shiftKey || e.altKey) {
			deleteHighlight(hid, mark);
			return;
		}
		showInlineDelete(mark, hid);
	}

	function showInlineDelete(mark, hid) {
		var existing = document.getElementById('hl-actions');
		if (existing) existing.remove();
		var box = document.createElement('span');
		box.id = 'hl-actions';
		box.className = 'hl-actions';
		var del = document.createElement('button');
		del.type = 'button';
		del.textContent = '× delete';
		del.addEventListener('click', function (e) {
			e.preventDefault(); e.stopPropagation();
			deleteHighlight(hid, mark);
			box.remove();
		});
		box.appendChild(del);
		mark.appendChild(box);
	}

	function deleteHighlight(hid, mark) {
		fetch('/highlights/' + hid, { method: 'DELETE' }).then(function () {
			// Unwrap the <mark> locally.
			while (mark.firstChild) mark.parentNode.insertBefore(mark.firstChild, mark);
			mark.parentNode.removeChild(mark);
		});
	}

	document.addEventListener('mouseup', function () {
		// Defer so the selection has time to settle (touch end → click).
		setTimeout(onSelection, 10);
	});
	document.addEventListener('selectionchange', function () {
		// Hide the float button when the selection collapses.
		var sel = window.getSelection();
		if (!sel || sel.isCollapsed) removeFloatingButton();
	});
	document.addEventListener('click', function (e) {
		if (e.target && e.target.closest && e.target.closest('mark.hl')) {
			onMarkClick(e);
		}
	});

	ns.highlightsRefresh = function () { /* placeholder for future */ };
})();
