// Tiny toast widget. Fired either by JS calls (linklore.toast(msg, kind))
// or by HTMX HX-Trigger response header carrying a "linklore-toast"
// event with detail = {message, kind}. Auto-dismisses after 3.5s,
// click to dismiss early. No timing-dependent state so multiple
// toasts stack cleanly.

(function () {
	'use strict';
	var ns = (window.linklore = window.linklore || {});
	var TIMEOUT_MS = 3500;

	function ensureContainer() {
		var c = document.getElementById('toasts');
		if (c) return c;
		c = document.createElement('div');
		c.id = 'toasts';
		c.className = 'toasts';
		document.body.appendChild(c);
		return c;
	}

	ns.toast = function (message, kind) {
		if (!message) return;
		kind = kind || 'info';
		var t = document.createElement('div');
		t.className = 'toast toast-' + kind;
		t.textContent = message;
		t.addEventListener('click', function () { dismiss(t); });
		ensureContainer().appendChild(t);
		// Trigger CSS slide-in on next paint.
		requestAnimationFrame(function () { t.classList.add('toast-shown'); });
		setTimeout(function () { dismiss(t); }, TIMEOUT_MS);
	};

	function dismiss(t) {
		if (!t || t.classList.contains('toast-leaving')) return;
		t.classList.add('toast-leaving');
		setTimeout(function () { if (t.parentNode) t.parentNode.removeChild(t); }, 300);
	}

	// HX-Trigger: '{"linklore-toast":{"message":"…","kind":"ok"}}' on the
	// server side fires this here. HTMX dispatches it as a CustomEvent.
	document.body.addEventListener('linklore-toast', function (e) {
		var d = e.detail || {};
		ns.toast(d.message || String(d), d.kind);
	});
})();
