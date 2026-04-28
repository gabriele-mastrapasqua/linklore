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
	"strings"
	"time"

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
	for _, item := range feed.Items {
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
