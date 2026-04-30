package feed

import (
	"context"
	"strings"
	"testing"

	"github.com/gabriele-mastrapasqua/linklore/internal/storage"
)

func newStoreWithLinks(t *testing.T) *storage.Store {
	t.Helper()
	st, err := storage.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	col, _ := st.CreateCollection(context.Background(), "reading", "Reading", "")
	for i, u := range []string{"https://a.example/1", "https://b.example/2"} {
		l, _ := st.CreateLink(context.Background(), col.ID, u)
		_ = st.UpdateLinkExtraction(context.Background(), l.ID,
			"Title "+string(rune('A'+i)), "desc", "", "body", "en", "")
		_ = st.UpdateLinkSummary(context.Background(), l.ID, "tldr "+string(rune('A'+i)))
	}
	return st
}

func TestAtom_basicShape(t *testing.T) {
	st := newStoreWithLinks(t)
	b := New(st)
	out, err := b.Atom(context.Background(), "reading", "http://localhost:8080", 50)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`<?xml`, `<feed`, `linklore — Reading`,
		"https://a.example/1", "https://b.example/2",
		"tldr A", "tldr B",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in feed:\n%s", want, out)
		}
	}
}

func TestAtom_unknownCollection(t *testing.T) {
	st := newStoreWithLinks(t)
	if _, err := New(st).Atom(context.Background(), "nope", "", 10); err == nil {
		t.Error("expected error for unknown collection")
	}
}

func TestAtom_emptyCollectionStillValid(t *testing.T) {
	st, _ := storage.Open(context.Background(), ":memory:")
	t.Cleanup(func() { _ = st.Close() })
	_, _ = st.CreateCollection(context.Background(), "empty", "Empty", "")
	out, err := New(st).Atom(context.Background(), "empty", "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "<feed") {
		t.Errorf("not a feed: %s", out)
	}
}
