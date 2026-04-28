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
	srv, err := New(config.Default(), st, nil, chatSvc)
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

func TestChat_disabled503(t *testing.T) {
	ts, _ := newTestServer(t)
	code, _ := get(t, ts, "/chat")
	if code != 503 {
		t.Errorf("status = %d (expected 503 when chat is nil)", code)
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

	srv, err := New(config.Default(), st, eng, chatSvc)
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
