// Tiny image lightbox. Click any preview <img> in a link row, in the
// detail page, or in the strip at the bottom of either, and you get
// a centred modal: bigger image + a button to open the original
// upstream URL in a new tab + close on outside-click / ESC / "×".
//
// Triggered by data-lightbox-src on the <img>. The link rows already
// render their <img>s wrapped in <a target="_blank">; we capture
// the click before the anchor navigates and instead open the modal.
// Hold ⌘ (Mac) / Ctrl to bypass the modal — click goes straight to
// the upstream image.

(function () {
	'use strict';

	function ensureModal() {
		var m = document.getElementById('lightbox-modal');
		if (m) return m;
		m = document.createElement('div');
		m.id = 'lightbox-modal';
		m.className = 'lightbox-modal';
		m.innerHTML = ''
			+ '<div class="lightbox-backdrop"></div>'
			+ '<div class="lightbox-frame">'
			+   '<button type="button" class="lightbox-close" aria-label="close">×</button>'
			+   '<img class="lightbox-img" alt="">'
			+   '<div class="lightbox-actions">'
			+     '<a class="lightbox-open" target="_blank" rel="noopener noreferrer">↗ open original</a>'
			+   '</div>'
			+ '</div>';
		document.body.appendChild(m);
		m.addEventListener('click', function (ev) {
			if (ev.target.classList.contains('lightbox-backdrop') ||
			    ev.target.classList.contains('lightbox-close')) {
				closeModal();
			}
		});
		document.addEventListener('keydown', function (ev) {
			if (ev.key === 'Escape') closeModal();
		});
		return m;
	}

	function openModal(src) {
		var m = ensureModal();
		m.querySelector('.lightbox-img').src = src;
		m.querySelector('.lightbox-open').href = src;
		m.classList.add('open');
		document.body.style.overflow = 'hidden';
	}

	function closeModal() {
		var m = document.getElementById('lightbox-modal');
		if (!m) return;
		m.classList.remove('open');
		m.querySelector('.lightbox-img').src = '';
		document.body.style.overflow = '';
	}

	// Match every <img> inside a .preview-strip / .preview-primary /
	// .detail-preview-primary / .detail-preview-strip — the four
	// places preview thumbs can show up. Inline favicons (16×16) are
	// skipped so a tiny site icon doesn't bloat the modal.
	var SELECTOR = '.preview-strip img, .preview-primary, .detail-preview-strip img, .detail-preview-primary';

	document.addEventListener('click', function (ev) {
		// Bypass with ⌘/Ctrl: let the wrapping anchor (target=_blank)
		// open the upstream image directly.
		if (ev.metaKey || ev.ctrlKey) return;
		var img = ev.target.closest(SELECTOR);
		if (!img) return;
		// Don't fight the favicon — it's tiny and not worth a modal.
		if (img.classList.contains('preview-favicon')) return;
		ev.preventDefault();
		ev.stopPropagation();
		openModal(img.src);
	}, true); // capture phase so we beat the <a> click navigation
})();
