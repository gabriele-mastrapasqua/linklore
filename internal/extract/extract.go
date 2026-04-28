// Package extract turns a URL (or raw HTML) into the structured fields
// linklore stores per link: title, description, image URL, and a clean
// Markdown body suitable for both display and LLM consumption.
//
// Layered strategy:
//  1. HTTP GET (with timeout + UA) — Fetch.
//  2. Readability — Readable; if its output is shorter than MinReadableChars
//     we fall back to raw <body> stripped of nav/script/style.
//  3. HTML → Markdown via JohannesKaufmann/html-to-markdown.
//  4. OG/twitter/<meta> tags scraped with goquery for image+description+title.
//
// chromedp/headless is intentionally NOT here. It's a separate add-on hooked
// behind an explicit config flag; Phase 3 lands the deterministic core only.
package extract

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	htm "github.com/JohannesKaufmann/html-to-markdown/v2"
	"github.com/PuerkitoBio/goquery"
	readability "github.com/go-shiori/go-readability"
)

// DefaultUserAgent looks like a real desktop browser. Many sites
// (Reddit, Twitter, Cloudflare-fronted blogs) serve an interstitial
// "please verify" page when they see a bot UA, so a custom string
// makes extraction useless. We mimic a stable, cache-friendly Safari
// UA that's commonly served the real article HTML.
const DefaultUserAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 14_5) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.5 Safari/605.1.15"

// MinReadableChars: if readability output is shorter than this we treat
// the page as JS-rendered / paywalled and fall back to a raw-body cleanup.
// Matches the config knob extract.min_readable_chars.
const MinReadableChars = 200

// Article holds everything Phase 3 produces for one URL.
type Article struct {
	URL         string
	Title       string
	Description string
	ImageURL    string   // primary image (og:image / twitter:image)
	ExtraImages []string // additional images found in the article body / page
	FaviconURL  string   // resolved favicon URL (icon, shortcut icon, apple-touch-icon)
	ContentMD   string
	RawHTML     string // kept around for optional gzip archiving by the worker
}

// Fetcher abstracts the HTTP layer so tests can swap in a fixture server.
type Fetcher struct {
	Client    *http.Client
	UserAgent string
}

// NewFetcher returns a Fetcher with sensible defaults (timeout, UA, redirects).
func NewFetcher(timeout time.Duration) *Fetcher {
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	return &Fetcher{
		Client:    &http.Client{Timeout: timeout},
		UserAgent: DefaultUserAgent,
	}
}

// Fetch performs a GET and returns the response body as a string. It refuses
// non-2xx responses so the caller can record a fetch_error promptly.
func (f *Fetcher) Fetch(ctx context.Context, url string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", fmt.Errorf("new request: %w", err)
	}
	req.Header.Set("User-Agent", f.UserAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9,it;q=0.8")
	req.Header.Set("Accept-Encoding", "identity") // skip gzip — net/http handles transparently otherwise
	req.Header.Set("Cache-Control", "no-cache")
	resp, err := f.Client.Do(req)
	if err != nil {
		return "", fmt.Errorf("get %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("status %d from %s", resp.StatusCode, url)
	}
	// Cap body size to avoid pathological pages eating memory.
	const maxBytes = 5 << 20 // 5 MiB
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes))
	if err != nil {
		return "", fmt.Errorf("read body: %w", err)
	}
	return string(body), nil
}

// Extract is the package's main entry point: HTML+URL → Article.
// It is pure (no network) so tests can pass canned HTML fixtures.
func Extract(html, sourceURL string) (*Article, error) {
	if strings.TrimSpace(html) == "" {
		return nil, errors.New("empty html")
	}
	a := &Article{URL: sourceURL, RawHTML: html}

	// Readability primary path. parseURL is best-effort: relative-link
	// resolution gracefully degrades when sourceURL is empty/invalid.
	parsed, _ := url.Parse(sourceURL)
	art, rerr := readability.FromReader(strings.NewReader(html), parsed)
	if rerr == nil {
		a.Title = strings.TrimSpace(art.Title)
		a.ImageURL = art.Image
		a.Description = strings.TrimSpace(art.Excerpt)
		md, mderr := convertHTML(art.Content)
		if mderr == nil {
			a.ContentMD = strings.TrimSpace(md)
		}
	}

	// Always inspect <meta> tags — readability sometimes drops the OG image.
	if doc, derr := goquery.NewDocumentFromReader(strings.NewReader(html)); derr == nil {
		applyMetaFallbacks(doc, a)
		a.FaviconURL = extractFavicon(doc, parsed)
		a.ExtraImages = extractImages(doc, parsed, a.ImageURL)
	}

	// Fallback if readability bailed out or produced too little.
	if len(a.ContentMD) < MinReadableChars {
		bodyMD, err := stripAndConvertBody(html)
		if err == nil && len(bodyMD) > len(a.ContentMD) {
			a.ContentMD = bodyMD
		}
	}

	if a.ContentMD == "" && a.Title == "" {
		return nil, errors.New("no extractable content")
	}
	return a, nil
}

