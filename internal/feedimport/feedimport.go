// Package feedimport pulls links into a collection from an RSS or Atom
// feed. The collection drives everything: when collection.feed_url is
// set, this package fetches that URL, dedupes by entry link, and
// inserts the missing ones via storage.CreateLinkIfMissing — the
// regular ingestion pipeline (worker fetch → extract → summary → tags)
// then takes over per link.
//
// Two entry points:
//
//   - Importer.RefreshOne(ctx, collectionID): one-shot. The HTTP
//     handler wires this to a "Refresh feed" button.
//   - Importer.PollAll(ctx): walks every feed-backed collection and
//     refreshes each one if it's been long enough since last check.
//     Called from a goroutine on app start with a 30-minute ticker.
package feedimport

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
	"github.com/gabrielemastrapasqua/linklore/internal/storage"
	"github.com/mmcdole/gofeed"
)

// Result reports what happened during one refresh.
type Result struct {
	CollectionID int64
	Added        int      // newly-created link rows
	Skipped      int      // entries whose URL was already in the collection
	Errors       []string // soft errors (one per problematic entry)
}

// Importer wires storage + a parser. The parser has its own internal
// HTTP client; we hand it a context so requests respect cancellation.
type Importer struct {
	store  *storage.Store
	parser *gofeed.Parser
	ttl    time.Duration // for PollAll: minimum delay between polls
}

func New(store *storage.Store) *Importer {
	p := gofeed.NewParser()
	p.UserAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 14_5) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.5 Safari/605.1.15"
	return &Importer{
		store:  store,
		parser: p,
		ttl:    30 * time.Minute,
	}
}

// SetTTL controls how stale "last_checked_at" must be before PollAll
// re-fetches a feed. Defaults to 30 minutes.
func (i *Importer) SetTTL(d time.Duration) { i.ttl = d }

// Discover takes an arbitrary URL — usually a site's homepage or
// article page — and returns the most likely RSS/Atom feed URL for
// that site. Strategy:
//
//  1. If the URL itself parses as a feed already, return it as-is.
//  2. Fetch the URL, look for <link rel="alternate" type="…rss+xml"
//     or "…atom+xml"> in the head, return the first absolute href.
//  3. Try a few well-known paths against the site root: /feed,
//     /feed/, /feed.xml, /rss, /rss.xml, /atom.xml, /index.xml.
//
// Returns ErrNoFeedFound when nothing works. The caller (HTTP handler)
// surfaces that as a "no feed detected — paste it manually" hint.
var ErrNoFeedFound = errors.New("no feed detected on this page")

func (i *Importer) Discover(ctx context.Context, pageURL string) (string, error) {
	pageURL = strings.TrimSpace(pageURL)
	if pageURL == "" {
		return "", errors.New("url required")
	}
	parsed, err := url.Parse(pageURL)
	if err != nil || parsed.Scheme == "" {
		return "", fmt.Errorf("invalid url: %s", pageURL)
	}

	// Step 1: maybe the URL IS already a feed.
	probeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	if _, perr := i.parser.ParseURLWithContext(pageURL, probeCtx); perr == nil {
		cancel()
		return pageURL, nil
	}
	cancel()

	// Step 2: scrape <link rel="alternate" …> from the page.
	if found := scrapeFeedLinks(ctx, pageURL, i.parser.UserAgent); found != "" {
		return found, nil
	}

	// Step 3: try well-known paths at the site root.
	root := &url.URL{Scheme: parsed.Scheme, Host: parsed.Host}
	for _, path := range []string{"/feed", "/feed/", "/feed.xml", "/rss", "/rss.xml", "/atom.xml", "/index.xml"} {
		candidate := root.ResolveReference(&url.URL{Path: path}).String()
		probeCtx2, cancel2 := context.WithTimeout(ctx, 8*time.Second)
		_, perr := i.parser.ParseURLWithContext(candidate, probeCtx2)
		cancel2()
		if perr == nil {
			return candidate, nil
		}
	}
	return "", ErrNoFeedFound
}

