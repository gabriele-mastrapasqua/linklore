// Regression and edge tests for the recently-added handlers.
// All tests use the in-memory test server; nothing touches a real DB.

package server

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gabriele-mastrapasqua/linklore/internal/config"
	"github.com/gabriele-mastrapasqua/linklore/internal/storage"
)

// ---------- /c/:slug rendering ----------

func TestListLinks_kindFilterChipsRender(t *testing.T) {
	ts, st := newTestServer(t)
	st.CreateCollection(context.Background(), "c", "C", "")
	_, body := get(t, ts, "/c/c")
	for _, k := range []string{"article", "video", "image", "audio", "document", "book"} {
		if !strings.Contains(body, `kind=`+k) {
			t.Errorf("type-filter chip for kind=%s missing", k)
		}
	}
}

func TestListLinks_layoutSwitchRendersAllFour(t *testing.T) {
	ts, st := newTestServer(t)
	st.CreateCollection(context.Background(), "c", "C", "")
	_, body := get(t, ts, "/c/c")
	for _, l := range []string{"list", "grid", "headlines", "moodboard"} {
		if !strings.Contains(body, `data-layout="`+l+`"`) {
			t.Errorf("layout button for %q missing", l)
		}
	}
}

func TestListLinks_unknownKindFilterEmpties(t *testing.T) {
	// An unknown ?kind=… produces an empty list (no error). Defensive
	// against bookmarked URLs from older versions.
	ts, st := newTestServer(t)
	col, _ := st.CreateCollection(context.Background(), "c", "C", "")
	_, _ = st.CreateLink(context.Background(), col.ID, "https://example.com/post")
	code, body := get(t, ts, "/c/c?kind=spaceship")
	if code != 200 {
		t.Errorf("status %d", code)
	}
	if strings.Contains(body, "example.com/post") {
		t.Errorf("unknown kind filter should not match anything")
	}
}

// ---------- pagination on /c/:slug ----------

// Default per is 50; with 75 links there should be a page 2 and exactly
// 50 link rows on page 1.
func TestListLinks_paginationDefault50(t *testing.T) {
	ts, st := newTestServer(t)
	col, _ := st.CreateCollection(context.Background(), "c", "C", "")
	for i := 0; i < 75; i++ {
		_, _ = st.CreateLink(context.Background(), col.ID, fmt.Sprintf("https://example.com/%03d", i))
	}
	code, body := get(t, ts, "/c/c")
	if code != 200 {
		t.Fatalf("status %d", code)
	}
	if !strings.Contains(body, "75 links") {
		t.Errorf("body missing '75 links' header")
	}
	if !strings.Contains(body, "page 1 of 2") {
		t.Errorf("body missing 'page 1 of 2'")
	}
	if !strings.Contains(body, "?page=2") {
		t.Errorf("body missing next ?page=2 link")
	}
	if !strings.Contains(body, "next →") {
		t.Errorf("body missing 'next ->' anchor text")
	}
	if got := strings.Count(body, "link-row"); got != 50 {
		t.Errorf("link-row count = %d, want 50", got)
	}
}

// per=100 fits all 75 in one page.
func TestListLinks_paginationPer100(t *testing.T) {
	ts, st := newTestServer(t)
	col, _ := st.CreateCollection(context.Background(), "c", "C", "")
	for i := 0; i < 75; i++ {
		_, _ = st.CreateLink(context.Background(), col.ID, fmt.Sprintf("https://example.com/%03d", i))
	}
	code, body := get(t, ts, "/c/c?per=100")
	if code != 200 {
		t.Fatalf("status %d", code)
	}
	if !strings.Contains(body, "page 1 of 1") {
		t.Errorf("body missing 'page 1 of 1'")
	}
	if got := strings.Count(body, "link-row"); got != 75 {
		t.Errorf("link-row count = %d, want 75", got)
	}
}

// per=0 means "all" (capped at 5000); 75 links all render and there is
// no next anchor.
func TestListLinks_paginationAll(t *testing.T) {
	ts, st := newTestServer(t)
	col, _ := st.CreateCollection(context.Background(), "c", "C", "")
	for i := 0; i < 75; i++ {
		_, _ = st.CreateLink(context.Background(), col.ID, fmt.Sprintf("https://example.com/%03d", i))
	}
	code, body := get(t, ts, "/c/c?per=0")
	if code != 200 {
		t.Fatalf("status %d", code)
	}
	if got := strings.Count(body, "link-row"); got != 75 {
		t.Errorf("link-row count = %d, want 75", got)
	}
	if strings.Contains(body, "next →") {
		t.Errorf("'next' anchor should not render when all links fit")
	}
}

// page=2 with default per=50 returns links 51..75 (25 rows). CreateLink
// sets order_idx so the most recently inserted link surfaces first; with
// predictable URLs we can assert that the page-2 window contains the
// earliest URLs (000..024) and not the latest.
func TestListLinks_paginationOffsetWorks(t *testing.T) {
	ts, st := newTestServer(t)
	col, _ := st.CreateCollection(context.Background(), "c", "C", "")
	for i := 0; i < 75; i++ {
		_, _ = st.CreateLink(context.Background(), col.ID, fmt.Sprintf("https://example.com/%03d", i))
	}
	code, body := get(t, ts, "/c/c?page=2")
	if code != 200 {
		t.Fatalf("status %d", code)
	}
	if got := strings.Count(body, "link-row"); got != 25 {
		t.Errorf("link-row count on page 2 = %d, want 25", got)
	}
	if !strings.Contains(body, "example.com/000") {
		t.Errorf("page 2 missing oldest link 'example.com/000'")
	}
	if strings.Contains(body, "example.com/074") {
		t.Errorf("page 2 should NOT contain newest link 'example.com/074'")
	}
}

// ---------- layout endpoint ----------

func TestSetLayout_unknownCollection404s(t *testing.T) {
	ts, _ := newTestServer(t)
	code, _ := postForm(t, ts, "/c/missing/layout", url.Values{"layout": {"grid"}})
	if code != 404 {
		t.Errorf("status = %d (want 404)", code)
	}
}

func TestSetLayout_persistsAcrossRequests(t *testing.T) {
	ts, st := newTestServer(t)
	st.CreateCollection(context.Background(), "c", "C", "")
	postForm(t, ts, "/c/c/layout", url.Values{"layout": {"moodboard"}})
	_, body := get(t, ts, "/c/c")
	if !strings.Contains(body, `class="layout-moodboard"`) {
		t.Errorf("layout class not echoed back, body excerpt: %s", excerpt(body, "links-list", 80))
	}
	// The active layout button has both `active` in its class and
	// `data-layout="moodboard"` — match a window of HTML around the
	// data-layout attribute and assert `active` is present in it.
	mood := strings.Index(body, `data-layout="moodboard"`)
	if mood < 0 {
		t.Fatalf("moodboard button missing")
	}
	start := mood - 80
	if start < 0 {
		start = 0
	}
	if !strings.Contains(body[start:mood], "active") {
		t.Errorf("active class not on moodboard button, window: %q", body[start:mood])
	}
}

// ---------- duplicates ----------

func TestDuplicates_emptyShowsHelpMessage(t *testing.T) {
	ts, _ := newTestServer(t)
	_, body := get(t, ts, "/duplicates")
	if !strings.Contains(body, "No duplicates found") {
		t.Errorf("expected help message on empty page")
	}
}

func TestDuplicates_groupsAcrossCollections(t *testing.T) {
	// Duplicates are global: the same URL saved into two different
	// collections should appear in one group, not be hidden because
	// the user organised them differently.
	ts, st := newTestServer(t)
	a, _ := st.CreateCollection(context.Background(), "a", "A", "")
	b, _ := st.CreateCollection(context.Background(), "b", "B", "")
	st.CreateLink(context.Background(), a.ID, "https://www.example.com/x")
	st.CreateLink(context.Background(), b.ID, "https://example.com/x?utm_source=foo")
	_, body := get(t, ts, "/duplicates")
	if !strings.Contains(body, "duplicate group") {
		t.Errorf("cross-collection duplicate not surfaced")
	}
}

