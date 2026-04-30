// Package feed renders an Atom feed of the latest links in a collection.
// Subscribing to your own collection from any reader is a surprisingly
// useful way to revisit saved items — the cost is one route + ~30 lines.
package feed

import (
	"context"
	"fmt"
	"time"

	"github.com/gabriele-mastrapasqua/linklore/internal/storage"
	"github.com/gorilla/feeds"
)

// Builder owns the storage handle.
type Builder struct {
	store *storage.Store
}

func New(store *storage.Store) *Builder { return &Builder{store: store} }

// Atom returns the serialised <feed> XML for a collection. limit caps the
// number of entries (default 50). siteURL is the public base used for
// permalinks; pass an empty string when running purely on localhost.
func (b *Builder) Atom(ctx context.Context, slug, siteURL string, limit int) (string, error) {
	col, err := b.store.GetCollectionBySlug(ctx, slug)
	if err != nil {
		return "", err
	}
	if limit <= 0 {
		limit = 50
	}
	links, err := b.store.ListLinksByCollection(ctx, col.ID, limit, 0)
	if err != nil {
		return "", err
	}

	feed := &feeds.Feed{
		Title:       fmt.Sprintf("linklore — %s", col.Name),
		Description: col.Description,
		Link:        &feeds.Link{Href: siteURL + "/c/" + col.Slug},
		Created:     col.CreatedAt,
	}
	for _, l := range links {
		title := l.Title
		if title == "" {
			title = l.URL
		}
		desc := l.Summary
		if desc == "" {
			desc = l.Description
		}
		created := l.CreatedAt
		if l.FetchedAt != nil {
			created = *l.FetchedAt
		}
		feed.Items = append(feed.Items, &feeds.Item{
			Id:          fmt.Sprintf("%s/links/%d", siteURL, l.ID),
			Title:       title,
			Link:        &feeds.Link{Href: l.URL},
			Description: desc,
			Created:     created,
		})
	}
	if len(feed.Items) > 0 {
		// Per Atom: <updated> = max(item.updated). Items are oldest-last.
		feed.Updated = feed.Items[0].Created
	} else {
		feed.Updated = time.Now().UTC()
	}
	return feed.ToAtom()
}
