package server

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/gabrielemastrapasqua/linklore/internal/chat"
	"github.com/gabrielemastrapasqua/linklore/internal/config"
	"github.com/gabrielemastrapasqua/linklore/internal/llm"
	"github.com/gabrielemastrapasqua/linklore/internal/llm/fake"
	"github.com/gabrielemastrapasqua/linklore/internal/search"
	"github.com/gabrielemastrapasqua/linklore/internal/storage"
	"github.com/gabrielemastrapasqua/linklore/internal/worker"
)

func newTestServer(t *testing.T) (*httptest.Server, *storage.Store) {
	t.Helper()
	return newTestServerWithChat(t, nil)
}

func newTestServerWithChat(t *testing.T, chatSvc *chat.Service) (*httptest.Server, *storage.Store) {
	t.Helper()
	st, err := storage.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	srv, err := New(config.Default(), st, nil, chatSvc, nil)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts, st
}

func get(t *testing.T, ts *httptest.Server, path string) (int, string) {
	t.Helper()
	resp, err := ts.Client().Get(ts.URL + path)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}

func postForm(t *testing.T, ts *httptest.Server, path string, form url.Values) (int, string) {
	t.Helper()
	resp, err := ts.Client().PostForm(ts.URL+path, form)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}

func deleteReq(t *testing.T, ts *httptest.Server, path string) int {
	t.Helper()
	req, _ := http.NewRequest(http.MethodDelete, ts.URL+path, nil)
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	return resp.StatusCode
}

func TestHomePage_empty(t *testing.T) {
	ts, _ := newTestServer(t)
	code, body := get(t, ts, "/")
	if code != 200 {
		t.Fatalf("status=%d body=%s", code, body)
	}
	if !strings.Contains(body, "linklore") || !strings.Contains(body, "Collections") {
		t.Errorf("unexpected body: %s", body)
	}
	if !strings.Contains(body, "No collections yet") {
		t.Errorf("expected empty-state message")
	}
}

func TestStaticAssetsServed(t *testing.T) {
	ts, _ := newTestServer(t)
	code, body := get(t, ts, "/static/app.css")
	if code != 200 {
		t.Fatalf("status=%d", code)
	}
	if !strings.Contains(body, ".link-row") {
		t.Errorf("css not served correctly")
	}
}

func TestCreateCollection_HTMXFragment(t *testing.T) {
	ts, _ := newTestServer(t)
	code, body := postForm(t, ts, "/collections",
		url.Values{"slug": {"reading"}, "name": {"Reading list"}})
	if code != 200 {
		t.Fatalf("status=%d body=%s", code, body)
	}
	// Fragment, not full HTML page.
	if strings.Contains(body, "<html") {
		t.Errorf("expected fragment, got full page: %s", body)
	}
	if !strings.Contains(body, "Reading list") || !strings.Contains(body, "/c/reading") {
		t.Errorf("fragment missing fields: %s", body)
	}
}

func TestCreateCollection_validation(t *testing.T) {
	ts, _ := newTestServer(t)
	code, _ := postForm(t, ts, "/collections", url.Values{"slug": {""}, "name": {""}})
	if code != http.StatusBadRequest {
		t.Errorf("status=%d, want 400", code)
	}
}

func TestListLinks_404OnUnknownCollection(t *testing.T) {
	ts, _ := newTestServer(t)
	code, _ := get(t, ts, "/c/missing")
	if code != http.StatusNotFound {
		t.Errorf("status=%d", code)
	}
}