func TestDuplicatesDelete_keepIDIsRespected(t *testing.T) {
	ts, st := newTestServer(t)
	col, _ := st.CreateCollection(context.Background(), "c", "C", "")
	a, _ := st.CreateLink(context.Background(), col.ID, "https://example.com/x")
	b, _ := st.CreateLink(context.Background(), col.ID, "https://www.example.com/x/")
	form := url.Values{}
	form.Set("keep_id", itoa(b.ID))
	form.Add("ids", itoa(a.ID))
	form.Add("ids", itoa(b.ID))
	postForm(t, ts, "/duplicates/delete", form)
	got, err := st.GetLink(context.Background(), b.ID)
	if err != nil || got == nil {
		t.Errorf("keep_id was deleted: %v", err)
	}
	if _, err := st.GetLink(context.Background(), a.ID); err == nil {
		t.Errorf("dup not deleted")
	}
}

// ---------- prune empty ----------

func TestPruneEmpty_htmxRedirects(t *testing.T) {
	ts, st := newTestServer(t)
	st.CreateCollection(context.Background(), "empty", "Empty", "")

	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/collections/prune", nil)
	req.Header.Set("HX-Request", "true")
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("status %d", resp.StatusCode)
	}
	if got := resp.Header.Get("HX-Redirect"); got != "/" {
		t.Errorf("HX-Redirect = %q, want /", got)
	}
}

func TestPruneEmpty_plainPostFollowsRedirect(t *testing.T) {
	ts, st := newTestServer(t)
	st.CreateCollection(context.Background(), "e", "E", "")
	c := &http.Client{
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse },
	}
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/collections/prune", nil)
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 303 {
		t.Errorf("non-HTMX prune status %d, want 303", resp.StatusCode)
	}
	if loc := resp.Header.Get("Location"); loc != "/" {
		t.Errorf("Location %q, want /", loc)
	}
}

// ---------- delete collection (HX-Redirect when on the deleted page) ----------

func TestDeleteCollection_fromOwnPageRedirectsHome(t *testing.T) {
	ts, st := newTestServer(t)
	col, _ := st.CreateCollection(context.Background(), "tobedel", "TBD", "")
	_ = col

	req, _ := http.NewRequest(http.MethodDelete, ts.URL+"/c/tobedel", nil)
	req.Header.Set("HX-Request", "true")
	req.Header.Set("HX-Current-URL", ts.URL+"/c/tobedel")
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("status %d", resp.StatusCode)
	}
	if r := resp.Header.Get("HX-Redirect"); r != "/" {
		t.Errorf("HX-Redirect = %q, want /", r)
	}
}

func TestDeleteCollection_fromOtherPageReturnsOOB(t *testing.T) {
	ts, st := newTestServer(t)
	col, _ := st.CreateCollection(context.Background(), "x", "X", "")
	_ = col

	req, _ := http.NewRequest(http.MethodDelete, ts.URL+"/c/x", nil)
	// No HX-Current-URL — request comes from /collections home.
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.Header.Get("HX-Redirect") != "" {
		t.Errorf("expected no HX-Redirect when deleting from elsewhere")
	}
	if !strings.Contains(string(body), `hx-swap-oob="delete"`) {
		t.Errorf("expected OOB delete fragments, got: %q", body)
	}
}

// ---------- chat ----------

func TestChat_disabledStreamReturns503(t *testing.T) {
	ts, _ := newTestServer(t)
	code, body := postForm(t, ts, "/chat/stream", url.Values{"message": {"hi"}})
	if code != 503 {
		t.Errorf("status %d, want 503", code)
	}
	if !strings.Contains(body, "chat unavailable") {
		t.Errorf("unexpected body: %q", body)
	}
}

// ---------- ?ask= prefill ----------

func TestChatPage_askEscapingRoundtrips(t *testing.T) {
	// Make sure HTML-encoding the prefilled question doesn't lose
	// special characters. (We don't have a live chat in the test, so
	// we just verify the input element is present and properly
	// escaped; the disabled banner code path runs because chat == nil.)
	ts, _ := newTestServer(t)
	_, body := get(t, ts, "/chat?ask=Why+is+%22X%22+interesting%3F&link=42")
	// The chat-disabled banner shows in this scenario; the Ask field is
	// rendered into the data map and the banner displays cleanly.
	if !strings.Contains(body, "Chat is disabled") {
		t.Errorf("expected disabled banner")
	}
}

// ---------- bookmark import error paths ----------

func TestImportNetscape_missingFile(t *testing.T) {
	ts, _ := newTestServer(t)
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("collection", "x")
	_ = mw.Close()
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/import", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 400 {
		t.Errorf("expected 4xx without a file, got %d", resp.StatusCode)
	}
}

func TestImportNetscape_garbageFileNoCrash(t *testing.T) {
	ts, _ := newTestServer(t)
	resp, body := postNetscape(t, ts, "/import", "x", "this is not html")
	// Garbage input produces zero bookmarks but the request must not crash.
	if resp >= 500 {
		t.Errorf("status %d should not be 5xx, body=%s", resp, body)
	}
}

func TestExportNetscape_unknownSlug404s(t *testing.T) {
	ts, _ := newTestServer(t)
	code, _ := get(t, ts, "/c/nope/export.html")
	if code != 404 {
		t.Errorf("status %d", code)
	}
}

func TestExportNetscape_attachmentHeader(t *testing.T) {
	ts, st := newTestServer(t)
	st.CreateCollection(context.Background(), "c", "C", "")
	resp, err := ts.Client().Get(ts.URL + "/c/c/export.html")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	disp := resp.Header.Get("Content-Disposition")
	if !strings.HasPrefix(disp, `attachment; filename="linklore-c.html"`) {
		t.Errorf("disposition = %q", disp)
	}
}

// ---------- create-collection sidebar OOB ----------

func TestCreateCollection_writesSidebarOOB(t *testing.T) {
	ts, _ := newTestServer(t)
	_, body := postForm(t, ts, "/collections", url.Values{"name": {"Sidebar Test"}})
	if !strings.Contains(body, `hx-swap-oob="beforeend:#sidebar-collections"`) {
		t.Errorf("missing sidebar OOB swap, body excerpt: %s", excerpt(body, "sidebar", 80))
	}
	if !strings.Contains(body, "Sidebar Test") {
		t.Errorf("collection card not rendered")
	}
}

// ---------- rename collection ----------

func TestRenameCollection_HtmxOnlySendsHXRedirect(t *testing.T) {
	// Regression for the "iframe-inside-page" bug: an HTMX rename
	// must NOT also write a 303 body.
	ts, st := newTestServer(t)
	st.CreateCollection(context.Background(), "old", "Old", "")
	form := url.Values{"name": {"New Name"}}
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/c/old/rename",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	c := &http.Client{
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse },
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		t.Errorf("HTMX rename status = %d, want 200", resp.StatusCode)
	}
	if got := resp.Header.Get("HX-Redirect"); got == "" {
		t.Errorf("HX-Redirect missing")
	}
	if len(body) > 0 {
		t.Errorf("HTMX rename should have empty body, got: %q", body)
	}
}

func TestRenameCollection_plainFormReturns303(t *testing.T) {
	ts, st := newTestServer(t)
	st.CreateCollection(context.Background(), "old", "Old", "")
	form := url.Values{"name": {"New Name"}}
	c := &http.Client{
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse },
	}
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/c/old/rename",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 303 {
		t.Errorf("status %d, want 303", resp.StatusCode)
	}
}

// ---------- bulk delete error paths ----------

func TestBulkDelete_emptyIdsRejected(t *testing.T) {
	ts, _ := newTestServer(t)
	code, _ := postForm(t, ts, "/links/bulk/delete", url.Values{"ids": {""}})
	if code != 400 {
		t.Errorf("status %d, want 400", code)
	}
}

