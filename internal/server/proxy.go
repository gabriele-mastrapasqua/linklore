package server

import (
	"bytes"
	"context"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/gabriele-mastrapasqua/linklore/internal/extract"
)

// handleProxyWeb fetches a remote URL server-side and pipes the response
// back with framing headers (X-Frame-Options, CSP frame-ancestors) stripped
// so the page can render inside the Web preview iframe.
//
// The browser still sandboxes the iframe (sandbox attribute on the <iframe>
// tag), so even if the proxied page tries something hostile it cannot read
// our cookies / storage. We additionally strip Set-Cookie on the way out.
//
// Limits:
//   - GET only, http(s) schemes only
//   - 15s total deadline
//   - 8 MiB body cap
//   - private/loopback IPs refused (no SSRF into the host LAN)
func (s *Server) handleProxyWeb(w http.ResponseWriter, r *http.Request) {
	raw := r.URL.Query().Get("u")
	if raw == "" {
		http.Error(w, "missing u", http.StatusBadRequest)
		return
	}
	target, err := url.Parse(raw)
	if err != nil || (target.Scheme != "http" && target.Scheme != "https") || target.Host == "" {
		http.Error(w, "bad url", http.StatusBadRequest)
		return
	}
	if isPrivateHost(target.Hostname()) {
		http.Error(w, "refused", http.StatusForbidden)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		http.Error(w, "request: "+err.Error(), http.StatusBadGateway)
		return
	}
	req.Header.Set("User-Agent", extract.DefaultUserAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9,it;q=0.8")
	req.Header.Set("Accept-Encoding", "identity")

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		http.Error(w, "fetch: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	for _, h := range []string{
		"X-Frame-Options",
		"Content-Security-Policy",
		"Content-Security-Policy-Report-Only",
		"Cross-Origin-Opener-Policy",
		"Cross-Origin-Embedder-Policy",
		"Cross-Origin-Resource-Policy",
		"Set-Cookie",
		"Strict-Transport-Security",
	} {
		resp.Header.Del(h)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	w.Header().Set("Content-Security-Policy",
		"frame-ancestors 'self'; default-src * data: blob: 'unsafe-inline' 'unsafe-eval'")
	w.Header().Set("X-Frame-Options", "SAMEORIGIN")
	w.Header().Set("Referrer-Policy", "no-referrer")

	const maxBytes = 8 << 20
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxBytes))

	// Relative URLs (CSS, images, JS, fonts) need to resolve against the
	// upstream origin, not ours — otherwise the browser fetches
	// http://localhost:8080/static/foo.css and gets a 404. Inject a <base>
	// pointing at the original page so the browser does the resolution.
	if isHTML(resp.Header.Get("Content-Type")) {
		body = injectBase(body, target)
		// nojs=1 (default) neutralises <script> tags + the meta-refresh
		// redirects SPAs use to bounce a stale URL to "/404". Keeps CSS,
		// fonts, images intact — you see the SSR'd first paint, not the
		// router's runtime decision. nojs=0 turns scripts back on.
		if r.URL.Query().Get("nojs") != "0" {
			body = neutraliseScripts(body)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(body)
}

func isHTML(ct string) bool {
	ct = strings.ToLower(ct)
	return strings.Contains(ct, "text/html") || strings.Contains(ct, "application/xhtml")
}

// baseRe drops any existing <base ...> the page declared (a relative or
// path-only base would still resolve against our origin and re-break the
// page). We re-add a single absolute <base href="…"> ourselves.
var baseRe = regexp.MustCompile(`(?is)<base\b[^>]*>`)
var headRe = regexp.MustCompile(`(?is)<head\b[^>]*>`)

// neutraliseScripts comments out <script>…</script> blocks and disarms
// meta-refresh redirects so a SPA router can't re-route the proxied page
// off the SSR'd content. Inline event handlers (onclick=…) are left
// alone — they only fire on user interaction, which is fine for previews.
var (
	scriptRe        = regexp.MustCompile(`(?is)<script\b[^>]*>.*?</script\s*>`)
	scriptSelfRe    = regexp.MustCompile(`(?is)<script\b[^>]*/>`)
	metaRefreshRe   = regexp.MustCompile(`(?is)<meta\b[^>]*http-equiv\s*=\s*["']?refresh["']?[^>]*>`)
	noscriptOpenRe  = regexp.MustCompile(`(?is)<noscript\b[^>]*>`)
	noscriptCloseRe = regexp.MustCompile(`(?is)</noscript\s*>`)
)

func neutraliseScripts(body []byte) []byte {
	body = scriptRe.ReplaceAll(body, []byte("<!--script removed-->"))
	body = scriptSelfRe.ReplaceAll(body, []byte("<!--script removed-->"))
	body = metaRefreshRe.ReplaceAll(body, []byte("<!--refresh removed-->"))
	// Reveal <noscript> content — that's literally the no-JS fallback the
	// site author wrote for browsers without JS, which is now us.
	body = noscriptOpenRe.ReplaceAll(body, nil)
	body = noscriptCloseRe.ReplaceAll(body, nil)
	return body
}

func injectBase(body []byte, target *url.URL) []byte {
	tag := []byte(`<base href="` + html.EscapeString(target.String()) + `">`)
	body = baseRe.ReplaceAll(body, nil)
	if loc := headRe.FindIndex(body); loc != nil {
		var out bytes.Buffer
		out.Grow(len(body) + len(tag))
		out.Write(body[:loc[1]])
		out.Write(tag)
		out.Write(body[loc[1]:])
		return out.Bytes()
	}
	// No <head> — prepend so at least it's somewhere browsers honour.
	return append(tag, body...)
}

// isPrivateHost rejects loopback, link-local, and RFC1918 ranges so the
// proxy can't be turned into an SSRF probe against the host's network.
func isPrivateHost(host string) bool {
	if host == "" || strings.EqualFold(host, "localhost") {
		return true
	}
	ips, err := net.LookupIP(host)
	if err != nil || len(ips) == 0 {
		return false
	}
	for _, ip := range ips {
		if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() {
			return true
		}
	}
	return false
}
