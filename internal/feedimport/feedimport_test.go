package feedimport

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gabrielemastrapasqua/linklore/internal/storage"
)

const atomFixture = `<?xml version="1.0" encoding="UTF-8"?>
<feed xmlns="http://www.w3.org/2005/Atom">
  <title>Demo</title>
  <id>urn:demo</id>
  <updated>2026-04-28T12:00:00Z</updated>
  <entry>
    <title>First</title>
    <id>urn:demo:1</id>
    <link href="https://example.com/post-1"/>
    <updated>2026-04-28T12:00:00Z</updated>
  </entry>
  <entry>
    <title>Second</title>
    <id>urn:demo:2</id>
    <link href="https://example.com/post-2"/>
    <updated>2026-04-28T11:00:00Z</updated>
  </entry>
</feed>`

func newFeedServer(body string, hits *int) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if hits != nil {
			*hits++
		}
		w.Header().Set("Content-Type", "application/atom+xml")
		fmt.Fprint(w, body)
	}))
}

func openMem(t *testing.T) *storage.Store {
	t.Helper()
	st, err := storage.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func TestRefreshOne_addsNewEntriesAndDedupes(t *testing.T) {
	st := openMem(t)
	col, _ := st.CreateCollection(context.Background(), "feed", "Feed", "")

	srv := newFeedServer(atomFixture, nil)
	defer srv.Close()
	if err := st.SetCollectionFeed(context.Background(), col.ID, srv.URL); err != nil {
		t.Fatal(err)
	}

	imp := New(st)
	r, err := imp.RefreshOne(context.Background(), col.ID)
	if err != nil {
		t.Fatal(err)
	}
	if r.Added != 2 || r.Skipped != 0 || len(r.Errors) != 0 {
		t.Errorf("first run: %+v", r)
	}
	links, _ := st.ListLinksByCollection(context.Background(), col.ID, 100, 0)
	if len(links) != 2 {
		t.Fatalf("links = %d", len(links))
	}

	// Re-running must dedupe on URL — no new rows.
	r, err = imp.RefreshOne(context.Background(), col.ID)
	if err != nil {
		t.Fatal(err)
	}
	if r.Added != 0 || r.Skipped != 2 {
		t.Errorf("rerun: %+v", r)
	}
	links2, _ := st.ListLinksByCollection(context.Background(), col.ID, 100, 0)
	if len(links2) != 2 {
		t.Errorf("dedupe failed: %d links", len(links2))
	}

	// last_checked_at populated.
	got, _ := st.GetCollectionBySlugByID(context.Background(), col.ID)
	if got.LastCheckedAt == nil {
		t.Error("last_checked_at not set after refresh")
	}
}

func TestRefreshOne_rejectsCollectionWithoutFeed(t *testing.T) {
	st := openMem(t)
	col, _ := st.CreateCollection(context.Background(), "x", "X", "")
	if _, err := New(st).RefreshOne(context.Background(), col.ID); err == nil {
		t.Error("expected error when feed_url is unset")
	}
}

func TestPollAll_skipsRecentlyCheckedFeeds(t *testing.T) {
	st := openMem(t)
	colA, _ := st.CreateCollection(context.Background(), "a", "A", "")
	colB, _ := st.CreateCollection(context.Background(), "b", "B", "")

	hitsA := 0
	hitsB := 0
	srvA := newFeedServer(atomFixture, &hitsA)
	defer srvA.Close()
	srvB := newFeedServer(atomFixture, &hitsB)
	defer srvB.Close()
	st.SetCollectionFeed(context.Background(), colA.ID, srvA.URL)
	st.SetCollectionFeed(context.Background(), colB.ID, srvB.URL)
	// A was just checked; B is fresh and should poll.
	st.MarkCollectionFeedChecked(context.Background(), colA.ID)

	imp := New(st)
	imp.SetTTL(10 * time.Minute)
	results, err := imp.PollAll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// Only B should have been refreshed.
	if hitsA != 0 {
		t.Errorf("A re-fetched despite recent check: %d", hitsA)
	}
	if hitsB != 1 {
		t.Errorf("B not fetched: %d", hitsB)
	}
	if len(results) != 1 || results[0].CollectionID != colB.ID {
		t.Errorf("results = %+v", results)
	}
}

func TestRefreshOne_badFeedReturnsError(t *testing.T) {
	st := openMem(t)
	col, _ := st.CreateCollection(context.Background(), "broken", "Broken", "")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()
	st.SetCollectionFeed(context.Background(), col.ID, srv.URL)

	if _, err := New(st).RefreshOne(context.Background(), col.ID); err == nil {
		t.Error("expected error on 500 from feed server")
	}
}