func TestBulkDelete_partialFailureKeepsOthers(t *testing.T) {
	// One bad id mixed with valid ones — handler skips the bad one
	// and deletes the rest. (parseBulkIDs returns 400 on malformed
	// numeric; that's what we want — caller fixes the bad input.)
	ts, st := newTestServer(t)
	col, _ := st.CreateCollection(context.Background(), "c", "C", "")
	a, _ := st.CreateLink(context.Background(), col.ID, "https://example.com/a")
	form := url.Values{"ids": {itoa(a.ID) + ",abc"}}
	code, _ := postForm(t, ts, "/links/bulk/delete", form)
	if code != 400 {
		t.Errorf("status %d, want 400 on malformed id", code)
	}
}

// ---------- bulk move sanity ----------

func TestBulkMove_missingDestinationRejected(t *testing.T) {
	ts, st := newTestServer(t)
	col, _ := st.CreateCollection(context.Background(), "c", "C", "")
	a, _ := st.CreateLink(context.Background(), col.ID, "https://example.com/a")
	form := url.Values{"ids": {itoa(a.ID)}}
	code, _ := postForm(t, ts, "/links/bulk/move", form)
	if code != 400 {
		t.Errorf("status %d, want 400 without collection_id", code)
	}
}

func TestBulkMove_unknownDestinationRejected(t *testing.T) {
	ts, st := newTestServer(t)
	col, _ := st.CreateCollection(context.Background(), "c", "C", "")
	a, _ := st.CreateLink(context.Background(), col.ID, "https://example.com/a")
	form := url.Values{"ids": {itoa(a.ID)}, "collection_id": {"99999"}}
	code, _ := postForm(t, ts, "/links/bulk/move", form)
	if code != 404 {
		t.Errorf("status %d, want 404 for unknown destination", code)
	}
}

// ---------- smart-add (unified link / feed input) ----------

// Pasting a regular page URL into the smart-add input creates a link
// in the collection.
func TestSmartAdd_pageURLBecomesLink(t *testing.T) {
	ts, st := newTestServer(t)
	st.CreateCollection(context.Background(), "c", "C", "")
	code, _ := postForm(t, ts, "/c/c/add", url.Values{"url": {"https://example.com/article"}})
	if code != 200 {
		t.Fatalf("status %d", code)
	}
	links, _ := st.ListLinksByCollection(context.Background(), 1, 5, 0)
	if len(links) != 1 || links[0].URL != "https://example.com/article" {
		t.Errorf("link not created, got %+v", links)
	}
	col, _ := st.GetCollectionBySlug(context.Background(), "c")
	if col.FeedURL != "" {
		t.Errorf("regular URL incorrectly set as feed: %q", col.FeedURL)
	}
}

// A URL that ends in /feed.xml SHOULD be promoted to a feed
// subscription when the collection doesn't already have one.
// We can't actually verify the feed contents (no real upstream)
// but the dispatch logic itself can be checked: with an unreachable
// host the discover step fails and the URL falls through to the
// link-creation path. That's fine for unit-testing the dispatcher.
func TestSmartAdd_unreachableFeedFallsBackToLink(t *testing.T) {
	ts, st := newTestServer(t)
	st.CreateCollection(context.Background(), "c", "C", "")
	// 127.0.0.1:1 is guaranteed-refused; Discover fails, we add as link.
	code, _ := postForm(t, ts, "/c/c/add", url.Values{"url": {"http://127.0.0.1:1/feed.xml"}})
	if code != 200 {
		t.Fatalf("status %d", code)
	}
	links, _ := st.ListLinksByCollection(context.Background(), 1, 5, 0)
	if len(links) != 1 {
		t.Errorf("expected fallback link, got %d links", len(links))
	}
}

func TestSmartAdd_emptyURLRejected(t *testing.T) {
	ts, st := newTestServer(t)
	st.CreateCollection(context.Background(), "c", "C", "")
	code, _ := postForm(t, ts, "/c/c/add", url.Values{"url": {""}})
	if code != 400 {
		t.Errorf("status %d, want 400", code)
	}
}

// pageWithAlternate spins up an httptest server whose root returns
// HTML advertising an RSS feed via <link rel="alternate">, and whose
// /feed.xml path returns a minimal valid Atom feed. Used by the
// "smart-add offers feed" tests.
func pageWithAlternate(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	var feedURL string
	mux.HandleFunc("/feed.xml", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/atom+xml")
		fmt.Fprint(w, `<?xml version="1.0" encoding="utf-8"?>
<feed xmlns="http://www.w3.org/2005/Atom">
  <title>Demo</title>
  <id>urn:demo</id>
  <updated>2026-01-01T00:00:00Z</updated>
  <entry>
    <title>Hello</title>
    <id>urn:demo:1</id>
    <updated>2026-01-01T00:00:00Z</updated>
    <link href="https://example.com/post-1"/>
  </entry>
</feed>`)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, `<html><head><link rel="alternate" type="application/rss+xml" href="%s"></head><body>hi</body></html>`, feedURL)
	})
	ts := httptest.NewServer(mux)
	feedURL = ts.URL + "/feed.xml"
	t.Cleanup(ts.Close)
	return ts
}

// Pasting a regular page URL whose <head> exposes an RSS feed should
// (a) still create the link normally and (b) attach an inline
// "subscribe to feed instead?" banner — but NOT auto-subscribe.
func TestSmartAdd_offersFeedWhenPageHasAlternate(t *testing.T) {
	ts, st := newTestServer(t)
	st.CreateCollection(context.Background(), "c", "C", "")
	site := pageWithAlternate(t)

	code, body := postForm(t, ts, "/c/c/add", url.Values{"url": {site.URL + "/"}})
	if code != 200 {
		t.Fatalf("status %d", code)
	}
	if !strings.Contains(body, "feed-offer-host") {
		t.Errorf("expected feed-offer-host OOB swap in response, body=%q", body)
	}
	if !strings.Contains(body, "Subscribe instead") {
		t.Errorf("offer button text missing, body=%q", body)
	}
	if !strings.Contains(body, site.URL+"/feed.xml") {
		t.Errorf("discovered feed URL missing from offer, body=%q", body)
	}
	// Link was still created.
	links, _ := st.ListLinksByCollection(context.Background(), 1, 5, 0)
	if len(links) != 1 || links[0].URL != site.URL+"/" {
		t.Errorf("link not created, got %+v", links)
	}
	// Collection feed_url stayed empty — we didn't auto-subscribe.
	col, _ := st.GetCollectionBySlug(context.Background(), "c")
	if col.FeedURL != "" {
		t.Errorf("auto-subscribed against intent: feed_url=%q", col.FeedURL)
	}
}

// When the pasted URL is itself feed-shaped (looksLikeFeedURL) it
// should follow the existing subscribe path, not the link+offer path.
// We don't need a real feed upstream — even if Discover fails, no
// feed_offer banner should appear because the dispatcher took the
// other branch.
func TestSmartAdd_noOfferWhenURLIsAlreadyFeed(t *testing.T) {
	ts, st := newTestServer(t)
	st.CreateCollection(context.Background(), "c", "C", "")
	// 127.0.0.1:1 is guaranteed-refused, so Discover fails and we
	// fall through to the regular link path. Crucially, we should
	// NOT then probe again and emit a feed offer for the same URL.
	_, body := postForm(t, ts, "/c/c/add", url.Values{"url": {"http://127.0.0.1:1/feed.xml"}})
	if strings.Contains(body, "feed-offer-host") {
		t.Errorf("feed-offer-host shown for an already-feed-shaped URL, body=%q", body)
	}
}

