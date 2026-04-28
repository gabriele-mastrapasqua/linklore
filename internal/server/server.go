// Package server wires the HTTP routes for the HTMX UI. It is intentionally
// thin: handlers parse, call storage, and render templates. No business logic.
package server

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gabrielemastrapasqua/linklore/internal/config"
	"github.com/gabrielemastrapasqua/linklore/internal/search"
	"github.com/gabrielemastrapasqua/linklore/internal/storage"
	"github.com/gabrielemastrapasqua/linklore/web"
)

type Server struct {
	cfg    config.Config
	store  *storage.Store
	r      *renderer
	search *search.Engine // nil → search routes return empty results
}

func New(cfg config.Config, store *storage.Store, eng *search.Engine) (*Server, error) {
	r, err := newRenderer()
	if err != nil {
		return nil, err
	}
	return &Server{cfg: cfg, store: store, r: r, search: eng}, nil
}

// Handler returns the configured *http.ServeMux. Kept separate from ListenAndServe
// so tests can pass it directly to httptest.NewServer.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// static assets — served from the embedded FS so the binary stays portable
	staticFS, _ := fs.Sub(web.Static, "static")
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.FS(staticFS))))

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("ok"))
	})

	mux.HandleFunc("GET /{$}", s.handleHome)
	mux.HandleFunc("POST /collections", s.handleCreateCollection)
	mux.HandleFunc("GET /c/{slug}", s.handleListLinks)
	mux.HandleFunc("POST /c/{slug}/links", s.handleCreateLink)
	mux.HandleFunc("DELETE /links/{id}", s.handleDeleteLink)

	mux.HandleFunc("GET /search", s.handleSearchPage)
	mux.HandleFunc("GET /search/live", s.handleSearchLive)
	mux.HandleFunc("GET /inbox", s.handlePlaceholder("Inbox", "Coming in Phase 7."))
	mux.HandleFunc("GET /chat", s.handlePlaceholder("Chat", "Coming in Phase 6."))
	mux.HandleFunc("GET /tags", s.handlePlaceholder("Tags", "Coming in Phase 4/7."))
	mux.HandleFunc("GET /links/{id}", s.handleLinkDetail)

	return logging(mux)
}

// ---- handlers ----

func (s *Server) handleHome(w http.ResponseWriter, r *http.Request) {
	cols, err := s.store.ListCollections(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.renderPage(w, "collections", map[string]any{
		"Title":       "Collections",
		"Collections": cols,
	})
}

func (s *Server) handleCreateCollection(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	slug := strings.TrimSpace(r.PostForm.Get("slug"))
	name := strings.TrimSpace(r.PostForm.Get("name"))
	if slug == "" || name == "" {
		http.Error(w, "slug and name required", http.StatusBadRequest)
		return
	}
	col, err := s.store.CreateCollection(r.Context(), slug, name, "")
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.renderFragment(w, "collection_card", col)
}

func (s *Server) handleListLinks(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	col, err := s.store.GetCollectionBySlug(r.Context(), slug)
	if err != nil {
		s.notFound(w, err)
		return
	}
	links, err := s.store.ListLinksByCollection(r.Context(), col.ID, 200, 0)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.renderPage(w, "links", map[string]any{
		"Title":      col.Name,
		"Collection": col,
		"Links":      links,
	})
}

func (s *Server) handleCreateLink(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	slug := r.PathValue("slug")
	col, err := s.store.GetCollectionBySlug(r.Context(), slug)
	if err != nil {
		s.notFound(w, err)
		return
	}
	url := strings.TrimSpace(r.PostForm.Get("url"))
	if url == "" {
		http.Error(w, "url required", http.StatusBadRequest)
		return
	}
	link, err := s.store.CreateLink(r.Context(), col.ID, url)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.renderFragment(w, "link_row", link)
}

func (s *Server) handleDeleteLink(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	if err := s.store.DeleteLink(r.Context(), id); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// HTMX swaps outerHTML with empty body → row disappears.
	w.WriteHeader(http.StatusOK)
}

func (s *Server) handleLinkDetail(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	link, err := s.store.GetLink(r.Context(), id)
	if err != nil {
		s.notFound(w, err)
		return
	}
	// Phase-2: bare placeholder until we have a detail template.
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, `<a href="/c/">← back</a><h2>%s</h2><p><a href="%s" target="_blank">%s</a></p><pre>%s</pre>`,
		htmlEscape(link.Title), htmlEscape(link.URL), htmlEscape(link.URL), htmlEscape(link.Summary))
}

func (s *Server) handleSearchPage(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	results := s.runSearch(r.Context(), q, 0, 20)
	s.renderPage(w, "search", map[string]any{
		"Title":   "Search",
		"Query":   q,
		"Results": results,
	})
}

// handleSearchLive returns just the result fragment for HTMX live-search
// driven by the top-bar input.
func (s *Server) handleSearchLive(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	results := s.runSearch(r.Context(), q, 0, 8)
	s.renderFragment(w, "search_results", map[string]any{
		"Query":   q,
		"Results": results,
	})
}

// runSearch is a small adapter so the two search handlers share error logging.
func (s *Server) runSearch(ctx context.Context, q string, collectionID int64, limit int) []search.LinkResult {
	if s.search == nil || q == "" {
		return nil
	}
	res, err := s.search.SearchLinks(ctx, q, collectionID, limit)
	if err != nil {
		log.Printf("search %q: %v", q, err)
		return nil
	}
	return res
}

func (s *Server) handlePlaceholder(title, msg string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, `<h2>%s</h2><p class="muted">%s</p>`, htmlEscape(title), htmlEscape(msg))
	}
}

// ---- helpers ----

func (s *Server) renderPage(w http.ResponseWriter, name string, data any) {
	t, ok := s.r.pages[name]
	if !ok {
		http.Error(w, "no such page: "+name, http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := t.ExecuteTemplate(w, "base", data); err != nil {
		log.Printf("render %s: %v", name, err)
	}
}

func (s *Server) renderFragment(w http.ResponseWriter, name string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.r.partials.ExecuteTemplate(w, name, data); err != nil {
		log.Printf("render fragment %s: %v", name, err)
	}
}

func (s *Server) notFound(w http.ResponseWriter, err error) {
	if errors.Is(err, storage.ErrNotFound) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	http.Error(w, err.Error(), http.StatusInternalServerError)
}

func htmlEscape(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", `'`, "&#39;")
	return r.Replace(s)
}

// logging is a tiny request logger; nothing fancy, just method+path+status.
func logging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sw := &statusWriter{ResponseWriter: w, status: 200}
		t0 := time.Now()
		next.ServeHTTP(sw, r)
		log.Printf("%s %s %d %s", r.Method, r.URL.Path, sw.status, time.Since(t0))
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (s *statusWriter) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}