func TestAddAndDeleteLink(t *testing.T) {
	ts, st := newTestServer(t)
	if _, err := st.CreateCollection(context.Background(), "default", "Default", ""); err != nil {
		t.Fatal(err)
	}

	// Render collection page.
	code, body := get(t, ts, "/c/default")
	if code != 200 {
		t.Fatalf("collection page: %d", code)
	}
	if !strings.Contains(body, "No links yet") {
		t.Errorf("expected empty links state")
	}

	// Add a link via HTMX form.
	code, body = postForm(t, ts, "/c/default/links", url.Values{"url": {"https://example.com/x"}})
	if code != 200 {
		t.Fatalf("add link: %d body=%s", code, body)
	}
	if !strings.Contains(body, `id="link-`) || !strings.Contains(body, "https://example.com/x") {
		t.Errorf("fragment missing: %s", body)
	}
	if !strings.Contains(body, `class="badge pending"`) {
		t.Errorf("expected pending badge: %s", body)
	}

	// Find the link id.
	links, _ := st.ListLinksByCollection(context.Background(), 1, 10, 0)
	if len(links) != 1 {
		t.Fatalf("len=%d", len(links))
	}
	id := links[0].ID

	// Delete it.
	if code := deleteReq(t, ts, "/links/"+i64s(id)); code != 200 {
		t.Fatalf("delete: %d", code)
	}
	links, _ = st.ListLinksByCollection(context.Background(), 1, 10, 0)
	if len(links) != 0 {
		t.Errorf("link not deleted")
	}
}

func TestAddLink_emptyURLRejected(t *testing.T) {
	ts, st := newTestServer(t)
	st.CreateCollection(context.Background(), "c", "C", "")
	code, _ := postForm(t, ts, "/c/c/links", url.Values{"url": {""}})
	if code != http.StatusBadRequest {
		t.Errorf("status=%d", code)
	}
}

func TestHomeListsCreatedCollections(t *testing.T) {
	ts, st := newTestServer(t)
	st.CreateCollection(context.Background(), "alpha", "Alpha", "")
	st.CreateCollection(context.Background(), "bravo", "Bravo", "")
	_, body := get(t, ts, "/")
	if !strings.Contains(body, "Alpha") || !strings.Contains(body, "Bravo") {
		t.Errorf("collections not listed: %s", body)
	}
}

func TestHealthz(t *testing.T) {
	ts, _ := newTestServer(t)
	code, body := get(t, ts, "/healthz")
	if code != 200 || body != "ok" {
		t.Errorf("got %d %q", code, body)
	}
}

func TestReaderMode_renderedFromContentMD(t *testing.T) {
	ts, st := newTestServer(t)
	col, _ := st.CreateCollection(context.Background(), "c", "C", "")
	l, _ := st.CreateLink(context.Background(), col.ID, "https://example.com/x")
	_ = st.UpdateLinkExtraction(context.Background(), l.ID,
		"Title", "desc", "", "# Heading\n\nbody **bold**\n\n<script>x</script>", "en", "")

	code, body := get(t, ts, "/links/"+i64s(l.ID)+"/read")
	if code != 200 {
		t.Fatalf("status=%d", code)
	}
	for _, want := range []string{"<h1", "<strong>bold</strong>", "Heading"} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q in body", want)
		}
	}
	// The article's malicious <script>x</script> must be stripped.
	// (The base layout still pulls in htmx via <script src=...> which is fine.)
	if strings.Contains(body, ">x</script>") || strings.Contains(body, "<script>x</script>") {
		t.Errorf("article script not sanitised: %s", body)
	}
}

func TestUserTagAddRemove(t *testing.T) {
	ts, st := newTestServer(t)
	col, _ := st.CreateCollection(context.Background(), "c", "C", "")
	l, _ := st.CreateLink(context.Background(), col.ID, "https://example.com/x")

	code, body := postForm(t, ts, "/links/"+i64s(l.ID)+"/tags", url.Values{"tag": {"Go!"}})
	if code != 200 {
		t.Fatalf("add tag: %d %s", code, body)
	}
	if !strings.Contains(body, "Go!") {
		t.Errorf("tag chip missing: %s", body)
	}
	tags, _ := st.ListTagsByLink(context.Background(), l.ID)
	if len(tags) != 1 || tags[0].Slug != "go" {
		t.Errorf("not slugified: %+v", tags)
	}

	// Remove via DELETE.
	code = deleteReq(t, ts, "/links/"+i64s(l.ID)+"/tags/go")
	if code != 200 {
		t.Errorf("delete status: %d", code)
	}
	tags, _ = st.ListTagsByLink(context.Background(), l.ID)
	if len(tags) != 0 {
		t.Errorf("tag still attached: %v", tags)
	}
}