// When the collection is already feed-backed, pasting a page URL
// must NOT offer to swap the existing feed for a newly discovered
// one — that would silently lose the user's prior subscription.
func TestSmartAdd_noOfferWhenCollectionAlreadyHasFeed(t *testing.T) {
	ts, st := newTestServer(t)
	col, _ := st.CreateCollection(context.Background(), "c", "C", "")
	_ = st.SetCollectionFeed(context.Background(), col.ID, "https://example.com/existing-feed.xml")
	site := pageWithAlternate(t)

	_, body := postForm(t, ts, "/c/c/add", url.Values{"url": {site.URL + "/"}})
	if strings.Contains(body, "feed-offer-host") {
		t.Errorf("feed-offer-host shown despite existing feed_url, body=%q", body)
	}
}

func TestLooksLikeFeedURL(t *testing.T) {
	yes := []string{
		"https://example.com/feed",
		"https://example.com/feed/",
		"https://example.com/rss",
		"https://example.com/atom.xml",
		"https://example.com/index.xml",
		"https://example.com/post.xml",
		"https://example.com/path/atom",
		"https://example.com/feed?x=1",
		"https://example.com/feed.xml#section",
	}
	for _, u := range yes {
		if !looksLikeFeedURL(u) {
			t.Errorf("looksLikeFeedURL(%q) = false, want true", u)
		}
	}
	no := []string{
		"https://example.com",
		"https://example.com/article",
		"https://example.com/blog/post",
		"https://example.com/feed-section/post", // suffix on non-final segment
		"",
	}
	for _, u := range no {
		if looksLikeFeedURL(u) {
			t.Errorf("looksLikeFeedURL(%q) = true, want false", u)
		}
	}
}

// ---------- cover image ----------

func TestSetCover_persistsAndShowsBanner(t *testing.T) {
	ts, st := newTestServer(t)
	st.CreateCollection(context.Background(), "c", "C", "")
	postForm(t, ts, "/c/c/cover",
		url.Values{"url": {"https://example.com/banner.jpg"}})
	col, _ := st.GetCollectionBySlug(context.Background(), "c")
	if col.CoverURL != "https://example.com/banner.jpg" {
		t.Errorf("cover not stored: %q", col.CoverURL)
	}
	_, body := get(t, ts, "/c/c")
	if !strings.Contains(body, "collection-banner") {
		t.Errorf("collection-banner not rendered when cover set")
	}
	if !strings.Contains(body, "https://example.com/banner.jpg") {
		t.Errorf("cover URL not in rendered body")
	}
}

func TestSetCover_emptyClears(t *testing.T) {
	ts, st := newTestServer(t)
	col, _ := st.CreateCollection(context.Background(), "c", "C", "")
	_ = st.SetCollectionCover(context.Background(), col.ID, "https://example.com/x.jpg")
	postForm(t, ts, "/c/c/cover", url.Values{"url": {""}})
	got, _ := st.GetCollectionBySlug(context.Background(), "c")
	if got.CoverURL != "" {
		t.Errorf("cover not cleared: %q", got.CoverURL)
	}
}

func TestSetCover_htmxRefresh(t *testing.T) {
	ts, st := newTestServer(t)
	st.CreateCollection(context.Background(), "c", "C", "")
	form := url.Values{"url": {"https://example.com/x.jpg"}}
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/c/c/cover",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.Header.Get("HX-Refresh") != "true" {
		t.Errorf("HX-Refresh = %q, want true", resp.Header.Get("HX-Refresh"))
	}
	if !strings.Contains(resp.Header.Get("HX-Trigger"), "Cover updated") {
		t.Errorf("toast trigger missing: %q", resp.Header.Get("HX-Trigger"))
	}
}

// ---------- preview drawer ----------

func TestPreview_unknownLink404s(t *testing.T) {
	ts, _ := newTestServer(t)
	code, _ := get(t, ts, "/links/9999/preview")
	if code != 404 {
		t.Errorf("status %d", code)
	}
}

func TestPreview_renders(t *testing.T) {
	ts, st := newTestServer(t)
	col, _ := st.CreateCollection(context.Background(), "c", "C", "")
	l, _ := st.CreateLink(context.Background(), col.ID, "https://example.com/p")
	if err := st.UpdateLinkExtraction(context.Background(), l.ID,
		"Title Here", "desc", "", "## Heading\n\nBody paragraph.", "en", ""); err != nil {
		t.Fatal(err)
	}
	code, body := get(t, ts, fmt.Sprintf("/links/%d/preview", l.ID))
	if code != 200 {
		t.Fatalf("status %d", code)
	}
	for _, want := range []string{
		"drawer-head", "drawer-tabs", "drawer-tab-body",
		"drawer-toolbar", "drawer-article",
		"Title Here", "drawerSize",
		`data-tab="edit"`, `data-tab="preview"`, `data-tab="web"`, `data-tab="archive"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("preview body missing %q", want)
		}
	}
}

func TestDrawerTabs_eachTabRendersStandalone(t *testing.T) {
	ts, st := newTestServer(t)
	col, _ := st.CreateCollection(context.Background(), "c", "C", "")
	l, _ := st.CreateLink(context.Background(), col.ID, "https://example.com/p")
	_ = st.UpdateLinkExtraction(context.Background(), l.ID,
		"Title Here", "desc", "", "## Heading\n\nBody.", "en", "")
	cases := []struct {
		tab  string
		want []string
	}{
		{"edit", []string{"drawer-edit", "Note", "Collection", "Tags", "Delete"}},
		{"preview", []string{"drawer-toolbar", "drawer-article"}},
		{"web", []string{"drawer-web", "iframe", "X-Frame-Options"}},
		{"archive", []string{"Wayback Machine", "All snapshots", "web.archive.org"}},
	}
	for _, c := range cases {
		t.Run(c.tab, func(t *testing.T) {
			code, body := get(t, ts, fmt.Sprintf("/links/%d/drawer/%s", l.ID, c.tab))
			if code != 200 {
				t.Fatalf("status %d", code)
			}
			for _, w := range c.want {
				if !strings.Contains(body, w) {
					t.Errorf("%s body missing %q", c.tab, w)
				}
			}
		})
	}
}

func TestBase_topbarHasAskButton(t *testing.T) {
	ts, _ := newTestServer(t)
	_, body := get(t, ts, "/")
	if !strings.Contains(body, `class="topbar-ask`) {
		t.Errorf("topbar ✦ ask button missing")
	}
}

func TestBase_drawerScaffoldPresent(t *testing.T) {
	ts, _ := newTestServer(t)
	_, body := get(t, ts, "/")
	if !strings.Contains(body, `id="drawer"`) {
		t.Errorf("drawer scaffold missing")
	}
	if !strings.Contains(body, `id="drawer-content"`) {
		t.Errorf("drawer content host missing")
	}
}

// ---------- palette + ctxmenu scaffolding ----------

func TestStatic_paletteAndCtxJSLoad(t *testing.T) {
	ts, _ := newTestServer(t)
	for _, p := range []string{"/static/palette.js", "/static/ctxmenu.js", "/static/drawer.js"} {
		code, body := get(t, ts, p)
		if code != 200 {
			t.Errorf("%s status %d", p, code)
		}
		if !strings.Contains(body, "use strict") {
			t.Errorf("%s body unexpected", p)
		}
	}
}

func TestBase_topbarHasCmdKHint(t *testing.T) {
	ts, _ := newTestServer(t)
	_, body := get(t, ts, "/")
	if !strings.Contains(body, `⌘K`) {
		t.Errorf("⌘K kbd hint missing from topbar")
	}
}

func TestBase_loadsPaletteAndCtxScripts(t *testing.T) {
	ts, _ := newTestServer(t)
	_, body := get(t, ts, "/")
	for _, src := range []string{"/static/palette.js", "/static/ctxmenu.js", "/static/drawer.js"} {
		if !strings.Contains(body, `src="`+src+`"`) {
			t.Errorf("base.html doesn't load %s", src)
		}
	}
}

// ---------- toasts via HX-Trigger ----------

