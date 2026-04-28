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

// DefaultUserAgent identifies linklore to upstream servers.
const DefaultUserAgent = "linklore/0.1 (+https://github.com/gabrielemastrapasqua/linklore)"

// MinReadableChars: if readability output is shorter than this we treat
// the page as JS-rendered / paywalled and fall back to a raw-body cleanup.
// Matches the config knob extract.min_readable_chars.
const MinReadableChars = 200

// Article holds everything Phase 3 produces for one URL.
type Article struct {
	URL         string
	Title       string
	Description string
	ImageURL    string
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
	req.Header.Set("Accept", "text/html,application/xhtml+xml")
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