func TestUserTag_emptyRejected(t *testing.T) {
	ts, st := newTestServer(t)
	col, _ := st.CreateCollection(context.Background(), "c", "C", "")
	l, _ := st.CreateLink(context.Background(), col.ID, "https://example.com/x")
	code, _ := postForm(t, ts, "/links/"+i64s(l.ID)+"/tags", url.Values{"tag": {"  !!  "}})
	if code != http.StatusBadRequest {
		t.Errorf("status = %d", code)
	}
}

func TestTagsPage_listsAndCounts(t *testing.T) {
	ts, st := newTestServer(t)
	col, _ := st.CreateCollection(context.Background(), "c", "C", "")
	l, _ := st.CreateLink(context.Background(), col.ID, "https://x")
	tg, _ := st.UpsertTag(context.Background(), "go", "Go")
	_ = st.AttachTag(context.Background(), l.ID, tg.ID, storage.TagSourceUser)

	code, body := get(t, ts, "/tags")
	if code != 200 {
		t.Fatalf("status: %d", code)
	}
	if !strings.Contains(body, "Go") || !strings.Contains(body, "·1") {
		t.Errorf("tag cloud broken: %s", body)
	}
}

func TestTagDetail_404OnUnknown(t *testing.T) {
	ts, _ := newTestServer(t)
	code, _ := get(t, ts, "/tags/nope")
	if code != http.StatusNotFound {
		t.Errorf("status = %d", code)
	}
}

func TestTagsMerge(t *testing.T) {
	ts, st := newTestServer(t)
	col, _ := st.CreateCollection(context.Background(), "c", "C", "")
	l, _ := st.CreateLink(context.Background(), col.ID, "https://x")
	a, _ := st.UpsertTag(context.Background(), "go", "Go")
	b, _ := st.UpsertTag(context.Background(), "golang", "Golang")
	_ = st.AttachTag(context.Background(), l.ID, a.ID, storage.TagSourceUser)
	_ = st.AttachTag(context.Background(), l.ID, b.ID, storage.TagSourceUser)

	resp, err := ts.Client().PostForm(ts.URL+"/tags/merge", url.Values{"src": {"go"}, "dst": {"golang"}})
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	// Redirect → 200 after follow.
	if resp.StatusCode != 200 {
		t.Errorf("status: %d", resp.StatusCode)
	}
	tags, _ := st.ListTagsByLink(context.Background(), l.ID)
	if len(tags) != 1 || tags[0].Slug != "golang" {
		t.Errorf("merge result: %v", tags)
	}
}

func TestFeed_atomXMLForCollection(t *testing.T) {
	ts, st := newTestServer(t)
	col, _ := st.CreateCollection(context.Background(), "reading", "Reading", "")
	l, _ := st.CreateLink(context.Background(), col.ID, "https://example.com/x")
	_ = st.UpdateLinkExtraction(context.Background(), l.ID, "Hello", "d", "", "body", "en", "")
	_ = st.UpdateLinkSummary(context.Background(), l.ID, "tldr text")

	code, body := get(t, ts, "/c/reading/feed.xml")
	if code != 200 {
		t.Fatalf("status: %d", code)
	}
	for _, want := range []string{"<feed", "Hello", "tldr text"} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q in feed", want)
		}
	}
}

func TestBookmarkletPageAndAPI(t *testing.T) {
	ts, st := newTestServer(t)

	code, body := get(t, ts, "/bookmarklet")
	if code != 200 || !strings.Contains(body, "javascript:") {
		t.Errorf("page broken: %d", code)
	}

	// API auto-creates the default collection.
	resp, err := ts.Client().PostForm(ts.URL+"/api/links",
		url.Values{"url": {"https://news.example/article"}})
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Errorf("status: %d", resp.StatusCode)
	}
	col, err := st.GetCollectionBySlug(context.Background(), "default")
	if err != nil {
		t.Fatalf("default collection not created: %v", err)
	}
	links, _ := st.ListLinksByCollection(context.Background(), col.ID, 10, 0)
	if len(links) != 1 {
		t.Errorf("link not created: %d", len(links))
	}
}