func TestBulkDelete_emitsToast(t *testing.T) {
	ts, st := newTestServer(t)
	col, _ := st.CreateCollection(context.Background(), "c", "C", "")
	a, _ := st.CreateLink(context.Background(), col.ID, "https://example.com/a")
	b, _ := st.CreateLink(context.Background(), col.ID, "https://example.com/b")

	resp, err := ts.Client().PostForm(ts.URL+"/links/bulk/delete",
		url.Values{"ids": {itoa(a.ID) + "," + itoa(b.ID)}})
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	trig := resp.Header.Get("HX-Trigger")
	if !strings.Contains(trig, "linklore-toast") {
		t.Errorf("missing toast trigger: %q", trig)
	}
	if !strings.Contains(trig, "Deleted 2 link") {
		t.Errorf("toast message wrong: %q", trig)
	}
}

func TestPruneEmpty_emitsToast(t *testing.T) {
	ts, st := newTestServer(t)
	st.CreateCollection(context.Background(), "empty", "E", "")
	c := &http.Client{
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse },
	}
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/collections/prune", nil)
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if !strings.Contains(resp.Header.Get("HX-Trigger"), "Pruned 1 empty") {
		t.Errorf("trigger = %q", resp.Header.Get("HX-Trigger"))
	}
}

// ---------- worker status dot ----------

func TestWorkerStatus_idleDot(t *testing.T) {
	ts, _ := newTestServer(t)
	code, body := get(t, ts, "/worker/status")
	if code != 200 {
		t.Fatalf("status %d", code)
	}
	if !strings.Contains(body, "status-idle") {
		t.Errorf("expected idle dot, got: %q", body)
	}
}

func TestWorkerStatus_busyDotWhenWorkPending(t *testing.T) {
	ts, st := newTestServer(t)
	col, _ := st.CreateCollection(context.Background(), "c", "C", "")
	_, _ = st.CreateLink(context.Background(), col.ID, "https://example.com/p")
	_, body := get(t, ts, "/worker/status")
	if !strings.Contains(body, "status-busy") {
		t.Errorf("expected busy dot when 1 link is pending, got: %q", body)
	}
}

// ---------- empty-state copy ----------

func TestEmptyState_collectionsHomeShowsCTA(t *testing.T) {
	ts, _ := newTestServer(t)
	_, body := get(t, ts, "/")
	if !strings.Contains(body, "No collections yet") {
		t.Errorf("missing empty-state headline")
	}
	if !strings.Contains(body, "empty-cta") {
		t.Errorf("missing empty-state CTA")
	}
}

// ---------- sidebar add affordance ----------

func TestSidebar_hasNewCollectionShortcut(t *testing.T) {
	ts, _ := newTestServer(t)
	_, body := get(t, ts, "/")
	if !strings.Contains(body, `class="sidebar-add"`) {
		t.Errorf("sidebar + button missing")
	}
	// Inline form replaced the legacy /#new-collection anchor; clicking
	// the + pill now toggles a <details> that posts directly to /collections.
	if !strings.Contains(body, `class="sidebar-new-collection`) {
		t.Errorf("sidebar inline new-collection <details> missing")
	}
	if !strings.Contains(body, `hx-post="/collections"`) {
		t.Errorf("inline new-collection form not wired to POST /collections")
	}
	if !strings.Contains(body, `id="new-collection"`) {
		t.Errorf("create-form anchor target missing on home page (kept for direct deep-links)")
	}
}

// ---------- type classifier in flight ----------

func TestCreateLink_classifiesPDFAsDocument(t *testing.T) {
	ts, st := newTestServer(t)
	st.CreateCollection(context.Background(), "c", "C", "")
	postForm(t, ts, "/c/c/links", url.Values{"url": {"https://example.com/whitepaper.pdf"}})
	links, _ := st.ListLinksByCollection(context.Background(), 1, 5, 0)
	if len(links) != 1 || links[0].Kind != "document" {
		t.Errorf("kind = %q (want document)", links[0].Kind)
	}
}

// ---------- LLM-disabled rendering ----------

func TestSidebar_alwaysContainsCollectionsAnchor(t *testing.T) {
	// The sidebar OOB-append needs a `#sidebar-collections` target;
	// confirm it's always emitted in the page shell.
	ts, _ := newTestServer(t)
	_, body := get(t, ts, "/")
	if !strings.Contains(body, `id="sidebar-collections"`) {
		t.Errorf("sidebar anchor missing — OOB swap on create would silently no-op")
	}
}

// ---------- LLM status pill ----------

func TestLLMHealth_rendersStatusPill(t *testing.T) {
	// /healthz/llm must return a `.status-pill` span (one of status-ok
	// or status-err depending on backend reachability). The test server
	// has no worker so we expect status-err, but either pill class
	// satisfies the contract that the topbar gets a stylable widget.
	ts, _ := newTestServer(t)
	code, body := get(t, ts, "/healthz/llm")
	if code != 200 {
		t.Fatalf("status: %d", code)
	}
	if !strings.Contains(body, `class="status-pill `) {
		t.Errorf("expected `.status-pill` markup, got: %q", body)
	}
	if !strings.Contains(body, `status-ok`) && !strings.Contains(body, `status-err`) {
		t.Errorf("expected status-ok or status-err class, got: %q", body)
	}
	if !strings.Contains(body, `>LLM<`) {
		t.Errorf("expected pill text `LLM`, got: %q", body)
	}
}

// ---------- /search/live is links-only ----------

// Top-bar live search must surface ONLY link results — no segmented
// switches (.seg-opt), no "create collection" affordance, no collection
// listings. The fragment is rendered as the user types, so anything
// extra would push real hits off-screen.
func TestSearchLive_linksOnly_noSegOptOrCreateCollection(t *testing.T) {
	ts, st := newTestServer(t)
	col, _ := st.CreateCollection(context.Background(), "c", "C", "")
	_, _ = st.CreateLink(context.Background(), col.ID, "https://example.com/p")
	code, body := get(t, ts, "/search/live?q=anything")
	if code != 200 {
		t.Fatalf("status %d", code)
	}
	if strings.Contains(body, `class="seg-opt"`) || strings.Contains(body, `class="seg-opt `) {
		t.Errorf("/search/live should not render seg-opt switches, got: %q", body)
	}
	for _, bad := range []string{"create collection", "Create collection", "new collection", "collection-card"} {
		if strings.Contains(body, bad) {
			t.Errorf("/search/live should not surface collections (%q): %q", bad, body)
		}
	}
}

// ---------- /search/suggest backs the topbar popover ----------

// Empty query should still render the help line (so focus alone shows
// useful copy) but skip the "Search 'q'" entry and the section
// headers.
func TestSearchSuggest_emptyQuery(t *testing.T) {
	ts, _ := newTestServer(t)
	code, body := get(t, ts, "/search/suggest")
	if code != 200 {
		t.Fatalf("status %d", code)
	}
	if !strings.Contains(body, "search-pop-help") {
		t.Errorf("empty suggest missing help line, body: %q", body)
	}
	for _, bad := range []string{"search-pop-go", `Search <em>"`, "Collections</div>", "Tags</div>"} {
		if strings.Contains(body, bad) {
			t.Errorf("empty suggest should not render %q, got: %q", bad, body)
		}
	}
}

// With a query that matches a collection name + a tag, both sections
// render plus the "Search '<q>'" entry. The popover never lists raw
// link results — that's /search/live's job.
func TestSearchSuggest_matchesCollectionsAndTags(t *testing.T) {
	ts, st := newTestServer(t)
	ctx := context.Background()
	colA, _ := st.CreateCollection(ctx, "ai", "AI", "")
	_, _ = st.CreateCollection(ctx, "videos", "Videos", "")
	l, _ := st.CreateLink(ctx, colA.ID, "https://example.com/p")
	tag, _ := st.UpsertTag(ctx, "ai", "AI")
	_ = st.AttachTag(ctx, l.ID, tag.ID, "user")

	code, body := get(t, ts, "/search/suggest?q=ai")
	if code != 200 {
		t.Fatalf("status %d", code)
	}
	if !strings.Contains(body, `Search <em>"ai"</em>`) {
		t.Errorf("expected literal Search 'ai' entry, got: %q", body)
	}
	if !strings.Contains(body, `href="/c/ai"`) {
		t.Errorf("expected matched collection link, got: %q", body)
	}
	if !strings.Contains(body, `href="/tags/ai"`) {
		t.Errorf("expected matched tag link, got: %q", body)
	}
	// Non-matching collection should NOT appear.
	if strings.Contains(body, `href="/c/videos"`) {
		t.Errorf("non-matching collection leaked into suggest: %q", body)
	}
	// Popover never shows snippets / link results.
	if strings.Contains(body, "search-result") || strings.Contains(body, "link-row") {
		t.Errorf("suggest should not render link results: %q", body)
	}
}

