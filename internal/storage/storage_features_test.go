// Storage tests for the feature batch (layout, kind classifier,
// duplicates-helper ListAllLinks, manual-cascade DeleteCollection).
package storage

import (
	"context"
	"testing"
)

func openMemForFeatures(t *testing.T) *Store {
	t.Helper()
	st, err := Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func TestSetCollectionLayout_acceptedAndRejected(t *testing.T) {
	st := openMemForFeatures(t)
	col, _ := st.CreateCollection(context.Background(), "v", "V", "")
	for _, l := range []string{"list", "grid", "headlines", "moodboard"} {
		if err := st.SetCollectionLayout(context.Background(), col.ID, l); err != nil {
			t.Errorf("layout %s: %v", l, err)
		}
		got, _ := st.GetCollectionBySlugByID(context.Background(), col.ID)
		if got.Layout != l {
			t.Errorf("Layout = %q, want %q", got.Layout, l)
		}
	}
	if err := st.SetCollectionLayout(context.Background(), col.ID, "weird"); err == nil {
		t.Error("expected error for unsupported layout")
	}
	if err := st.SetCollectionLayout(context.Background(), 99999, "list"); err != ErrNotFound {
		t.Errorf("expected ErrNotFound for missing collection, got %v", err)
	}
}

func TestCreateLink_setsKindFromURL(t *testing.T) {
	st := openMemForFeatures(t)
	col, _ := st.CreateCollection(context.Background(), "c", "C", "")
	cases := []struct {
		url, kind string
	}{
		{"https://www.youtube.com/watch?v=x", "video"},
		{"https://example.com/foo.pdf", "document"},
		{"https://example.com/post", "article"},
	}
	for _, c := range cases {
		l, err := st.CreateLink(context.Background(), col.ID, c.url)
		if err != nil {
			t.Fatal(err)
		}
		if l.Kind != c.kind {
			t.Errorf("kind for %q = %q, want %q", c.url, l.Kind, c.kind)
		}
		got, _ := st.GetLink(context.Background(), l.ID)
		if got.Kind != c.kind {
			t.Errorf("persisted kind for %q = %q", c.url, got.Kind)
		}
	}
}

func TestSetCollectionCover_setAndClear(t *testing.T) {
	st := openMemForFeatures(t)
	col, _ := st.CreateCollection(context.Background(), "c", "C", "")
	if err := st.SetCollectionCover(context.Background(), col.ID, "https://example.com/banner.jpg"); err != nil {
		t.Fatal(err)
	}
	got, _ := st.GetCollectionBySlugByID(context.Background(), col.ID)
	if got.CoverURL != "https://example.com/banner.jpg" {
		t.Errorf("CoverURL = %q", got.CoverURL)
	}
	if err := st.SetCollectionCover(context.Background(), col.ID, ""); err != nil {
		t.Fatal(err)
	}
	got, _ = st.GetCollectionBySlugByID(context.Background(), col.ID)
	if got.CoverURL != "" {
		t.Errorf("CoverURL not cleared: %q", got.CoverURL)
	}
	if err := st.SetCollectionCover(context.Background(), 99999, "x"); err != ErrNotFound {
		t.Errorf("expected ErrNotFound for missing id, got %v", err)
	}
}

func TestSetLinkKind_overrides(t *testing.T) {
	st := openMemForFeatures(t)
	col, _ := st.CreateCollection(context.Background(), "c", "C", "")
	l, _ := st.CreateLink(context.Background(), col.ID, "https://example.com/whatever")
	if err := st.SetLinkKind(context.Background(), l.ID, "audio"); err != nil {
		t.Fatal(err)
	}
	got, _ := st.GetLink(context.Background(), l.ID)
	if got.Kind != "audio" {
		t.Errorf("kind = %q, want audio", got.Kind)
	}
}

func TestListAllLinks_returnsEverything(t *testing.T) {
	st := openMemForFeatures(t)
	a, _ := st.CreateCollection(context.Background(), "a", "A", "")
	b, _ := st.CreateCollection(context.Background(), "b", "B", "")
	st.CreateLink(context.Background(), a.ID, "https://example.com/a1")
	st.CreateLink(context.Background(), a.ID, "https://example.com/a2")
	st.CreateLink(context.Background(), b.ID, "https://example.com/b1")
	got, err := st.ListAllLinks(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Errorf("got %d, want 3", len(got))
	}
}

func TestUpdateLinkCheck(t *testing.T) {
	st := openMemForFeatures(t)
	col, _ := st.CreateCollection(context.Background(), "c", "C", "")
	l, _ := st.CreateLink(context.Background(), col.ID, "https://example.com/x")

	// Initially nothing recorded.
	got, err := st.GetLink(context.Background(), l.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.LastCheckStatus != "" || got.LastCheckCode != 0 || got.LastCheckAt != nil {
		t.Errorf("fresh link has check state: status=%q code=%d at=%v",
			got.LastCheckStatus, got.LastCheckCode, got.LastCheckAt)
	}

	if err := st.UpdateLinkCheck(context.Background(), l.ID, "broken", 404); err != nil {
		t.Fatal(err)
	}
	got, err = st.GetLink(context.Background(), l.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.LastCheckStatus != "broken" {
		t.Errorf("status = %q, want broken", got.LastCheckStatus)
	}
	if got.LastCheckCode != 404 {
		t.Errorf("code = %d, want 404", got.LastCheckCode)
	}
	if got.LastCheckAt == nil {
		t.Errorf("last_check_at not persisted")
	}

	// CountLinkChecks aggregates correctly.
	counts, err := st.CountLinkChecks(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if counts.Total != 1 || counts.Broken != 1 || counts.OK != 0 {
		t.Errorf("counts = %+v", counts)
	}

	// ListBrokenLinks surfaces it.
	broken, err := st.ListBrokenLinks(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(broken) != 1 || broken[0].ID != l.ID {
		t.Errorf("broken list = %+v", broken)
	}
}

func TestDeleteCollection_cascadesEverything(t *testing.T) {
	st := openMemForFeatures(t)
	col, _ := st.CreateCollection(context.Background(), "c", "C", "")
	l, _ := st.CreateLink(context.Background(), col.ID, "https://example.com/x")
	tag, _ := st.UpsertTag(context.Background(), "ai", "ai")
	_ = st.AttachTag(context.Background(), l.ID, tag.ID, "auto")
	_, _ = st.InsertChunks(context.Background(), l.ID, []string{"snippet"})
	if err := st.DeleteCollection(context.Background(), col.ID); err != nil {
		t.Fatal(err)
	}
	if links, _ := st.ListLinksByCollection(context.Background(), col.ID, 100, 0); len(links) != 0 {
		t.Errorf("links not cascade-deleted: %d", len(links))
	}
	// Tags survive (they're shared across collections); link_tags shouldn't.
	chunks, _ := st.ListChunksByLink(context.Background(), l.ID)
	if len(chunks) != 0 {
		t.Errorf("chunks not cascade-deleted: %d", len(chunks))
	}
}