func TestCollectionPage_showsCounters(t *testing.T) {
	ts, st := newTestServer(t)
	col, _ := st.CreateCollection(context.Background(), "ai", "AI", "")
	a, _ := st.CreateLink(context.Background(), col.ID, "https://x/1")
	b, _ := st.CreateLink(context.Background(), col.ID, "https://x/2")
	c, _ := st.CreateLink(context.Background(), col.ID, "https://x/3")
	_ = st.UpdateLinkExtraction(context.Background(), a.ID, "T", "d", "", "body", "en", "")
	_ = st.UpdateLinkSummary(context.Background(), a.ID, "tldr")
	_ = st.MarkLinkFailed(context.Background(), b.ID, "boom")
	_ = c

	code, body := get(t, ts, "/c/ai")
	if code != 200 {
		t.Fatalf("status: %d", code)
	}
	for _, want := range []string{"3 links", "1 ready", "1 processing", "1 failed"} {
		if !strings.Contains(body, want) {
			t.Errorf("missing counter %q in body", want)
		}
	}
}

func TestChatPage_emptyLibraryHint(t *testing.T) {
	st, _ := storage.Open(context.Background(), ":memory:")
	t.Cleanup(func() { _ = st.Close() })
	srv, err := New(config.Default(), st, nil,
		chat.New(st, search.New(st, nil), &fake.Backend{}), nil)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	code, body := get(t, ts, "/chat")
	if code != 200 {
		t.Fatalf("status: %d", code)
	}
	if !strings.Contains(body, "no summarised links yet") {
		t.Errorf("expected empty-library hint: %s", body)
	}
}

func TestLinkDetail_showsBigPreviewWhenAvailable(t *testing.T) {
	ts, st := newTestServer(t)
	col, _ := st.CreateCollection(context.Background(), "ai", "AI", "")
	l, _ := st.CreateLink(context.Background(), col.ID, "https://example.com/x")
	// Persist favicon + extra images via the full extraction setter so we
	// don't have to hit the network in this test.
	if err := st.UpdateLinkExtractionFull(context.Background(), l.ID,
		"Title", "desc",
		"https://example.com/cover.jpg",
		"https://example.com/favicon.ico",
		[]string{"https://example.com/extra-1.jpg", "https://example.com/extra-2.jpg"},
		"# heading\n\nbody", "en", ""); err != nil {
		t.Fatal(err)
	}
	_ = st.UpdateLinkSummary(context.Background(), l.ID, "tldr")

	code, body := get(t, ts, "/links/"+i64s(l.ID))
	if code != 200 {
		t.Fatalf("status: %d", code)
	}
	for _, want := range []string{
		`detail-preview-primary`,
		`https://example.com/cover.jpg`,
		`https://example.com/extra-1.jpg`,
		`https://example.com/extra-2.jpg`,
		`https://example.com/favicon.ico`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q on detail page", want)
		}
	}
	// previews-on by default.
	if !strings.Contains(body, `class="previews-on"`) {
		t.Errorf("body class wrong on detail page")
	}

	// With cookie show_previews=0 the body class flips → CSS hides .preview.
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/links/"+i64s(l.ID), nil)
	req.AddCookie(&http.Cookie{Name: "show_previews", Value: "0"})
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body2, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body2), `class="previews-off"`) {
		t.Errorf("expected previews-off class on detail with cookie")
	}
}

func TestLinkDetail_noPreviewSectionWhenNoImages(t *testing.T) {
	ts, st := newTestServer(t)
	col, _ := st.CreateCollection(context.Background(), "c", "C", "")
	l, _ := st.CreateLink(context.Background(), col.ID, "https://example.com/y")
	// No images at all.
	_ = st.UpdateLinkExtraction(context.Background(), l.ID,
		"T", "d", "", "body", "en", "")
	_ = st.UpdateLinkSummary(context.Background(), l.ID, "tldr")

	code, body := get(t, ts, "/links/"+i64s(l.ID))
	if code != 200 {
		t.Fatalf("status: %d", code)
	}
	if strings.Contains(body, "detail-preview-primary") {
		t.Errorf("preview section rendered with no images")
	}
}

