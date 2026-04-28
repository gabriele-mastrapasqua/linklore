package storage

import (
	"context"
	"strings"
	"testing"
)

func openMem(t *testing.T) *Store {
	t.Helper()
	s, err := Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatalf("open mem: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestMigrate_idempotent(t *testing.T) {
	s := openMem(t)
	// Re-running must not fail.
	if err := s.migrate(context.Background()); err != nil {
		t.Fatalf("rerun migrate: %v", err)
	}
}

func TestCollections_CRUD(t *testing.T) {
	s := openMem(t)
	ctx := context.Background()

	c, err := s.CreateCollection(ctx, "default", "Default", "main bucket")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if c.ID == 0 {
		t.Fatal("expected nonzero id")
	}

	got, err := s.GetCollectionBySlug(ctx, "default")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Name != "Default" {
		t.Errorf("name = %q", got.Name)
	}

	if _, err := s.GetCollectionBySlug(ctx, "nope"); err != ErrNotFound {
		t.Errorf("expected ErrNotFound, got %v", err)
	}

	list, err := s.ListCollections(ctx)
	if err != nil || len(list) != 1 {
		t.Fatalf("list: err=%v len=%d", err, len(list))
	}

	// Slug uniqueness is enforced by schema.
	if _, err := s.CreateCollection(ctx, "default", "Dup", ""); err == nil {
		t.Fatal("expected unique-violation error")
	}

	if err := s.DeleteCollection(ctx, c.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
}

func TestLinks_CRUDAndStatus(t *testing.T) {
	s := openMem(t)
	ctx := context.Background()
	c, _ := s.CreateCollection(ctx, "c", "C", "")

	l, err := s.CreateLink(ctx, c.ID, "https://example.com/a")
	if err != nil {
		t.Fatalf("create link: %v", err)
	}
	if l.Status != StatusPending {
		t.Errorf("status = %q", l.Status)
	}

	if err := s.UpdateLinkExtraction(ctx, l.ID,
		"Title", "Desc", "https://img", "# hello\n\nbody", "en", ""); err != nil {
		t.Fatalf("update extraction: %v", err)
	}
	if err := s.UpdateLinkSummary(ctx, l.ID, "tldr"); err != nil {
		t.Fatalf("update summary: %v", err)
	}
	got, err := s.GetLink(ctx, l.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Title != "Title" || got.Summary != "tldr" || got.Status != StatusSummarized {
		t.Errorf("state: %+v", got)
	}
	if got.FetchedAt == nil {
		t.Error("expected FetchedAt set after extraction")
	}

	if err := s.MarkLinkRead(ctx, l.ID); err != nil {
		t.Fatalf("mark read: %v", err)
	}
	got, _ = s.GetLink(ctx, l.ID)
	if got.ReadAt == nil {
		t.Error("expected ReadAt set")
	}

	// Pagination + ordering.
	for i := range 3 {
		if _, err := s.CreateLink(ctx, c.ID, "https://example.com/b"+string(rune('a'+i))); err != nil {
			t.Fatal(err)
		}
	}
	all, err := s.ListLinksByCollection(ctx, c.ID, 100, 0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(all) != 4 {
		t.Errorf("len=%d", len(all))
	}

	// Failed status.
	if err := s.MarkLinkFailed(ctx, all[0].ID, "boom"); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetLink(ctx, all[0].ID)
	if got.Status != StatusFailed || got.FetchError != "boom" {
		t.Errorf("after fail: %+v", got)
	}

	// Unique (collection_id, url).
	if _, err := s.CreateLink(ctx, c.ID, "https://example.com/a"); err == nil {
		t.Error("expected unique violation")
	}

	if err := s.DeleteLink(ctx, l.ID); err != nil {
		t.Fatal(err)
	}
}

func TestUpdateLinkNote(t *testing.T) {
	s := openMem(t)
	ctx := context.Background()
	c, _ := s.CreateCollection(ctx, "c", "C", "")
	l, _ := s.CreateLink(ctx, c.ID, "https://x")

	got, _ := s.GetLink(ctx, l.ID)
	if got.Note != "" {
		t.Errorf("expected empty note, got %q", got.Note)
	}
	if err := s.UpdateLinkNote(ctx, l.ID, "  remember to revisit this  "); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetLink(ctx, l.ID)
	if got.Note != "remember to revisit this" {
		t.Errorf("note = %q", got.Note)
	}
	// Empty string clears.
	if err := s.UpdateLinkNote(ctx, l.ID, ""); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetLink(ctx, l.ID)
	if got.Note != "" {
		t.Errorf("expected note cleared, got %q", got.Note)
	}
}

func TestPrefs_Roundtrip(t *testing.T) {
	s := openMem(t)
	ctx := context.Background()
	if _, err := s.GetPref(ctx, "theme"); err != ErrNotFound {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
	if err := s.SetPref(ctx, "theme", "dark"); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetPref(ctx, "theme")
	if err != nil || got != "dark" {
		t.Errorf("got %q err=%v", got, err)
	}
	// Upsert overwrites.
	if err := s.SetPref(ctx, "theme", "light"); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetPref(ctx, "theme")
	if got != "light" {
		t.Errorf("after upsert: %q", got)
	}
}

func TestReorderLink_withinSameCollection(t *testing.T) {
	s := openMem(t)
	ctx := context.Background()
	c, _ := s.CreateCollection(ctx, "c", "C", "")
	a, _ := s.CreateLink(ctx, c.ID, "https://x/a") // order_idx 1
	b, _ := s.CreateLink(ctx, c.ID, "https://x/b") // order_idx 2
	d, _ := s.CreateLink(ctx, c.ID, "https://x/c") // order_idx 3

	// Initial order top→bottom is d, b, a (highest order_idx first).
	got, _ := s.ListLinksByCollection(ctx, c.ID, 100, 0)
	wantOrder := []int64{d.ID, b.ID, a.ID}
	for i, l := range got {
		if l.ID != wantOrder[i] {
			t.Errorf("initial[%d] = %d, want %d", i, l.ID, wantOrder[i])
		}
	}

	// Move "a" before "d" → list becomes a, d, b.
	if err := s.ReorderLink(ctx, a.ID, d.ID, 0, false); err != nil {
		t.Fatal(err)
	}
	got, _ = s.ListLinksByCollection(ctx, c.ID, 100, 0)
	wantOrder = []int64{a.ID, d.ID, b.ID}
	for i, l := range got {
		if l.ID != wantOrder[i] {
			t.Errorf("after before: got[%d]=%d want %d", i, l.ID, wantOrder[i])
		}
	}

	// Move "d" after "b" → list becomes a, b, d.
	if err := s.ReorderLink(ctx, d.ID, b.ID, 0, true); err != nil {
		t.Fatal(err)
	}
	got, _ = s.ListLinksByCollection(ctx, c.ID, 100, 0)
	wantOrder = []int64{a.ID, b.ID, d.ID}
	for i, l := range got {
		if l.ID != wantOrder[i] {
			t.Errorf("after after: got[%d]=%d want %d", i, l.ID, wantOrder[i])
		}
	}
}

func TestReorderLink_acrossCollections(t *testing.T) {
	s := openMem(t)
	ctx := context.Background()
	a, _ := s.CreateCollection(ctx, "a", "A", "")
	b, _ := s.CreateCollection(ctx, "b", "B", "")
	src, _ := s.CreateLink(ctx, a.ID, "https://x/1")
	pivot, _ := s.CreateLink(ctx, b.ID, "https://x/2")

	if err := s.ReorderLink(ctx, src.ID, pivot.ID, b.ID, false); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetLink(ctx, src.ID)
	if got.CollectionID != b.ID {
		t.Errorf("collection_id = %d (want %d)", got.CollectionID, b.ID)
	}
	if got.OrderIdx <= pivot.OrderIdx {
		t.Errorf("expected new order_idx ABOVE pivot: %v vs %v", got.OrderIdx, pivot.OrderIdx)
	}
}

func TestCreateLink_orderIdxAppendsOnTop(t *testing.T) {
	s := openMem(t)
	ctx := context.Background()
	c, _ := s.CreateCollection(ctx, "c", "C", "")
	a, _ := s.CreateLink(ctx, c.ID, "https://x/a")
	b, _ := s.CreateLink(ctx, c.ID, "https://x/b")
	if b.OrderIdx <= a.OrderIdx {
		t.Errorf("new link should sit above the previous one: %v vs %v", b.OrderIdx, a.OrderIdx)
	}
}

func TestMoveLink(t *testing.T) {
	s := openMem(t)
	ctx := context.Background()
	a, _ := s.CreateCollection(ctx, "a", "A", "")
	b, _ := s.CreateCollection(ctx, "b", "B", "")
	l, _ := s.CreateLink(ctx, a.ID, "https://x")

	if err := s.MoveLink(ctx, l.ID, b.ID); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetLink(ctx, l.ID)
	if got.CollectionID != b.ID {
		t.Errorf("collection_id = %d, want %d", got.CollectionID, b.ID)
	}

	// Unknown destination → ErrNotFound, link untouched.
	if err := s.MoveLink(ctx, l.ID, 9999); err != ErrNotFound {
		t.Errorf("expected ErrNotFound on unknown dst, got %v", err)
	}
	// Unknown link → ErrNotFound.
	if err := s.MoveLink(ctx, 9999, b.ID); err != ErrNotFound {
		t.Errorf("expected ErrNotFound on unknown link, got %v", err)
	}
}

func TestLinks_FK_CascadeOnCollectionDelete(t *testing.T) {
	s := openMem(t)
	ctx := context.Background()
	c, _ := s.CreateCollection(ctx, "c", "C", "")
	l, _ := s.CreateLink(ctx, c.ID, "https://x")
	if err := s.DeleteCollection(ctx, c.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetLink(ctx, l.ID); err != ErrNotFound {
		t.Errorf("expected cascade delete, got %v", err)
	}
}

func TestChunks_InsertEmbeddingList(t *testing.T) {
	s := openMem(t)
	ctx := context.Background()
	c, _ := s.CreateCollection(ctx, "c", "C", "")
	l, _ := s.CreateLink(ctx, c.ID, "https://x")

	ids, err := s.InsertChunks(ctx, l.ID, []string{"alpha bravo", "charlie delta", "echo foxtrot"})
	if err != nil {
		t.Fatalf("insert chunks: %v", err)
	}
	if len(ids) != 3 {
		t.Fatalf("ids=%v", ids)
	}

	emb := []byte{0x01, 0x02, 0x03, 0x04}
	if err := s.SetChunkEmbedding(ctx, ids[1], emb); err != nil {
		t.Fatal(err)
	}
	chunks, err := s.ListChunksByLink(ctx, l.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) != 3 || chunks[0].Ord != 0 || chunks[2].Ord != 2 {
		t.Errorf("ord broken: %+v", chunks)
	}
	if string(chunks[1].Embedding) != string(emb) {
		t.Errorf("embedding roundtrip failed")
	}

	// Cascade on link delete.
	if err := s.DeleteLink(ctx, l.ID); err != nil {
		t.Fatal(err)
	}
	chunks, _ = s.ListChunksByLink(ctx, l.ID)
	if len(chunks) != 0 {
		t.Errorf("chunks not cascaded: %v", chunks)
	}
}

func TestTags_UpsertAttachListTop(t *testing.T) {
	s := openMem(t)
	ctx := context.Background()
	c, _ := s.CreateCollection(ctx, "c", "C", "")
	l1, _ := s.CreateLink(ctx, c.ID, "https://x/1")
	l2, _ := s.CreateLink(ctx, c.ID, "https://x/2")

	tg, err := s.UpsertTag(ctx, "go", "Go")
	if err != nil {
		t.Fatal(err)
	}
	// Idempotent.
	tg2, err := s.UpsertTag(ctx, "go", "Go")
	if err != nil {
		t.Fatal(err)
	}
	if tg.ID != tg2.ID {
		t.Errorf("upsert created duplicate")
	}

	tg3, _ := s.UpsertTag(ctx, "rust", "Rust")
	if err := s.AttachTag(ctx, l1.ID, tg.ID, TagSourceAuto); err != nil {
		t.Fatal(err)
	}
	if err := s.AttachTag(ctx, l1.ID, tg3.ID, TagSourceUser); err != nil {
		t.Fatal(err)
	}
	if err := s.AttachTag(ctx, l2.ID, tg.ID, TagSourceAuto); err != nil {
		t.Fatal(err)
	}

	tags, err := s.ListTagsByLink(ctx, l1.ID)
	if err != nil || len(tags) != 2 {
		t.Fatalf("tags=%v err=%v", tags, err)
	}

	top, err := s.ListTopTagSlugs(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(top) != 2 || top[0] != "go" {
		t.Errorf("top order wrong: %v", top)
	}

	n, err := s.CountActiveTags(ctx)
	if err != nil || n != 2 {
		t.Errorf("active = %d err=%v", n, err)
	}

	if err := s.DetachTag(ctx, l1.ID, tg3.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.AttachTag(ctx, l1.ID, tg.ID, "nope"); err == nil {
		t.Error("expected invalid source error")
	}
}

func TestTags_MergeAndCounts(t *testing.T) {
	s := openMem(t)
	ctx := context.Background()
	c, _ := s.CreateCollection(ctx, "c", "C", "")
	l1, _ := s.CreateLink(ctx, c.ID, "https://x/1")
	l2, _ := s.CreateLink(ctx, c.ID, "https://x/2")
	a, _ := s.UpsertTag(ctx, "ai", "AI")
	b, _ := s.UpsertTag(ctx, "ml", "ML")
	_ = s.AttachTag(ctx, l1.ID, a.ID, TagSourceAuto)
	_ = s.AttachTag(ctx, l1.ID, b.ID, TagSourceAuto)
	_ = s.AttachTag(ctx, l2.ID, b.ID, TagSourceAuto)

	counts, err := s.ListTagsWithCounts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// "ml" used twice, "ai" once → ml first.
	if len(counts) != 2 || counts[0].Slug != "ml" || counts[0].Count != 2 {
		t.Errorf("counts: %+v", counts)
	}

	// Merge ai into ml. Both links must end up tagged ml; ai tag gone.
	if err := s.MergeTag(ctx, a.ID, b.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.FindTagBySlug(ctx, "ai"); err != ErrNotFound {
		t.Errorf("ai still present: %v", err)
	}
	for _, lid := range []int64{l1.ID, l2.ID} {
		tags, _ := s.ListTagsByLink(ctx, lid)
		if len(tags) != 1 || tags[0].Slug != "ml" {
			t.Errorf("after merge, link %d tags: %v", lid, tags)
		}
	}
	// Self-merge is a no-op.
	if err := s.MergeTag(ctx, b.ID, b.ID); err != nil {
		t.Errorf("self-merge errored: %v", err)
	}
}

func TestListLinksByTag(t *testing.T) {
	s := openMem(t)
	ctx := context.Background()
	c, _ := s.CreateCollection(ctx, "c", "C", "")
	l, _ := s.CreateLink(ctx, c.ID, "https://x/1")
	tg, _ := s.UpsertTag(ctx, "go", "Go")
	_ = s.AttachTag(ctx, l.ID, tg.ID, TagSourceUser)

	got, err := s.ListLinksByTag(ctx, "go", 10)
	if err != nil || len(got) != 1 || got[0].ID != l.ID {
		t.Errorf("got %v err=%v", got, err)
	}
	none, _ := s.ListLinksByTag(ctx, "missing", 10)
	if len(none) != 0 {
		t.Errorf("expected 0, got %v", none)
	}
}

func TestCollectionStats(t *testing.T) {
	s := openMem(t)
	ctx := context.Background()
	c, _ := s.CreateCollection(ctx, "c", "C", "")
	a, _ := s.CreateLink(ctx, c.ID, "https://x/1") // pending
	b, _ := s.CreateLink(ctx, c.ID, "https://x/2") // → summarized
	d, _ := s.CreateLink(ctx, c.ID, "https://x/3") // → failed

	_ = s.UpdateLinkExtraction(ctx, b.ID, "T", "d", "", "body", "en", "")
	_ = s.UpdateLinkSummary(ctx, b.ID, "tldr")
	_ = s.MarkLinkFailed(ctx, d.ID, "boom")
	_ = a // keep at pending

	cs, err := s.CollectionStatsByID(ctx, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if cs.Total != 3 || cs.Summarized != 1 || cs.InProgress != 1 || cs.Failed != 1 {
		t.Errorf("stats: %+v", cs)
	}

	all, err := s.ListCollectionsWithStats(ctx)
	if err != nil || len(all) != 1 {
		t.Fatalf("list: err=%v len=%d", err, len(all))
	}
	if all[0].Summarized != 1 {
		t.Errorf("Summarized = %d (want 1)", all[0].Summarized)
	}

	gc, err := s.LinkStatusCounts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if gc.Ready != 1 || gc.InProgress != 1 || gc.Failed != 1 {
		t.Errorf("global counts: %+v", gc)
	}
}

func TestCollectionStatsByID_NotFound(t *testing.T) {
	s := openMem(t)
	if _, err := s.CollectionStatsByID(context.Background(), 9999); err != ErrNotFound {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestFTS_LinksAndChunks(t *testing.T) {
	s := openMem(t)
	ctx := context.Background()
	c, _ := s.CreateCollection(ctx, "c", "C", "")
	l, _ := s.CreateLink(ctx, c.ID, "https://x")
	if err := s.UpdateLinkExtraction(ctx, l.ID,
		"The Quick Brown Fox", "jumps over", "", "the lazy dog runs", "en", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateLinkSummary(ctx, l.ID, "fox jumps dog"); err != nil {
		t.Fatal(err)
	}

	// Link FTS hit on summary or content.
	row := s.db.QueryRowContext(ctx,
		`SELECT rowid FROM links_fts WHERE links_fts MATCH ?`, "fox")
	var rid int64
	if err := row.Scan(&rid); err != nil {
		t.Fatalf("fts hit: %v", err)
	}
	if rid != l.ID {
		t.Errorf("fts rowid = %d want %d", rid, l.ID)
	}

	// chunks_fts hit on chunk text.
	if _, err := s.InsertChunks(ctx, l.ID,
		[]string{"alpha tornado", "beta hurricane"}); err != nil {
		t.Fatal(err)
	}
	row = s.db.QueryRowContext(ctx,
		`SELECT text FROM chunks_fts WHERE chunks_fts MATCH ? LIMIT 1`, "tornado")
	var txt string
	if err := row.Scan(&txt); err != nil {
		t.Fatalf("chunks fts: %v", err)
	}
	if !strings.Contains(txt, "tornado") {
		t.Errorf("got %q", txt)
	}

	// Update propagation.
	if err := s.UpdateLinkSummary(ctx, l.ID, "totally different"); err != nil {
		t.Fatal(err)
	}
	row = s.db.QueryRowContext(ctx,
		`SELECT rowid FROM links_fts WHERE links_fts MATCH ?`, "different")
	var rid2 int64
	if err := row.Scan(&rid2); err != nil {
		t.Fatalf("expected update propagated: %v", err)
	}

	// Delete propagation.
	if err := s.DeleteLink(ctx, l.ID); err != nil {
		t.Fatal(err)
	}
	row = s.db.QueryRowContext(ctx,
		`SELECT rowid FROM links_fts WHERE links_fts MATCH ?`, "different")
	if err := row.Scan(&rid2); err == nil {
		t.Error("expected no FTS rows after delete")
	}
}