// applyMetaFallbacks fills any blank Article field from <meta> tags.
// Order: og:* → twitter:* → <title> / <meta name="description">.
func applyMetaFallbacks(doc *goquery.Document, a *Article) {
	if a.Title == "" {
		if v := metaContent(doc, `meta[property="og:title"]`); v != "" {
			a.Title = v
		} else if v := metaContent(doc, `meta[name="twitter:title"]`); v != "" {
			a.Title = v
		} else {
			a.Title = strings.TrimSpace(doc.Find("title").First().Text())
		}
	}
	if a.Description == "" {
		if v := metaContent(doc, `meta[property="og:description"]`); v != "" {
			a.Description = v
		} else if v := metaContent(doc, `meta[name="description"]`); v != "" {
			a.Description = v
		}
	}
	if a.ImageURL == "" {
		if v := metaContent(doc, `meta[property="og:image"]`); v != "" {
			a.ImageURL = v
		} else if v := metaContent(doc, `meta[name="twitter:image"]`); v != "" {
			a.ImageURL = v
		}
	}
}

func metaContent(doc *goquery.Document, sel string) string {
	v, _ := doc.Find(sel).First().Attr("content")
	return strings.TrimSpace(v)
}

// extractFavicon walks the typical <link rel="..."> declarations in the
// document head and returns an absolute URL, or "" if nothing usable.
// We don't download — the caller stores only the URL and renders it
// straight from the upstream host.
func extractFavicon(doc *goquery.Document, base *url.URL) string {
	// Selectors in priority order. The first match wins.
	selectors := []string{
		`link[rel="apple-touch-icon"]`,
		`link[rel="icon"]`,
		`link[rel="shortcut icon"]`,
		`link[rel="mask-icon"]`,
	}
	for _, sel := range selectors {
		if href, ok := doc.Find(sel).First().Attr("href"); ok {
			if abs := absoluteURL(base, strings.TrimSpace(href)); abs != "" {
				return abs
			}
		}
	}
	// Last-resort: /favicon.ico at the site root.
	if base != nil && base.Host != "" {
		return base.Scheme + "://" + base.Host + "/favicon.ico"
	}
	return ""
}

// extractImages returns up to ~6 distinct image URLs from the page body,
// excluding the primary one (already in Article.ImageURL). Sources:
// inline <img>, og:image:additional, twitter:image:src duplicates skipped.
// Tiny tracking pixels (1×1) and data: URIs are filtered out.
func extractImages(doc *goquery.Document, base *url.URL, primary string) []string {
	const maxImages = 6
	seen := map[string]struct{}{}
	if primary != "" {
		seen[primary] = struct{}{}
	}
	out := make([]string, 0, maxImages)

	add := func(raw string) bool {
		raw = strings.TrimSpace(raw)
		if raw == "" || strings.HasPrefix(raw, "data:") {
			return false
		}
		abs := absoluteURL(base, raw)
		if abs == "" {
			return false
		}
		if _, dup := seen[abs]; dup {
			return false
		}
		seen[abs] = struct{}{}
		out = append(out, abs)
		return len(out) >= maxImages
	}

	// Additional og:image declarations (some pages emit several).
	doc.Find(`meta[property="og:image"], meta[property="og:image:url"], meta[name="twitter:image"]`).
		EachWithBreak(func(_ int, sel *goquery.Selection) bool {
			v, _ := sel.Attr("content")
			return !add(v)
		})
	if len(out) >= maxImages {
		return out
	}

	// Inline article images. We crudely filter by size attribute when the
	// page bothers to set one — that catches most tracking pixels.
	doc.Find("article img, main img, .post img, .entry img, body img").
		EachWithBreak(func(_ int, sel *goquery.Selection) bool {
			if w, ok := sel.Attr("width"); ok {
				if w == "1" || w == "0" {
					return true
				}
			}
			if src, ok := sel.Attr("src"); ok {
				return !add(src)
			}
			return true
		})
	return out
}

// absoluteURL resolves possibly-relative href against base. Returns "" when
// the href is empty or unparseable.
func absoluteURL(base *url.URL, href string) string {
	if href == "" {
		return ""
	}
	u, err := url.Parse(href)
	if err != nil {
		return ""
	}
	if u.IsAbs() {
		return u.String()
	}
	if base == nil {
		return ""
	}
	return base.ResolveReference(u).String()
}

// stripAndConvertBody drops navs/scripts/styles from <body> and converts the
// rest to Markdown. Used when readability fails or yields a stub.
func stripAndConvertBody(html string) (string, error) {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(html))
	if err != nil {
		return "", err
	}
	doc.Find("script, style, nav, header, footer, aside, noscript, iframe, form").Remove()
	body, err := doc.Find("body").Html()
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(body) == "" {
		body, _ = doc.Html() // last resort
	}
	return convertHTML(body)
}

func convertHTML(s string) (string, error) {
	out, err := htm.ConvertString(s)
	if err != nil {
		return "", err
	}
	return collapseBlankLines(strings.TrimSpace(out)), nil
}

// collapseBlankLines squashes runs of >2 blank lines to exactly one — keeps
// the markdown compact when sending to the LLM context window.
func collapseBlankLines(s string) string {
	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	blanks := 0
	for _, l := range lines {
		if strings.TrimSpace(l) == "" {
			blanks++
			if blanks > 1 {
				continue
			}
		} else {
			blanks = 0
		}
		out = append(out, l)
	}
	return strings.Join(out, "\n")
}