func TestLinkDetail_showsGenerateSummaryBannerWhenFetched(t *testing.T) {
	ts, st := newTestServer(t)
	col, _ := st.CreateCollection(context.Background(), "c", "C", "")
	l, _ := st.CreateLink(context.Background(), col.ID, "https://example.com/x")
	// Status=fetched (extraction done, no summary yet).
	_ = st.UpdateLinkExtraction(context.Background(), l.ID,
		"T", "d", "", "body", "en", "")

	code, body := get(t, ts, "/links/"+i64s(l.ID))
	if code != 200 {
		t.Fatalf("status: %d", code)
	}
	if !strings.Contains(body, "No summary yet.") {
		t.Errorf("expected banner on a fetched link")
	}
	if !strings.Contains(body, `hx-post="/links/`+i64s(l.ID)+`/summarize"`) {
		t.Errorf("expected Generate summary button to POST to /summarize")
	}
}

func TestLinkDetail_noBannerWhenAlreadySummarized(t *testing.T) {
	ts, st := newTestServer(t)
	col, _ := st.CreateCollection(context.Background(), "c", "C", "")
	l, _ := st.CreateLink(context.Background(), col.ID, "https://example.com/x")
	_ = st.UpdateLinkExtraction(context.Background(), l.ID, "T", "d", "", "body", "en", "")
	_ = st.UpdateLinkSummary(context.Background(), l.ID, "tldr")

	code, body := get(t, ts, "/links/"+i64s(l.ID))
	if code != 200 {
		t.Fatalf("status: %d", code)
	}
	if strings.Contains(body, "No summary yet.") {
		t.Errorf("banner shown on a summarized link")
	}
}

func TestSidebar_listsActiveCollection(t *testing.T) {
	ts, st := newTestServer(t)
	st.CreateCollection(context.Background(), "alpha", "Alpha", "")
	st.CreateCollection(context.Background(), "bravo", "Bravo", "")

	// Home → "All" must be active, neither collection slug.
	_, body := get(t, ts, "/")
	if !strings.Contains(body, `class="sidebar-link active"`) {
		t.Errorf("home should have an active sidebar link")
	}
	if !strings.Contains(body, ">All<") {
		t.Errorf("All entry missing from sidebar")
	}

	// Inside /c/alpha → that link gets the active class.
	_, body = get(t, ts, "/c/alpha")
	// Look for a sidebar-link with the alpha href that's active.
	if !strings.Contains(body, `href="/c/alpha" class="sidebar-link active`) {
		t.Errorf("active class not applied to /c/alpha sidebar link: %s", body[:300])
	}
	// Bravo is present but not active.
	if !strings.Contains(body, `href="/c/bravo"`) {
		t.Errorf("bravo missing from sidebar")
	}
}

func TestSidebar_hiddenInReaderMode(t *testing.T) {
	ts, st := newTestServer(t)
	col, _ := st.CreateCollection(context.Background(), "c", "C", "")
	l, _ := st.CreateLink(context.Background(), col.ID, "https://x")
	_ = st.UpdateLinkExtraction(context.Background(), l.ID,
		"T", "d", "", "# h\n\nbody", "en", "")

	_, body := get(t, ts, "/links/"+i64s(l.ID)+"/read")
	if strings.Contains(body, `class="sidebar"`) {
		t.Errorf("reader mode should hide the sidebar")
	}
}

func TestAddLink_OOB_updatesCountersAndEmptyState(t *testing.T) {
	ts, st := newTestServer(t)
	if _, err := st.CreateCollection(context.Background(), "default", "Default", ""); err != nil {
		t.Fatal(err)
	}
	code, body := postForm(t, ts, "/c/default/links",
		url.Values{"url": {"https://example.com/x"}})
	if code != 200 {
		t.Fatalf("status=%d body=%s", code, body)
	}
	for _, want := range []string{
		`id="link-`,                              // the freshly inserted row
		`id="collection-stats-`,                  // OOB stats wrapper
		`hx-swap-oob="outerHTML"`,                // OOB attribute
		`1 link`,                                 // updated counter
		`id="links-empty"`,                       // empty-state OOB
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q in OOB response: %s", want, body)
		}
	}
}

