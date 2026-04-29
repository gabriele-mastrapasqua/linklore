// Tests for the lower-coverage storage methods: feed bookkeeping,
// chunk + chat-session CRUD, FTS searches, status transitions, tag
// search, link-status counts. All :memory:-backed.

package storage

import (
	"context"
	"strings"
	"testing"
)

func mustOpen(t *testing.T) *Store {
	t.Helper()
	st, err := Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// ---------- DB getter ----------

func TestDB_returnsUnderlyingHandle(t *testing.T) {
	st := mustOpen(t)
	db := st.DB()
	if db == nil {
		t.Fatal("DB() returned nil")
	}
	if err := db.Ping(); err != nil {
		t.Errorf("ping: %v", err)
	}
}

// ---------- collection feed bookkeeping ----------

func TestSetCollectionFeed_andMarkChecked(t *testing.T) {
	ctx := context.Background()
	st := mustOpen(t)
	col, _ := st.CreateCollection(ctx, "x", "X", "")

	if err := st.SetCollectionFeed(ctx, col.ID, "https://example.com/feed.xml"); err != nil {
		t.Fatal(err)
	}
	got, _ := st.GetCollectionBySlugByID(ctx, col.ID)
	if got.FeedURL != "https://example.com/feed.xml" {
		t.Errorf("FeedURL = %q", got.FeedURL)
	}
	if err := st.MarkCollectionFeedChecked(ctx, col.ID); err != nil {
		t.Fatal(err)
	}
	got, _ = st.GetCollectionBySlugByID(ctx, col.ID)
	if got.LastCheckedAt == nil {
		t.Errorf("LastCheckedAt still nil after MarkCollectionFeedChecked")
	}

	// Clear by passing empty.
	_ = st.SetCollectionFeed(ctx, col.ID, "")
	got, _ = st.GetCollectionBySlugByID(ctx, col.ID)
	if got.FeedURL != "" {
		t.Errorf("FeedURL not cleared: %q", got.FeedURL)
	}
}

func TestListFeedCollections_skipsBareOnes(t *testing.T) {
	ctx := context.Background()
	st := mustOpen(t)
	a, _ := st.CreateCollection(ctx, "a", "A", "")
	b, _ := st.CreateCollection(ctx, "b", "B", "")
	st.CreateCollection(ctx, "c", "C", "")
	_ = st.SetCollectionFeed(ctx, a.ID, "https://example.com/a.xml")
	_ = st.SetCollectionFeed(ctx, b.ID, "https://example.com/b.xml")

	got, err := st.ListFeedCollections(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Errorf("got %d, want 2", len(got))
	}
}

// ---------- CreateLinkIfMissing ----------

func TestCreateLinkIfMissing_isIdempotent(t *testing.T) {
	ctx := context.Background()
	st := mustOpen(t)
	col, _ := st.CreateCollection(ctx, "c", "C", "")

	a, created, err := st.CreateLinkIfMissing(ctx, col.ID, "https://example.com/x")
	if err != nil || !created {
		t.Fatalf("first call: created=%v err=%v", created, err)
	}
	b, created2, err := st.CreateLinkIfMissing(ctx, col.ID, "https://example.com/x")
	if err != nil || created2 {
		t.Fatalf("second call: created=%v err=%v", created2, err)
	}
	if a.ID != b.ID {
		t.Errorf("expected same id, got %d vs %d", a.ID, b.ID)
	}
}

func TestCreateLinkIfMissing_emptyURLRejected(t *testing.T) {
	ctx := context.Background()
	st := mustOpen(t)
	col, _ := st.CreateCollection(ctx, "c", "C", "")
	if _, _, err := st.CreateLinkIfMissing(ctx, col.ID, "  "); err == nil {
		t.Error("expected error for empty url")
	}
}

// ---------- status transitions ----------

func TestMarkLinkPending_andFetched(t *testing.T) {
	ctx := context.Background()
	st := mustOpen(t)
	col, _ := st.CreateCollection(ctx, "c", "C", "")
	l, _ := st.CreateLink(ctx, col.ID, "https://example.com/y")
	_ = st.UpdateLinkSummary(ctx, l.ID, "summary set")

	got, _ := st.GetLink(ctx, l.ID)
	if got.Status != StatusSummarized {
		t.Fatalf("precondition: status = %q", got.Status)
	}

	// Bumping back to pending should clear the summary stamp from
	// the user's POV (but the summary text stays — reindex regens it).
	if err := st.MarkLinkPending(ctx, l.ID); err != nil {
		t.Fatal(err)
	}
	got, _ = st.GetLink(ctx, l.ID)
	if got.Status != StatusPending {
		t.Errorf("status = %q after MarkLinkPending", got.Status)
	}

	if err := st.MarkLinkFetched(ctx, l.ID); err != nil {
		t.Fatal(err)
	}
	got, _ = st.GetLink(ctx, l.ID)
	if got.Status != StatusFetched {
		t.Errorf("status = %q after MarkLinkFetched", got.Status)
	}
}

// ---------- ListLinksByStatus ----------

func TestListLinksByStatus_filtersAndCaps(t *testing.T) {
	ctx := context.Background()
	st := mustOpen(t)
	col, _ := st.CreateCollection(ctx, "c", "C", "")
	for i := 0; i < 5; i++ {
		_, _ = st.CreateLink(ctx, col.ID, "https://example.com/p"+string(rune('a'+i)))
	}
	got, err := st.ListLinksByStatus(ctx, StatusPending, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Errorf("expected 3 with limit, got %d", len(got))
	}
	for _, l := range got {
		if l.Status != StatusPending {
			t.Errorf("non-pending row leaked: %q", l.Status)
		}
	}
	got2, _ := st.ListLinksByStatus(ctx, StatusSummarized, 50)
	if len(got2) != 0 {
		t.Errorf("no summarized rows yet, got %d", len(got2))
	}
}

// ---------- CountInProgress ----------

func TestCountInProgress(t *testing.T) {
	ctx := context.Background()
	st := mustOpen(t)
	col, _ := st.CreateCollection(ctx, "c", "C", "")
	for i := 0; i < 4; i++ {
		_, _ = st.CreateLink(ctx, col.ID, "https://example.com/p"+string(rune('a'+i)))
	}
	got, err := st.CountInProgress(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got != 4 {
		t.Errorf("CountInProgress = %d, want 4", got)
	}
}

// ---------- Chunks: replace, list-by-collection, get-by-ids ----------

func TestReplaceChunks_overwritesExisting(t *testing.T) {
	ctx := context.Background()
	st := mustOpen(t)
	col, _ := st.CreateCollection(ctx, "c", "C", "")
	l, _ := st.CreateLink(ctx, col.ID, "https://example.com/p")

	ids1, err := st.ReplaceChunks(ctx, l.ID, []string{"alpha", "beta"})
	if err != nil {
		t.Fatal(err)
	}
	if len(ids1) != 2 {
		t.Errorf("first replace ids = %v", ids1)
	}
	ids2, err := st.ReplaceChunks(ctx, l.ID, []string{"gamma"})
	if err != nil {
		t.Fatal(err)
	}
	if len(ids2) != 1 {
		t.Errorf("second replace ids = %v", ids2)
	}

	got, _ := st.ListChunksByLink(ctx, l.ID)
	if len(got) != 1 || got[0].Text != "gamma" {
		t.Errorf("chunks after replace: %v", got)
	}
}

func TestListChunksByCollection_skipsUnembedded(t *testing.T) {
	ctx := context.Background()
	st := mustOpen(t)
	col, _ := st.CreateCollection(ctx, "c", "C", "")
	l, _ := st.CreateLink(ctx, col.ID, "https://example.com/p")
	ids, _ := st.InsertChunks(ctx, l.ID, []string{"one", "two"})
	// Only one of the two has an embedding — the other must be skipped.
	if err := st.SetChunkEmbedding(ctx, ids[0], []byte{0x01, 0x00, 0x00, 0x00}); err != nil {
		t.Fatal(err)
	}

	got, err := st.ListChunksByCollection(ctx, col.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != ids[0] {
		t.Errorf("expected only embedded chunk, got %v", got)
	}
}

func TestGetChunksByIDs_arbitraryOrder(t *testing.T) {
	ctx := context.Background()
	st := mustOpen(t)
	col, _ := st.CreateCollection(ctx, "c", "C", "")
	l, _ := st.CreateLink(ctx, col.ID, "https://example.com/p")
	ids, _ := st.InsertChunks(ctx, l.ID, []string{"alpha", "beta", "gamma"})

	got, err := st.GetChunksByIDs(ctx, []int64{ids[2], ids[0]})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Errorf("got %d", len(got))
	}
	// Empty input returns nil cleanly.
	if g, _ := st.GetChunksByIDs(ctx, nil); g != nil {
		t.Errorf("empty input should return nil, got %v", g)
	}
}

// ---------- chat sessions / messages ----------

func TestChatSession_appendAndRetrieveOldestFirst(t *testing.T) {
	ctx := context.Background()
	st := mustOpen(t)
	col, _ := st.CreateCollection(ctx, "c", "C", "")

	sid, err := st.CreateChatSession(ctx, col.ID)
	if err != nil {
		t.Fatal(err)
	}
	if sid <= 0 {
		t.Errorf("session id = %d", sid)
	}

	for i, role := range []string{"user", "assistant", "user"} {
		if _, err := st.AppendChatMessage(ctx, sid, role, "msg-"+string(rune('a'+i))); err != nil {
			t.Fatal(err)
		}
	}
	msgs, err := st.RecentChatMessages(ctx, sid, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 3 {
		t.Fatalf("got %d", len(msgs))
	}
	// Oldest-first.
	if msgs[0].Content != "msg-a" || msgs[2].Content != "msg-c" {
		t.Errorf("ordering wrong: %+v", msgs)
	}
}

func TestChatSession_anonymousNoCollection(t *testing.T) {
	ctx := context.Background()
	st := mustOpen(t)
	sid, err := st.CreateChatSession(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	if sid <= 0 {
		t.Errorf("session id = %d", sid)
	}
}

func TestAppendChatMessage_rejectsBadRole(t *testing.T) {
	ctx := context.Background()
	st := mustOpen(t)
	sid, _ := st.CreateChatSession(ctx, 0)
	if _, err := st.AppendChatMessage(ctx, sid, "robot", "x"); err == nil {
		t.Error("expected role validation error")
	}
}

func TestRecentChatMessages_appliesLimit(t *testing.T) {
	ctx := context.Background()
	st := mustOpen(t)
	sid, _ := st.CreateChatSession(ctx, 0)
	for i := 0; i < 10; i++ {
		_, _ = st.AppendChatMessage(ctx, sid, "user", string(rune('a'+i)))
	}
	got, _ := st.RecentChatMessages(ctx, sid, 4)
	if len(got) != 4 {
		t.Errorf("limit ignored: %d", len(got))
	}
	// Most recent 4 in oldest-first order: g h i j? No — content was a..j,
	// most-recent 4 = g h i j, oldest-first = g h i j.
	if got[0].Content != "g" || got[3].Content != "j" {
		t.Errorf("ordering wrong: %v",
			[]string{got[0].Content, got[1].Content, got[2].Content, got[3].Content})
	}
}

// ---------- FTS searches ----------

func TestSearchLinksFTS_findsByTitle(t *testing.T) {
	ctx := context.Background()
	st := mustOpen(t)
	col, _ := st.CreateCollection(ctx, "c", "C", "")
	l, _ := st.CreateLink(ctx, col.ID, "https://example.com/p")
	if err := st.UpdateLinkExtraction(ctx, l.ID, "Quantum Networking",
		"deep dive", "", "body text", "en", ""); err != nil {
		t.Fatal(err)
	}
	// SearchLinksFTS pulls the snippet from the summary column;
	// without one, the snippet scan would reject NULL. Add a summary.
	if err := st.UpdateLinkSummary(ctx, l.ID, "quantum networking deep dive"); err != nil {
		t.Fatal(err)
	}

	hits, err := st.SearchLinksFTS(ctx, "quantum", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].LinkID != l.ID {
		t.Errorf("hits = %+v", hits)
	}
}

func TestSearchChunksFTS_collectionScoping(t *testing.T) {
	ctx := context.Background()
	st := mustOpen(t)
	a, _ := st.CreateCollection(ctx, "a", "A", "")
	b, _ := st.CreateCollection(ctx, "b", "B", "")
	la, _ := st.CreateLink(ctx, a.ID, "https://example.com/a")
	lb, _ := st.CreateLink(ctx, b.ID, "https://example.com/b")
	_, _ = st.InsertChunks(ctx, la.ID, []string{"alpha bravo charlie"})
	_, _ = st.InsertChunks(ctx, lb.ID, []string{"alpha delta echo"})

	all, _ := st.SearchChunksFTS(ctx, "alpha", 0, 10)
	if len(all) != 2 {
		t.Errorf("global search hits = %d, want 2", len(all))
	}
	scoped, _ := st.SearchChunksFTS(ctx, "alpha", a.ID, 10)
	if len(scoped) != 1 || scoped[0].LinkID != la.ID {
		t.Errorf("scoped hits = %+v", scoped)
	}
}

// ---------- tag prefix search ----------

func TestSearchLinksByTagPrefix(t *testing.T) {
	ctx := context.Background()
	st := mustOpen(t)
	col, _ := st.CreateCollection(ctx, "c", "C", "")
	l1, _ := st.CreateLink(ctx, col.ID, "https://example.com/a")
	l2, _ := st.CreateLink(ctx, col.ID, "https://example.com/b")
	t1, _ := st.UpsertTag(ctx, "ai", "AI")
	t2, _ := st.UpsertTag(ctx, "audio", "Audio")
	_ = st.AttachTag(ctx, l1.ID, t1.ID, "user")
	_ = st.AttachTag(ctx, l2.ID, t2.ID, "user")

	got, err := st.SearchLinksByTagPrefix(ctx, "a", 10)
	if err != nil {
		t.Fatal(err)
	}
	// Both ai and audio start with "a" → 2 distinct link ids.
	if len(got) != 2 {
		t.Errorf("got %v, want 2 ids", got)
	}
	one, _ := st.SearchLinksByTagPrefix(ctx, "ai", 10)
	if len(one) != 1 || one[0] != l1.ID {
		t.Errorf("prefix=ai got %v", one)
	}
	if g, _ := st.SearchLinksByTagPrefix(ctx, "  ", 10); g != nil {
		t.Errorf("blank prefix should return nil, got %v", g)
	}
}

// ---------- LinkStatusCounts (the chat header relies on this) ----------

func TestLinkStatusCounts(t *testing.T) {
	ctx := context.Background()
	st := mustOpen(t)
	col, _ := st.CreateCollection(ctx, "c", "C", "")
	l1, _ := st.CreateLink(ctx, col.ID, "https://example.com/a")
	l2, _ := st.CreateLink(ctx, col.ID, "https://example.com/b")
	l3, _ := st.CreateLink(ctx, col.ID, "https://example.com/c")
	_ = st.UpdateLinkSummary(ctx, l1.ID, "summary")
	_ = st.MarkLinkFailed(ctx, l2.ID, "boom")
	_ = l3 // stays pending

	got, err := st.LinkStatusCounts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got.Ready != 1 || got.Failed != 1 || got.InProgress != 1 {
		t.Errorf("counts = %+v", got)
	}
}

// ---------- FindTagBySlug error path ----------

func TestFindTagBySlug_unknownReturnsErrNotFound(t *testing.T) {
	ctx := context.Background()
	st := mustOpen(t)
	if _, err := st.FindTagBySlug(ctx, "no-such-tag"); err != ErrNotFound {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

// ---------- buildDSN edges ----------

// :memory: paths get an isolated, non-WAL DSN — sharing across handles
// caused "database is closed" races in parallel tests, so the in-mem
// path is intentionally different from the file-backed one.
func TestBuildDSN_inMemoryHasIsolatedDSN(t *testing.T) {
	got := buildDSN(":memory:")
	for _, want := range []string{":memory:", "_journal_mode=DELETE", "_foreign_keys=on"} {
		if !strings.Contains(got, want) {
			t.Errorf("in-mem DSN missing %q: %s", want, got)
		}
	}
}

func TestBuildDSN_filePathAppliesProductionPragmas(t *testing.T) {
	got := buildDSN("/tmp/x.db")
	for _, want := range []string{"file:/tmp/x.db", "_journal_mode=WAL", "_busy_timeout=5000", "_foreign_keys=on", "_txlock=immediate"} {
		if !strings.Contains(got, want) {
			t.Errorf("file DSN missing %q: %s", want, got)
		}
	}
}
