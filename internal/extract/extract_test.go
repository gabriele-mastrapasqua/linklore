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