func TestDeleteLink_OOB_recountsAndShowsEmptyState(t *testing.T) {
	ts, st := newTestServer(t)
	col, _ := st.CreateCollection(context.Background(), "c", "C", "")
	l, _ := st.CreateLink(context.Background(), col.ID, "https://x")

	code := deleteReq(t, ts, "/links/"+i64s(l.ID))
	if code != 200 {
		t.Fatalf("status=%d", code)
	}
}

func TestAddLink_hidesNoLinksYet(t *testing.T) {
	ts, st := newTestServer(t)
	if _, err := st.CreateCollection(context.Background(), "x", "X", ""); err != nil {
		t.Fatal(err)
	}
	// Empty-state visible on first load.
	_, body := get(t, ts, "/c/x")
	if !strings.Contains(body, "No links yet") {
		t.Fatalf("expected empty-state on freshly-created collection")
	}
	// After add, the OOB swap hides the empty-state placeholder.
	_, body = postForm(t, ts, "/c/x/links", url.Values{"url": {"https://x"}})
	if !strings.Contains(body, `id="links-empty" hx-swap-oob="outerHTML" hidden`) {
		t.Errorf("OOB hidden empty-state missing: %s", body)
	}
}

func TestCollectionStats_endpoint_returnsFragment(t *testing.T) {
	ts, st := newTestServer(t)
	col, _ := st.CreateCollection(context.Background(), "ai", "AI", "")
	_, _ = st.CreateLink(context.Background(), col.ID, "https://x")

	code, body := get(t, ts, "/c/ai/stats")
	if code != 200 {
		t.Fatalf("status=%d", code)
	}
	if !strings.Contains(body, "1 link") {
		t.Errorf("counter missing in fragment: %s", body)
	}
	if strings.Contains(body, "<html") {
		t.Errorf("expected fragment, got page")
	}
}

func TestLLMHealth_endpoint(t *testing.T) {
	ts, _ := newTestServer(t)
	code, body := get(t, ts, "/healthz/llm")
	if code != 200 {
		t.Fatalf("status: %d", code)
	}
	// No worker → "no LLM" badge.
	if !strings.Contains(body, "no LLM") {
		t.Errorf("expected 'no LLM' when worker is nil, got %q", body)
	}
}

func TestPreviewsToggle_defaultOnAndCookieFlips(t *testing.T) {
	ts, _ := newTestServer(t)

	// Default: home page should have body class previews-on.
	code, body := get(t, ts, "/")
	if code != 200 {
		t.Fatalf("home: %d", code)
	}
	if !strings.Contains(body, `class="previews-on"`) {
		t.Errorf("expected previews-on on body by default")
	}

	// POST /preferences/previews → should set show_previews=0 cookie + HX-Refresh.
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/preferences/previews", nil)
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("toggle: %d", resp.StatusCode)
	}
	if resp.Header.Get("HX-Refresh") != "true" {
		t.Errorf("missing HX-Refresh header")
	}
	var seenCookie bool
	for _, c := range resp.Cookies() {
		if c.Name == "show_previews" && c.Value == "0" {
			seenCookie = true
		}
	}
	if !seenCookie {
		t.Errorf("show_previews cookie not set to 0")
	}

	// Now request home with the cookie present → body class flips.
	req2, _ := http.NewRequest(http.MethodGet, ts.URL+"/", nil)
	req2.AddCookie(&http.Cookie{Name: "show_previews", Value: "0"})
	resp2, err := ts.Client().Do(req2)
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	body2, _ := io.ReadAll(resp2.Body)
	if !strings.Contains(string(body2), `class="previews-off"`) {
		t.Errorf("expected previews-off after toggle, got: %s", string(body2)[:200])
	}
}