// scrapeFeedLinks fetches pageURL and walks the head looking for the
// canonical <link rel="alternate" type="application/(rss|atom)+xml">.
// Returns the first absolute href, or "" if none found.
func scrapeFeedLinks(ctx context.Context, pageURL, userAgent string) string {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, pageURL, nil)
	if err != nil {
		return ""
	}
	if userAgent != "" {
		req.Header.Set("User-Agent", userAgent)
	}
	req.Header.Set("Accept", "text/html,application/xhtml+xml")
	hc := &http.Client{Timeout: 12 * time.Second}
	resp, err := hc.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return ""
	}
	// Cap body so an HTML page that lies about being a feed can't OOM.
	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return ""
	}
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(string(body)))
	if err != nil {
		return ""
	}
	base, _ := url.Parse(pageURL)

	// Strict selectors first (the common case), looser fallback after.
	selectors := []string{
		`link[rel="alternate"][type="application/rss+xml"]`,
		`link[rel="alternate"][type="application/atom+xml"]`,
		`link[rel="alternate"][type="application/feed+json"]`,
		`link[rel="alternate"][type="application/json"]`,
	}
	for _, sel := range selectors {
		if href, ok := doc.Find(sel).First().Attr("href"); ok {
			abs := absoluteURL(base, strings.TrimSpace(href))
			if abs != "" {
				return abs
			}
		}
	}
	return ""
}

// absoluteURL resolves a possibly-relative href against base.
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

// RefreshOne fetches the feed for a single collection and imports any
// new entries. Caller is the HTTP "Refresh feed" handler.
func (i *Importer) RefreshOne(ctx context.Context, collectionID int64) (*Result, error) {
	col, err := i.store.GetCollectionBySlugByID(ctx, collectionID)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(col.FeedURL) == "" {
		return nil, errors.New("collection has no feed_url set")
	}
	return i.refresh(ctx, col)
}

// PollAll iterates every feed-backed collection and refreshes each one
// whose last_checked_at is older than ttl. Returns a slice of Result —
// one per refreshed feed. Skipped (still-fresh) collections are not
// included. Errors on individual feeds don't abort the loop.
func (i *Importer) PollAll(ctx context.Context) ([]Result, error) {
	cols, err := i.store.ListFeedCollections(ctx)
	if err != nil {
		return nil, err
	}
	var out []Result
	for _, c := range cols {
		col := c // capture for closures
		if col.LastCheckedAt != nil && time.Since(*col.LastCheckedAt) < i.ttl {
			continue
		}
		r, err := i.refresh(ctx, &col)
		if err != nil {
			out = append(out, Result{
				CollectionID: col.ID,
				Errors:       []string{err.Error()},
			})
			continue
		}
		out = append(out, *r)
	}
	return out, nil
}

// refresh is the shared implementation for both entry points.
func (i *Importer) refresh(ctx context.Context, col *storage.Collection) (*Result, error) {
	feedCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	feed, err := i.parser.ParseURLWithContext(col.FeedURL, feedCtx)
	if err != nil {
		return nil, fmt.Errorf("parse feed %s: %w", col.FeedURL, err)
	}
	r := &Result{CollectionID: col.ID}
	// gofeed returns items newest-first. We insert from oldest to
	// newest so the newest entry ends up with the highest order_idx
	// — and thus appears at the top of the collection page (which
	// orders by order_idx DESC, created_at DESC).
	for idx := len(feed.Items) - 1; idx >= 0; idx-- {
		item := feed.Items[idx]
		if item == nil || strings.TrimSpace(item.Link) == "" {
			continue
		}
		_, created, err := i.store.CreateLinkIfMissing(ctx, col.ID, item.Link)
		if err != nil {
			r.Errors = append(r.Errors, item.Link+": "+err.Error())
			continue
		}
		if created {
			r.Added++
		} else {
			r.Skipped++
		}
	}
	if err := i.store.MarkCollectionFeedChecked(ctx, col.ID); err != nil {
		r.Errors = append(r.Errors, "mark checked: "+err.Error())
	}
	return r, nil
}
