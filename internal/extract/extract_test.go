package extract

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func loadFixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return string(b)
}

func TestExtract_article_richReadability(t *testing.T) {
	html := loadFixture(t, "article.html")
	a, err := Extract(html, "https://blog.example.com/post")
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if a.Title != "Why local-first matters" {
		t.Errorf("title = %q", a.Title)
	}
	if a.ImageURL != "https://example.com/cover.png" {
		t.Errorf("image = %q", a.ImageURL)
	}
	if a.Description == "" {
		t.Errorf("expected description from og:description")
	}
	if !strings.Contains(strings.ToLower(a.ContentMD), "local-first") {
		t.Errorf("body missing keyword: %q", a.ContentMD)
	}
	if strings.Contains(a.ContentMD, "tracking") {
		t.Errorf("script content leaked into MD: %q", a.ContentMD)
	}
	if strings.Contains(a.ContentMD, "© 2024 Some Blog") {
		t.Errorf("footer leaked into MD: %q", a.ContentMD)
	}
	if len(a.ContentMD) < MinReadableChars {
		t.Errorf("body too short: %d chars", len(a.ContentMD))
	}
}

func TestExtract_no_og_metaFallbacks(t *testing.T) {
	html := loadFixture(t, "no_og.html")
	a, err := Extract(html, "https://example.com/x")
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if a.Title != "Plain Page Title" {
		t.Errorf("title fallback failed: %q", a.Title)
	}
	if !strings.Contains(strings.ToLower(a.Description), "plain meta description") {
		t.Errorf("description fallback failed: %q", a.Description)
	}
	if a.ImageURL != "" {
		t.Errorf("expected empty image, got %q", a.ImageURL)
	}
}

func TestExtract_spa_fallsBackToBody(t *testing.T) {
	html := loadFixture(t, "spa_stub.html")
	a, err := Extract(html, "https://spa.example.com/")
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	// No real article body. We still expect title + image from OG tags
	// and an empty (or near-empty) ContentMD — never a panic.
	if a.Title != "SPA Demo App" {
		t.Errorf("og:title fallback failed: %q", a.Title)
	}
	if a.ImageURL != "https://cdn.example.com/spa.png" {
		t.Errorf("og:image fallback failed: %q", a.ImageURL)
	}
}