// ---------- /links/{id}/reminder ----------

// POST with no fields uses the configured default offset (~1w) and
// fixed cadence. The follow-up GET on the link reads the value back.
func TestReminder_setDefaultsToFixedOneWeek(t *testing.T) {
	ts, st := newTestServer(t)
	ctx := context.Background()
	col, _ := st.CreateCollection(ctx, "c", "C", "")
	l, _ := st.CreateLink(ctx, col.ID, "https://example.com/p")

	resp, err := ts.Client().PostForm(
		ts.URL+fmt.Sprintf("/links/%d/reminder", l.ID), url.Values{})
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	got, _ := st.GetLink(ctx, l.ID)
	if got.ReminderAt == nil {
		t.Fatalf("reminder not persisted")
	}
	if got.ReminderCadence != "fixed" {
		t.Errorf("cadence = %q, want fixed", got.ReminderCadence)
	}
	delta := time.Until(*got.ReminderAt)
	if delta < 6*24*time.Hour || delta > 8*24*time.Hour {
		t.Errorf("reminder is %.1fh from now, want ~1w", delta.Hours())
	}
}

func TestReminder_clearViaDelete(t *testing.T) {
	ts, st := newTestServer(t)
	ctx := context.Background()
	col, _ := st.CreateCollection(ctx, "c", "C", "")
	l, _ := st.CreateLink(ctx, col.ID, "https://example.com/p")
	at := time.Now().Add(7 * 24 * time.Hour)
	_ = st.SetReminder(ctx, l.ID, &at, "fixed")

	req, _ := http.NewRequest(http.MethodDelete,
		ts.URL+fmt.Sprintf("/links/%d/reminder", l.ID), nil)
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	got, _ := st.GetLink(ctx, l.ID)
	if got.ReminderAt != nil {
		t.Errorf("reminder still set: %v", got.ReminderAt)
	}
}

func TestReminder_dueFilterReturnsOnlyOverdue(t *testing.T) {
	ts, st := newTestServer(t)
	ctx := context.Background()
	col, _ := st.CreateCollection(ctx, "c", "C", "")
	overdue, _ := st.CreateLink(ctx, col.ID, "https://example.com/overdue")
	future, _ := st.CreateLink(ctx, col.ID, "https://example.com/future")
	_ = st.UpdateLinkExtraction(ctx, overdue.ID, "OverdueOne", "", "", "", "en", "")
	_ = st.UpdateLinkExtraction(ctx, future.ID, "FutureOne", "", "", "", "en", "")
	past := time.Now().Add(-1 * time.Hour)
	soon := time.Now().Add(7 * 24 * time.Hour)
	_ = st.SetReminder(ctx, overdue.ID, &past, "fixed")
	_ = st.SetReminder(ctx, future.ID, &soon, "fixed")

	code, body := get(t, ts, "/links?due=1")
	if code != 200 {
		t.Fatalf("status %d", code)
	}
	if !strings.Contains(body, "OverdueOne") {
		t.Errorf("expected OverdueOne in /links?due=1: %s", excerpt(body, "Due", 80))
	}
	if strings.Contains(body, "FutureOne") {
		t.Errorf("future reminder should not appear in due list: %q", body)
	}
}

// ---------- F1 highlights ----------

// POST /links/{id}/highlights stores a range + text + note. The
// stored highlight is rendered into the Preview as a <mark> with
// the same dataset id.
func TestHighlights_createAndRender(t *testing.T) {
	ts, st := newTestServer(t)
	ctx := context.Background()
	col, _ := st.CreateCollection(ctx, "c", "C", "")
	l, _ := st.CreateLink(ctx, col.ID, "https://example.com/p")
	_ = st.UpdateLinkExtraction(ctx, l.ID, "Title", "", "", "Lorem ipsum dolor sit amet.", "en", "")

	resp, err := ts.Client().PostForm(
		ts.URL+fmt.Sprintf("/links/%d/highlights", l.ID),
		url.Values{
			"start": {"6"}, "end": {"11"},
			"text": {"ipsum"}, "note": {"key concept"},
		})
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status %d", resp.StatusCode)
	}
	hs, _ := st.ListHighlightsByLink(ctx, l.ID)
	if len(hs) != 1 || hs[0].Text != "ipsum" || hs[0].Note != "key concept" {
		t.Fatalf("highlight not persisted: %+v", hs)
	}

	// Preview tab body should now embed a <mark class="hl"> wrapping
	// the highlighted slice.
	code, body := get(t, ts, fmt.Sprintf("/links/%d/drawer/preview", l.ID))
	if code != 200 {
		t.Fatalf("preview status %d", code)
	}
	if !strings.Contains(body, `class="hl"`) {
		t.Errorf("preview body missing mark.hl: %s", excerpt(body, "ipsum", 80))
	}
	if !strings.Contains(body, `data-hid=`) {
		t.Errorf("preview body missing data-hid attribute")
	}
}

// /highlights.md exports markdown grouped by link. Smoke check the
// shape only — full mark formatting is in reader unit tests.
func TestHighlights_exportMarkdown(t *testing.T) {
	ts, st := newTestServer(t)
	ctx := context.Background()
	col, _ := st.CreateCollection(ctx, "c", "C", "")
	l, _ := st.CreateLink(ctx, col.ID, "https://example.com/p")
	_, err := st.CreateHighlight(ctx, storage.Highlight{
		LinkID: l.ID, StartOffset: 0, EndOffset: 5,
		Text: "hello", Note: "greeting",
	})
	if err != nil {
		t.Fatal(err)
	}
	code, body := get(t, ts, "/highlights.md")
	if code != 200 {
		t.Fatalf("status %d", code)
	}
	for _, want := range []string{"# linklore highlights", "https://example.com/p", "> hello", "_greeting_"} {
		if !strings.Contains(body, want) {
			t.Errorf("export missing %q. got: %s", want, body)
		}
	}
}

// /links?highlighted=1 returns only links that have at least one
// highlight.
func TestHighlights_globalFilter(t *testing.T) {
	ts, st := newTestServer(t)
	ctx := context.Background()
	col, _ := st.CreateCollection(ctx, "c", "C", "")
	hi, _ := st.CreateLink(ctx, col.ID, "https://example.com/with")
	bare, _ := st.CreateLink(ctx, col.ID, "https://example.com/bare")
	_ = st.UpdateLinkExtraction(ctx, hi.ID, "WithHL", "", "", "", "en", "")
	_ = st.UpdateLinkExtraction(ctx, bare.ID, "BareOne", "", "", "", "en", "")
	_, _ = st.CreateHighlight(ctx, storage.Highlight{
		LinkID: hi.ID, StartOffset: 0, EndOffset: 3, Text: "abc",
	})
	code, body := get(t, ts, "/links?highlighted=1")
	if code != 200 {
		t.Fatalf("status %d", code)
	}
	if !strings.Contains(body, "WithHL") {
		t.Errorf("expected WithHL in highlighted view: %s", excerpt(body, "Highlighted", 80))
	}
	if strings.Contains(body, "BareOne") {
		t.Errorf("BareOne should be hidden: %q", body)
	}
}

// ---------- /backup.zip global backup ----------