func TestRefetchReindex_returnsFragmentWithQueuedBadge(t *testing.T) {
	st, err := storage.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	col, _ := st.CreateCollection(context.Background(), "c", "C", "")
	l, _ := st.CreateLink(context.Background(), col.ID, "https://x")
	_ = st.UpdateLinkExtraction(context.Background(), l.ID, "T", "d", "", "body", "en", "")
	_ = st.UpdateLinkSummary(context.Background(), l.ID, "tldr")

	// Real worker with a stub fetcher so ProcessOne doesn't blow up if it
	// runs synchronously fast enough to be visible from the test.
	wk := worker.New(st, &fake.Backend{GenerateText: `{"tldr":"x","tags":["x"]}`, EmbedDim: 4},
		stubReindexFetcher{body: "<html><title>x</title><body>plenty of body text here for readability to munch through plenty of body text here</body></html>"},
		config.Default(), worker.Options{})

	srv, err := New(config.Default(), st, nil, nil, wk)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	for _, path := range []string{"/links/" + i64s(l.ID) + "/refetch", "/links/" + i64s(l.ID) + "/reindex"} {
		code, body := postForm(t, ts, path, url.Values{})
		if code != 200 {
			t.Fatalf("%s status=%d body=%s", path, code, body)
		}
		// The fragment must contain the polling target id and a "queued"
		// badge so the user gets visual feedback.
		if !strings.Contains(body, "link-header-") {
			t.Errorf("%s: missing header anchor: %s", path, body)
		}
		if !strings.Contains(body, "queued") {
			t.Errorf("%s: missing 'queued' badge: %s", path, body)
		}
	}
}

// stubReindexFetcher serves canned HTML for the worker without going to network.
type stubReindexFetcher struct{ body string }

func (s stubReindexFetcher) Fetch(_ context.Context, _ string) (string, error) { return s.body, nil }