func TestExtract_faviconAndExtraImages(t *testing.T) {
	html := `<!doctype html><html><head>
<title>News</title>
<link rel="icon" href="/favicon-32.png">
<link rel="apple-touch-icon" href="/apple.png">
<meta property="og:image" content="https://cdn.example.com/cover.jpg">
<meta property="og:image" content="https://cdn.example.com/cover-2.jpg">
<meta name="twitter:image" content="https://cdn.example.com/tw.png">
<meta name="description" content="d">
</head><body>
<article>
<p>This article has more than enough body text to satisfy readability — at least a couple of paragraphs of substantive content so the extraction path runs through the full readability primary branch instead of the SPA fallback. Lorem ipsum dolor sit amet consectetur.</p>
<img src="/article/inline-1.jpg" width="600" height="400">
<img src="https://cdn.example.com/inline-2.jpg">
<img src="data:image/png;base64,xxx">
<img src="/tracking.gif" width="1" height="1">
</article>
</body></html>`

	a, err := Extract(html, "https://news.example.com/post/42")
	if err != nil {
		t.Fatal(err)
	}
	// Apple-touch-icon wins over <link rel="icon"> in our priority list.
	if a.FaviconURL != "https://news.example.com/apple.png" {
		t.Errorf("favicon = %q", a.FaviconURL)
	}
	// readability may pick either of the two og:image declarations as the
	// primary. Whichever wins, all four expected images must be visible
	// across primary + extras, the primary must not appear in extras, and
	// data:/tracker images must never show up.
	all := append([]string{a.ImageURL}, a.ExtraImages...)
	want := []string{
		"https://cdn.example.com/cover.jpg",
		"https://cdn.example.com/cover-2.jpg",
		"https://cdn.example.com/tw.png",
		"https://news.example.com/article/inline-1.jpg",
		"https://cdn.example.com/inline-2.jpg",
	}
	for _, w := range want {
		found := false
		for _, g := range all {
			if g == w {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("missing image %q in primary+extras: primary=%q extras=%v",
				w, a.ImageURL, a.ExtraImages)
		}
	}
	// Primary must not appear among extras.
	for _, g := range a.ExtraImages {
		if g == a.ImageURL {
			t.Errorf("primary leaked into extras: %v", a.ExtraImages)
		}
	}
	// data: URIs and 1×1 trackers must be filtered out.
	for _, g := range a.ExtraImages {
		if strings.HasPrefix(g, "data:") {
			t.Errorf("data: URI leaked: %v", a.ExtraImages)
		}
		if strings.Contains(g, "tracking.gif") {
			t.Errorf("tracker leaked: %v", a.ExtraImages)
		}
	}
}

func TestExtract_picksLazyLoadAndSrcsetImages(t *testing.T) {
	html := `<!doctype html><html><head>
<title>Lazy</title>
<meta property="og:image" content="https://cdn.example.com/cover.jpg">
</head><body>
<article>
<p>Body text long enough for readability — lorem ipsum dolor sit amet consectetur adipiscing elit sed do eiusmod tempor incididunt ut labore et dolore magna aliqua.</p>
<img src="data:image/gif;base64,xxx" data-src="https://cdn.example.com/lazy.jpg" alt="">
<picture>
  <source srcset="https://cdn.example.com/hero-2x.jpg 2400w, https://cdn.example.com/hero-1x.jpg 1200w">
  <img src="https://cdn.example.com/hero-fallback.jpg" alt="">
</picture>
<figure>
  <img src="/article/inline.jpg" srcset="/article/inline-720.jpg 720w, /article/inline-1440.jpg 1440w">
</figure>
</article>
</body></html>`

	a, err := Extract(html, "https://news.example.com/post")
	if err != nil {
		t.Fatal(err)
	}
	all := append([]string{a.ImageURL}, a.ExtraImages...)

	for _, want := range []string{
		"https://cdn.example.com/lazy.jpg",                 // data-src wins over data: src
		"https://cdn.example.com/hero-2x.jpg",              // <picture><source srcset> highest width
		"https://news.example.com/article/inline-1440.jpg", // <img srcset> resolved + highest width
	} {
		found := false
		for _, g := range all {
			if g == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("missing %q in primary+extras: %v", want, all)
		}
	}
	// data: placeholder must NOT show up.
	for _, g := range all {
		if strings.HasPrefix(g, "data:") {
			t.Errorf("data: placeholder leaked: %v", a.ExtraImages)
		}
	}
}

func TestExtract_filtersTrackerURLs(t *testing.T) {
	html := `<!doctype html><html><head><title>x</title>
<meta property="og:image" content="https://cdn.example.com/cover.jpg">
</head><body>
<article>
<p>Body text long enough for readability — lorem ipsum dolor sit amet consectetur adipiscing elit sed do eiusmod tempor.</p>
<img src="https://www.facebook.com/tr?id=123&ev=PageView">
<img src="https://www.google-analytics.com/collect?v=1">
<img src="https://cdn.example.com/legit.jpg">
</article>
</body></html>`
	a, _ := Extract(html, "https://news.example.com/post")
	for _, g := range a.ExtraImages {
		low := strings.ToLower(g)
		if strings.Contains(low, "facebook.com/tr") ||
			strings.Contains(low, "google-analytics.com") {
			t.Errorf("tracker URL leaked: %v", a.ExtraImages)
		}
	}
}

func TestPickFromSrcset(t *testing.T) {
	cases := map[string]string{
		"":                              "",
		"a.jpg":                         "a.jpg",
		"a.jpg 100w, b.jpg 300w":        "b.jpg",
		"b.jpg 800w, a.jpg 200w":        "b.jpg",
		"a.jpg 1x, b.jpg 2x":            "b.jpg", // no width descriptors → last-wins (matches browser)
		"a.jpg 120w, c.jpg, b.jpg 600w": "b.jpg",
	}
	for in, want := range cases {
		if got := pickFromSrcset(in); got != want {
			t.Errorf("pickFromSrcset(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestExtract_faviconFallbackToRoot(t *testing.T) {
	html := `<html><head><title>X</title></head><body>` +
		strings.Repeat("hello world ", 50) + `</body></html>`
	a, _ := Extract(html, "https://news.example.com/post")
	if a.FaviconURL != "https://news.example.com/favicon.ico" {
		t.Errorf("favicon fallback = %q", a.FaviconURL)
	}
}

func TestExtract_emptyHTML(t *testing.T) {
	if _, err := Extract("", ""); err == nil {
		t.Fatal("expected error on empty input")
	}
}

func TestExtract_garbageHTML_keepsTitleIfPresent(t *testing.T) {
	a, err := Extract(`<html><head><title>Tiny</title></head><body>x</body></html>`, "")
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if a.Title != "Tiny" {
		t.Errorf("title = %q", a.Title)
	}
}

func TestCollapseBlankLines(t *testing.T) {
	in := "a\n\n\n\nb\n\nc"
	got := collapseBlankLines(in)
	if got != "a\n\nb\n\nc" {
		t.Errorf("collapse = %q", got)
	}
}

func TestFetcher_HTTP(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") == "" {
			t.Errorf("missing UA header")
		}
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte("<html><title>OK</title></html>"))
	}))
	defer srv.Close()

	f := NewFetcher(2 * time.Second)
	body, err := f.Fetch(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if !strings.Contains(body, "<title>OK</title>") {
		t.Errorf("body = %q", body)
	}
}

func TestFetcher_non2xxFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "nope", http.StatusForbidden)
	}))
	defer srv.Close()

	f := NewFetcher(2 * time.Second)
	if _, err := f.Fetch(context.Background(), srv.URL); err == nil {
		t.Fatal("expected error on 403")
	}
}

func TestFetcher_capsBody(t *testing.T) {
	// Server returns 6 MiB; Fetch must cap at 5 MiB and not OOM.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		buf := make([]byte, 1<<20)
		for range 6 {
			_, _ = w.Write(buf)
		}
	}))
	defer srv.Close()

	f := NewFetcher(5 * time.Second)
	body, err := f.Fetch(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if got := len(body); got > (5<<20)+1 {
		t.Errorf("body not capped: %d bytes", got)
	}
}

// End-to-end: fixture served by httptest → Fetch → Extract.
func TestEndToEnd_HTTPFetchAndExtract(t *testing.T) {
	html := loadFixture(t, "article.html")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(html))
	}))
	defer srv.Close()

	f := NewFetcher(2 * time.Second)
	body, err := f.Fetch(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	a, err := Extract(body, srv.URL)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if a.Title != "Why local-first matters" {
		t.Errorf("title = %q", a.Title)
	}
}