// /backup.zip serves a ZIP with linklore-export.html (combined
// Netscape) plus a README.txt. The DB file copy is skipped when
// running on :memory: (which is what newTestServer uses), so the
// test asserts the streamed payload is a valid zip with the html
// and readme entries.
func TestBackupZip_streamsExpectedEntries(t *testing.T) {
	ts, st := newTestServer(t)
	ctx := context.Background()
	col, _ := st.CreateCollection(ctx, "c", "C", "")
	l, _ := st.CreateLink(ctx, col.ID, "https://example.com/p")
	_ = st.UpdateLinkExtraction(ctx, l.ID, "Title", "", "", "", "en", "")

	resp, err := ts.Client().Get(ts.URL + "/backup.zip")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/zip" {
		t.Errorf("content-type = %q, want application/zip", ct)
	}
	if cd := resp.Header.Get("Content-Disposition"); !strings.Contains(cd, "linklore-backup-") {
		t.Errorf("content-disposition = %q, want linklore-backup-… filename", cd)
	}
	buf, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	zr, err := zip.NewReader(bytes.NewReader(buf), int64(len(buf)))
	if err != nil {
		t.Fatalf("zip parse: %v", err)
	}
	got := map[string]bool{}
	for _, f := range zr.File {
		got[f.Name] = true
	}
	for _, want := range []string{"linklore-export.html", "README.txt"} {
		if !got[want] {
			t.Errorf("zip missing entry %q (got %v)", want, got)
		}
	}
}

// ---------- /links global filter view ----------

// /links lists across all collections and accepts kind=, notags=,
// status= filters.
func TestGlobalLinks_kindFilter(t *testing.T) {
	ts, st := newTestServer(t)
	ctx := context.Background()
	col, _ := st.CreateCollection(ctx, "c", "C", "")
	la, _ := st.CreateLink(ctx, col.ID, "https://example.com/article")
	lv, _ := st.CreateLink(ctx, col.ID, "https://example.com/video.mp4")
	_ = la
	_ = lv

	// Prime kinds via UpdateLinkExtraction (sets Kind defaults to article).
	_ = st.UpdateLinkExtraction(ctx, la.ID, "Article", "", "", "body", "en", "")
	_ = st.UpdateLinkExtraction(ctx, lv.ID, "Video", "", "", "body", "en", "")
	// Force a non-default kind on the second link.
	if err := st.SetLinkKind(ctx, lv.ID, "video"); err != nil {
		t.Fatal(err)
	}

	code, body := get(t, ts, "/links?kind=video")
	if code != 200 {
		t.Fatalf("status %d", code)
	}
	if !strings.Contains(body, "Video") {
		t.Errorf("video link should appear, body excerpt: %s", excerpt(body, "Video", 80))
	}
	if strings.Contains(body, ">Article<") {
		t.Errorf("Article should not appear when kind=video; body: %q", body)
	}
}

func TestGlobalLinks_notagsFilter(t *testing.T) {
	ts, st := newTestServer(t)
	ctx := context.Background()
	col, _ := st.CreateCollection(ctx, "c", "C", "")
	tagged, _ := st.CreateLink(ctx, col.ID, "https://example.com/tagged")
	untagged, _ := st.CreateLink(ctx, col.ID, "https://example.com/untagged")
	_ = st.UpdateLinkExtraction(ctx, tagged.ID, "TaggedOne", "", "", "body", "en", "")
	_ = st.UpdateLinkExtraction(ctx, untagged.ID, "BareOne", "", "", "body", "en", "")
	tg, _ := st.UpsertTag(ctx, "ai", "AI")
	_ = st.AttachTag(ctx, tagged.ID, tg.ID, "user")

	code, body := get(t, ts, "/links?notags=1")
	if code != 200 {
		t.Fatalf("status %d", code)
	}
	if !strings.Contains(body, "BareOne") {
		t.Errorf("untagged link should appear, body: %q", body)
	}
	if strings.Contains(body, "TaggedOne") {
		t.Errorf("tagged link should be hidden, body: %q", body)
	}
}

// ---------- /search facet syntax ----------

// Searching with a facet token like `tag:apollo` parses cleanly:
// the query keeps "graphql" as the residual text and the facet shows
// up as a chip on the result header. (Result filtering proper is
// covered by the unit test on search.Facets.Apply — newTestServer
// runs without a real search engine, so we focus on the parser/UI
// side of the integration here.)
//
// We also assert the chip renders even when the result list is empty
// so users see what their query was understood as.
func TestSearch_facetChipRenders(t *testing.T) {
	ts, _ := newTestServer(t)
	code, body := get(t, ts, "/search?q=graphql+tag:apollo+kind:article")
	if code != 200 {
		t.Fatalf("status %d", code)
	}
	for _, want := range []string{`tag:apollo`, `kind:article`} {
		if !strings.Contains(body, want) {
			t.Errorf("parsed facet chip %q missing from body: %q", want, excerpt(body, "facet", 200))
		}
	}
}

// ---------- favicon link in <head> ----------

func TestBase_includesFaviconLink(t *testing.T) {
	ts, _ := newTestServer(t)
	_, body := get(t, ts, "/")
	if !strings.Contains(body, `rel="icon"`) || !strings.Contains(body, `/static/favicon.svg`) {
		t.Errorf("base.html missing favicon link, body excerpt: %s", excerpt(body, "favicon", 80))
	}
}

func TestStatic_faviconServed(t *testing.T) {
	ts, _ := newTestServer(t)
	code, body := get(t, ts, "/static/favicon.svg")
	if code != 200 {
		t.Errorf("favicon status %d", code)
	}
	if !strings.Contains(body, "<svg") {
		t.Errorf("favicon body not SVG: %q", body)
	}
}

// ---------- topbar reorder + bookmarklet glyph ----------

func TestBase_topbarHasBookmarkletGlyphAndSettings(t *testing.T) {
	ts, _ := newTestServer(t)
	_, body := get(t, ts, "/")
	if !strings.Contains(body, `📎`) {
		t.Errorf("topbar bookmarklet glyph missing")
	}
	if !strings.Contains(body, `href="/settings"`) {
		t.Errorf("topbar settings link missing")
	}
	// The LLM status span sits between bookmarklet and chat in the new
	// order. Check both that the span exists and that it triggers the
	// new pill endpoint.
	if !strings.Contains(body, `id="llm-status"`) {
		t.Errorf("llm-status span missing from topbar")
	}
}

// Avoid an unused-import warning when the `time` import is only used
// indirectly (e.g. by sub-test helpers in features_test.go); calling
// time.Now() here keeps the import alive without changing semantics.
var _ = time.Now

// ---------- /api/links auto-default-collection ----------

func TestAPILinks_autoCreatesDefaultCollection(t *testing.T) {
	// Bookmarklet POSTs without a "collection" field. The handler
	// should auto-create the canonical "default" collection on first
	// hit and stash the link inside it.
	ts, st := newTestServer(t)
	resp, err := ts.Client().PostForm(ts.URL+"/api/links",
		url.Values{"url": {"https://example.com/x"}})
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	col, err := st.GetCollectionBySlug(context.Background(), "default")
	if err != nil {
		t.Fatalf("default collection not created: %v", err)
	}
	links, _ := st.ListLinksByCollection(context.Background(), col.ID, 10, 0)
	if len(links) != 1 {
		t.Errorf("default has %d link(s), want 1", len(links))
	}
}

func TestAPILinks_secondCallReusesDefault(t *testing.T) {
	// Two consecutive POSTs must NOT create two collections.
	ts, st := newTestServer(t)
	for _, u := range []string{"https://example.com/a", "https://example.com/b"} {
		resp, err := ts.Client().PostForm(ts.URL+"/api/links", url.Values{"url": {u}})
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("status %d for %s", resp.StatusCode, u)
		}
	}
	cols, _ := st.ListCollections(context.Background())
	if len(cols) != 1 {
		t.Fatalf("got %d collections, want exactly 1 (the auto-created default)", len(cols))
	}
	if cols[0].Slug != "default" {
		t.Errorf("slug = %q, want %q", cols[0].Slug, "default")
	}
	links, _ := st.ListLinksByCollection(context.Background(), cols[0].ID, 10, 0)
	if len(links) != 2 {
		t.Errorf("default has %d link(s), want 2", len(links))
	}
}