func TestLinkHeader_pollsUntilTerminal(t *testing.T) {
	st, _ := storage.Open(context.Background(), ":memory:")
	t.Cleanup(func() { _ = st.Close() })
	col, _ := st.CreateCollection(context.Background(), "c", "C", "")
	l, _ := st.CreateLink(context.Background(), col.ID, "https://x")

	srv, err := New(config.Default(), st, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// status=pending → fragment should carry hx-trigger="every 2s"
	code, body := get(t, ts, "/links/"+i64s(l.ID)+"/header")
	if code != 200 {
		t.Fatalf("status: %d", code)
	}
	if !strings.Contains(body, `hx-trigger="every 2s"`) {
		t.Errorf("expected polling attribute in pending header: %s", body)
	}

	// flip to summarized → polling attribute must disappear.
	_ = st.UpdateLinkSummary(context.Background(), l.ID, "ok")
	code, body = get(t, ts, "/links/"+i64s(l.ID)+"/header")
	if code != 200 {
		t.Fatalf("status: %d", code)
	}
	if strings.Contains(body, `hx-trigger="every 2s"`) {
		t.Errorf("polling should have stopped on summarized: %s", body)
	}
}

func TestRefetchReindex_503WhenNoWorker(t *testing.T) {
	ts, st := newTestServer(t)
	col, _ := st.CreateCollection(context.Background(), "c", "C", "")
	l, _ := st.CreateLink(context.Background(), col.ID, "https://x")
	for _, path := range []string{"/links/" + i64s(l.ID) + "/refetch", "/links/" + i64s(l.ID) + "/reindex"} {
		code, _ := postForm(t, ts, path, url.Values{})
		if code != http.StatusServiceUnavailable {
			t.Errorf("%s status = %d", path, code)
		}
	}
}

func TestChat_disabled503(t *testing.T) {
	ts, _ := newTestServer(t)
	code, _ := get(t, ts, "/chat")
	if code != 503 {
		t.Errorf("status = %d (expected 503 when chat is nil)", code)
	}
}

func TestChat_e2e_SSE_frameOrderAndCitations(t *testing.T) {
	st, err := storage.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	// Seed one summarised link with a chunk so RetrieveChunks finds it.
	col, _ := st.CreateCollection(context.Background(), "c", "C", "")
	l, _ := st.CreateLink(context.Background(), col.ID, "https://blog/rust")
	_ = st.UpdateLinkExtraction(context.Background(), l.ID,
		"Rust ownership", "ownership rules", "", "Rust uses ownership for safety.", "en", "")
	_ = st.UpdateLinkSummary(context.Background(), l.ID, "primer on rust ownership")
	_, _ = st.InsertChunks(context.Background(), l.ID,
		[]string{"Rust uses ownership for compile-time memory safety."})

	streamer := &fake.Backend{
		StreamChunks: []llm.StreamChunk{
			{Text: "Rust "}, {Text: "uses "}, {Text: "ownership"},
			{Text: " [src:"}, {Text: "1]"}, {Done: true},
		},
		EmbedDim: 8,
	}
	eng := search.New(st, streamer)
	chatSvc := chat.New(st, eng, streamer)

	srv, err := New(config.Default(), st, eng, chatSvc, nil)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/chat/stream",
		strings.NewReader("message=rust+ownership&collection_id="+i64s(col.ID)))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status: %d", resp.StatusCode)
	}

	// Parse SSE frames into (event, data) pairs.
	body, _ := io.ReadAll(resp.Body)
	var events []struct{ Event, Data string }
	for _, block := range strings.Split(string(body), "\n\n") {
		if strings.TrimSpace(block) == "" {
			continue
		}
		var ev, da string
		for _, ln := range strings.Split(block, "\n") {
			if strings.HasPrefix(ln, "event: ") {
				ev = strings.TrimPrefix(ln, "event: ")
			} else if strings.HasPrefix(ln, "data: ") {
				da += strings.TrimPrefix(ln, "data: ")
			}
		}
		events = append(events, struct{ Event, Data string }{ev, da})
	}

	// Required ordering: session first, then ≥1 source, then ≥1 token,
	// then a stats event (final), then done.
	wantSeq := []string{"session", "source", "token", "stats", "done"}
	posInSeq := 0
	var allTokens []string
	var lastStats string
	for _, e := range events {
		if posInSeq < len(wantSeq) && e.Event == wantSeq[posInSeq] {
			posInSeq++
		}
		switch e.Event {
		case "token":
			allTokens = append(allTokens, e.Data)
		case "stats":
			lastStats = e.Data
		}
	}
	if posInSeq < len(wantSeq) {
		t.Errorf("missing required event sequence (got through %d/%d): %+v", posInSeq, len(wantSeq), events)
	}
	if lastStats == "" {
		t.Errorf("no stats event")
	} else if !strings.Contains(lastStats, "|") {
		t.Errorf("stats event malformed: %q", lastStats)
	}
	answer := strings.Join(allTokens, "")
	if !strings.Contains(answer, "ownership") {
		t.Errorf("answer missing keyword: %q", answer)
	}
	if !strings.Contains(answer, "[src:") {
		t.Errorf("citation tag missing in answer: %q", answer)
	}

	// Persistence: user + assistant rows.
	for _, sess := range []int64{1} {
		msgs, _ := st.RecentChatMessages(context.Background(), sess, 10)
		if len(msgs) != 2 {
			t.Errorf("session %d msgs = %d (want 2)", sess, len(msgs))
		}
	}
}

func TestChat_streamSSE(t *testing.T) {
	st, err := storage.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	col, _ := st.CreateCollection(context.Background(), "c", "C", "")

	streamer := &fake.Backend{
		StreamChunks: []llm.StreamChunk{{Text: "hello "}, {Text: "world"}, {Done: true}},
	}
	eng := search.New(st, nil) // BM25-only path is fine; collection has no chunks
	chatSvc := chat.New(st, eng, streamer)

	srv, err := New(config.Default(), st, eng, chatSvc, nil)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := ts.Client().PostForm(ts.URL+"/chat/stream",
		url.Values{"message": {"hi"}, "collection_id": {i64s(col.ID)}})
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Errorf("content-type = %q", ct)
	}
	body, _ := io.ReadAll(resp.Body)
	stream := string(body)
	for _, want := range []string{"event: session", "event: token", "data: hello", "event: done"} {
		if !strings.Contains(stream, want) {
			t.Errorf("missing %q in stream:\n%s", want, stream)
		}
	}
}

func i64s(n int64) string {
	return strings.TrimSpace(itoa(n))
}

// itoa avoids importing strconv just for one call in tests.
func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