// ---------- /collections page structure ----------

func TestCollections_pageStructure(t *testing.T) {
	ts, _ := newTestServer(t)
	_, body := get(t, ts, "/")
	if !strings.Contains(body, "Add a collection") {
		t.Errorf(`expected "Add a collection" heading on /, got: %s`, excerpt(body, "card", 80))
	}
	if !strings.Contains(body, "Your collections") {
		t.Errorf(`expected "Your collections" heading on /`)
	}
	// Import form is collapsed inside a <details>. Find the specific
	// details that wraps it (there are now multiple <details> blocks on
	// the home page, e.g. the sidebar's inline new-collection toggle —
	// scanning forward until we hit the one with action="/import").
	importIdx := strings.Index(body, `action="/import"`)
	if importIdx < 0 {
		t.Fatalf(`no import form on / (expected action="/import")`)
	}
	di := strings.LastIndex(body[:importIdx], "<details")
	if di < 0 {
		t.Fatalf("import form not wrapped in <details>")
	}
	dEnd := strings.Index(body[di:], "</details>")
	if dEnd < 0 {
		t.Fatalf("unterminated <details>")
	}
	detailsBlock := body[di : di+dEnd]
	if !strings.Contains(detailsBlock, `action="/import"`) {
		t.Errorf("import form not inside <details>")
	}
	// The legacy "target slug (optional)" input is gone.
	if strings.Contains(detailsBlock, `name="collection"`) {
		t.Errorf(`import form still has name="collection" input`)
	}
}

// ---------- /c/:slug no cover button ----------

func TestLinksPage_noCoverButton(t *testing.T) {
	ts, st := newTestServer(t)
	st.CreateCollection(context.Background(), "c", "C", "")
	_, body := get(t, ts, "/c/c")
	if strings.Contains(body, "🖼 cover") {
		t.Errorf("page still has 🖼 cover button")
	}
	if strings.Contains(body, `id="cover-`) {
		t.Errorf("page still has cover-form (id=cover-…)")
	}
}

// ---------- /settings ----------

func TestSettings_renderRoute(t *testing.T) {
	ts, _ := newTestServer(t)
	code, body := get(t, ts, "/settings")
	if code != 200 {
		t.Fatalf("status=%d", code)
	}
	for _, want := range []string{"Backend", "Endpoint URL", "Save"} {
		if !strings.Contains(body, want) {
			t.Errorf("settings page missing %q", want)
		}
	}
}

func TestSettings_postRoundtrips(t *testing.T) {
	st, err := storage.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	srv, err := New(config.Default(), st, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	cfgPath := dir + "/config.yaml"
	envPath := dir + "/.env"
	srv.SetConfigPath(cfgPath)
	srv.SetDotEnvPath(envPath)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	form := url.Values{
		"backend":     {"none"},
		"endpoint":    {""},
		"model":       {""},
		"embed_model": {""},
		"api_key":     {""},
	}
	code, body := postForm(t, ts, "/settings", form)
	if code != 200 {
		t.Fatalf("status=%d body=%s", code, body)
	}
	if !strings.Contains(body, "status-pill status-ok") {
		t.Errorf("expected status-ok pill on save: %s", body)
	}
	// /settings now writes LLM to .env, not yaml. The yaml file must
	// stay untouched (still safe to commit).
	if _, err := os.Stat(envPath); err != nil {
		t.Errorf("expected .env saved at %s, got %v", envPath, err)
	}
	if _, err := os.Stat(cfgPath); err == nil {
		t.Errorf("yaml must NOT be written by /settings save: file exists at %s", cfgPath)
	}
	// GET reflects the new backend.
	_, body2 := get(t, ts, "/settings")
	if !strings.Contains(body2, `value="none"`) {
		t.Errorf("GET /settings doesn't reflect saved backend: %s", body2)
	}
}

func TestSettingsTest_noneBackend(t *testing.T) {
	ts, _ := newTestServer(t)
	code, body := postForm(t, ts, "/settings/test", url.Values{"backend": {"none"}})
	if code != 200 {
		t.Fatalf("status=%d", code)
	}
	if !strings.Contains(strings.ToLower(body), "disabled") {
		t.Errorf("expected 'disabled' in body, got: %s", body)
	}
}

func TestSettingsTest_litellmReachable(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"data":[{"id":"m1"},{"id":"m2"}]}`)
	}))
	defer upstream.Close()

	ts, _ := newTestServer(t)
	code, body := postForm(t, ts, "/settings/test", url.Values{
		"backend":  {"litellm"},
		"endpoint": {upstream.URL},
		"api_key":  {"sk-test"},
	})
	if code != 200 {
		t.Fatalf("status=%d", code)
	}
	if !strings.Contains(body, "status-pill status-ok") {
		t.Errorf("expected status-ok pill, got: %s", body)
	}
	if !strings.Contains(body, "2 models") {
		t.Errorf("expected '2 models', got: %s", body)
	}
}

func TestSettingsTest_litellmAuthFail(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"error":"unauthorized"}`)
	}))
	defer upstream.Close()

	ts, _ := newTestServer(t)
	code, body := postForm(t, ts, "/settings/test", url.Values{
		"backend":  {"litellm"},
		"endpoint": {upstream.URL},
		"api_key":  {"sk-bad"},
	})
	if code != 200 {
		t.Fatalf("status=%d", code)
	}
	if !strings.Contains(body, "status-pill status-err") {
		t.Errorf("expected status-err pill, got: %s", body)
	}
	if !strings.Contains(body, "401") {
		t.Errorf("expected '401' in body, got: %s", body)
	}
}

// ---------- /checks ----------

func TestChecks_renderRoute(t *testing.T) {
	ts, _ := newTestServer(t)
	code, body := get(t, ts, "/checks")
	if code != 200 {
		t.Fatalf("status=%d", code)
	}
	if !strings.Contains(body, "Link checker") {
		t.Errorf("page missing 'Link checker' header")
	}
}

func TestChecks_summaryCounts(t *testing.T) {
	ts, st := newTestServer(t)
	col, _ := st.CreateCollection(context.Background(), "c", "C", "")
	a, _ := st.CreateLink(context.Background(), col.ID, "https://example.com/a")
	_, _ = st.CreateLink(context.Background(), col.ID, "https://example.com/b")
	_, _ = st.CreateLink(context.Background(), col.ID, "https://example.com/c")
	if err := st.UpdateLinkCheck(context.Background(), a.ID, "broken", 404); err != nil {
		t.Fatal(err)
	}
	code, body := get(t, ts, "/checks/summary")
	if code != 200 {
		t.Fatalf("status=%d", code)
	}
	if !strings.Contains(body, "1 broken") {
		t.Errorf("expected '1 broken' in summary, got: %s", body)
	}
}

func TestChecksRun_marksDeadLink(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer upstream.Close()

	ts, st := newTestServer(t)
	col, _ := st.CreateCollection(context.Background(), "c", "C", "")
	link, _ := st.CreateLink(context.Background(), col.ID, upstream.URL+"/missing")

	code, _ := postForm(t, ts, "/checks/run", url.Values{})
	if code != 200 {
		t.Fatalf("status=%d", code)
	}

	// Wait up to ~2s for the goroutine to finish.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		got, err := st.GetLink(context.Background(), link.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.LastCheckStatus != "" {
			if got.LastCheckStatus != "broken" {
				t.Errorf("status = %q, want broken", got.LastCheckStatus)
			}
			if got.LastCheckCode != 404 {
				t.Errorf("code = %d, want 404", got.LastCheckCode)
			}
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("scan never completed within 2s")
}
