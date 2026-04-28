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

	"github.com/gabrielemastrapasqua/linklore/internal/chat"
	"github.com/gabrielemastrapasqua/linklore/internal/config"
	"github.com/gabrielemastrapasqua/linklore/internal/feed"
	"github.com/gabrielemastrapasqua/linklore/internal/reader"
	"github.com/gabrielemastrapasqua/linklore/internal/search"
	"github.com/gabrielemastrapasqua/linklore/internal/storage"
	"github.com/gabrielemastrapasqua/linklore/internal/tags"
	"github.com/gabrielemastrapasqua/linklore/internal/worker"
	"github.com/gabrielemastrapasqua/linklore/web"
)

type Server struct {
	cfg     config.Config
	store   *storage.Store
	r       *renderer
	search  *search.Engine // nil → search routes return empty results
	chat    *chat.Service  // nil → chat routes return 503
	feed    *feed.Builder
	worker  *worker.Worker // optional, for refetch/reindex
	tagsCfg tagsCfg
}

// tagsCfg is a tiny local view onto config.Tags so we don't carry the whole
// config struct into hot handlers.
type tagsCfg struct {
	MaxPerLink, ActiveCap, ReuseDistance int
}

func New(cfg config.Config, store *storage.Store, eng *search.Engine, chatSvc *chat.Service, w *worker.Worker) (*Server, error) {
	r, err := newRenderer()
	if err != nil {
		return nil, err
	}
	return &Server{
		cfg: cfg, store: store, r: r,
		search:  eng,
		chat:    chatSvc,
		feed:    feed.New(store),
		worker:  w,
		tagsCfg: tagsCfg{MaxPerLink: cfg.Tags.MaxPerLink, ActiveCap: cfg.Tags.ActiveCap, ReuseDistance: cfg.Tags.ReuseDistance},
	}, nil
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
	mux.HandleFunc("GET /c/{slug}/feed.xml", s.handleFeed)
	mux.HandleFunc("DELETE /links/{id}", s.handleDeleteLink)
	mux.HandleFunc("GET /links/{id}", s.handleLinkDetail)
	mux.HandleFunc("GET /links/{id}/row", s.handleLinkRow)
	mux.HandleFunc("GET /links/{id}/header", s.handleLinkHeader)
	mux.HandleFunc("GET /links/{id}/read", s.handleReaderMode)
	mux.HandleFunc("POST /links/{id}/refetch", s.handleRefetch)
	mux.HandleFunc("POST /links/{id}/reindex", s.handleReindex)
	mux.HandleFunc("POST /links/{id}/tags", s.handleAddUserTag)
	mux.HandleFunc("DELETE /links/{id}/tags/{slug}", s.handleRemoveTag)

	mux.HandleFunc("GET /search", s.handleSearchPage)
	mux.HandleFunc("GET /search/live", s.handleSearchLive)

	mux.HandleFunc("GET /worker/status", s.handleWorkerStatus)

	mux.HandleFunc("GET /chat", s.handleChatPage)
	mux.HandleFunc("POST /chat/stream", s.handleChatStream)

	mux.HandleFunc("GET /tags", s.handleTagsPage)
	mux.HandleFunc("GET /tags/{slug}", s.handleTagDetail)
	mux.HandleFunc("POST /tags/merge", s.handleMergeTags)

	mux.HandleFunc("GET /bookmarklet", s.handleBookmarkletPage)
	mux.HandleFunc("POST /api/links", s.handleAPILinks)

	mux.HandleFunc("GET /inbox", s.handlePlaceholder("Inbox", "Inbox is intentionally not implemented for now."))

	return logging(mux)
}

// ---- handlers ----

func (s *Server) handleHome(w http.ResponseWriter, r *http.Request) {
	cols, err := s.store.ListCollectionsWithStats(r.Context())
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
	// Wrap in a stat with zero counts so the card template renders correctly.
	s.renderFragment(w, "collection_card", storage.CollectionStat{Collection: *col})
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
	stats, _ := s.store.CollectionStatsByID(r.Context(), col.ID)
	s.renderPage(w, "links", map[string]any{
		"Title":      col.Name,
		"Collection": col,
		"Links":      links,
		"Stats":      stats,
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
	col, _ := s.store.GetCollectionBySlugByID(r.Context(), link.CollectionID)
	linkTags, _ := s.store.ListTagsByLink(r.Context(), id)
	s.renderPage(w, "link_detail", map[string]any{
		"Title":      "Link",
		"Link":       link,
		"Collection": col,
		"Tags":       linkTags,
	})
}

// handleLinkRow returns a single link_row fragment. Used by the HTMX
// auto-refresh on rows whose status is still pending/fetched, so the user
// sees the badge flip to "summarized" without a manual refresh.
func (s *Server) handleLinkRow(w http.ResponseWriter, r *http.Request) {
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
	s.renderFragment(w, "link_row", link)
}

func (s *Server) handleReaderMode(w http.ResponseWriter, r *http.Request) {
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
	s.renderPage(w, "reader", map[string]any{
		"Title":   firstNonEmpty(link.Title, link.URL),
		"Link":    link,
		"Article": reader.Render(link.ContentMD),
	})
}

func (s *Server) handleRefetch(w http.ResponseWriter, r *http.Request) {
	s.runReprocess(w, r, "refetch", s.store.MarkLinkPending)
}

func (s *Server) handleReindex(w http.ResponseWriter, r *http.Request) {
	s.runReprocess(w, r, "reindex", s.store.MarkLinkFetched)
}

// runReprocess shares the body of refetch and reindex: flip status, kick
// off ProcessOne in the background, and return the updated link_header
// fragment with an "↻ <action> queued" badge so the user sees that the
// click landed. The header polls itself until the worker reaches a
// terminal state.
func (s *Server) runReprocess(w http.ResponseWriter, r *http.Request, action string,
	flip func(context.Context, int64) error) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	if s.worker == nil {
		http.Error(w, "worker disabled", http.StatusServiceUnavailable)
		return
	}
	if err := flip(r.Context(), id); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	go func() {
		_ = s.worker.ProcessOne(context.Background(), id)
	}()
	link, err := s.store.GetLink(r.Context(), id)
	if err != nil {
		s.notFound(w, err)
		return
	}
	s.renderFragment(w, "link_header", map[string]any{
		"Link":   link,
		"Action": action,
		"At":     time.Now().Format("15:04:05"),
	})
}

// handleLinkHeader is the polling endpoint the link_header fragment hits
// every 2s while the link's status is non-terminal.
func (s *Server) handleLinkHeader(w http.ResponseWriter, r *http.Request) {
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
	s.renderFragment(w, "link_header", map[string]any{"Link": link})
}

func (s *Server) handleAddUserTag(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	raw := strings.TrimSpace(r.PostForm.Get("tag"))
	slug := tags.Slugify(raw)
	if slug == "" {
		http.Error(w, "tag required", http.StatusBadRequest)
		return
	}
	tag, err := s.store.UpsertTag(r.Context(), slug, raw)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := s.store.AttachTag(r.Context(), id, tag.ID, storage.TagSourceUser); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	linkTags, _ := s.store.ListTagsByLink(r.Context(), id)
	s.renderFragment(w, "tag_chips", map[string]any{
		"LinkID": id,
		"Tags":   linkTags,
	})
}

func (s *Server) handleRemoveTag(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	tag, err := s.store.FindTagBySlug(r.Context(), r.PathValue("slug"))
	if err != nil {
		s.notFound(w, err)
		return
	}
	if err := s.store.DetachTag(r.Context(), id, tag.ID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	linkTags, _ := s.store.ListTagsByLink(r.Context(), id)
	s.renderFragment(w, "tag_chips", map[string]any{
		"LinkID": id,
		"Tags":   linkTags,
	})
}

func (s *Server) handleTagsPage(w http.ResponseWriter, r *http.Request) {
	counts, err := s.store.ListTagsWithCounts(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	active, _ := s.store.CountActiveTags(r.Context())
	s.renderPage(w, "tags", map[string]any{
		"Title":  "Tags",
		"Tags":   counts,
		"Active": active,
		"Cap":    s.tagsCfg.ActiveCap,
	})
}

func (s *Server) handleTagDetail(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	tag, err := s.store.FindTagBySlug(r.Context(), slug)
	if err != nil {
		s.notFound(w, err)
		return
	}
	links, err := s.store.ListLinksByTag(r.Context(), slug, 200)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.renderPage(w, "tag_detail", map[string]any{
		"Title": "#" + tag.Slug,
		"Tag":   tag,
		"Links": links,
	})
}

func (s *Server) handleMergeTags(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	src := strings.TrimSpace(r.PostForm.Get("src"))
	dst := strings.TrimSpace(r.PostForm.Get("dst"))
	if src == "" || dst == "" {
		http.Error(w, "src and dst slugs required", http.StatusBadRequest)
		return
	}
	srcT, err := s.store.FindTagBySlug(r.Context(), src)
	if err != nil {
		s.notFound(w, err)
		return
	}
	dstT, err := s.store.FindTagBySlug(r.Context(), dst)
	if err != nil {
		s.notFound(w, err)
		return
	}
	if err := s.store.MergeTag(r.Context(), srcT.ID, dstT.ID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/tags", http.StatusSeeOther)
}

func (s *Server) handleFeed(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	siteURL := "http://" + r.Host
	xml, err := s.feed.Atom(r.Context(), slug, siteURL, 50)
	if err != nil {
		s.notFound(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/atom+xml; charset=utf-8")
	_, _ = w.Write([]byte(xml))
}

func (s *Server) handleBookmarkletPage(w http.ResponseWriter, _ *http.Request) {
	s.renderPage(w, "bookmarklet", map[string]any{"Title": "Bookmarklet"})
}

// handleAPILinks accepts {url, collection?} as form-encoded or JSON. It is
// the endpoint the bookmarklet hits. Always defaults to the "default"
// collection (auto-creating it) when none is supplied.
func (s *Server) handleAPILinks(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	url := strings.TrimSpace(r.PostForm.Get("url"))
	if url == "" {
		http.Error(w, "url required", http.StatusBadRequest)
		return
	}
	slug := strings.TrimSpace(r.PostForm.Get("collection"))
	if slug == "" {
		slug = "default"
	}
	col, err := s.store.GetCollectionBySlug(r.Context(), slug)
	if errors.Is(err, storage.ErrNotFound) {
		col, err = s.store.CreateCollection(r.Context(), slug, slug, "")
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	link, err := s.store.CreateLink(r.Context(), col.ID, url)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	fmt.Fprintf(w, `{"id":%d,"url":%q,"collection":%q,"status":%q}`,
		link.ID, link.URL, col.Slug, link.Status)
}

func firstNonEmpty(xs ...string) string {
	for _, x := range xs {
		if x != "" {
			return x
		}
	}
	return ""
}

func (s *Server) handleWorkerStatus(w http.ResponseWriter, r *http.Request) {
	n, err := s.store.CountInProgress(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if n == 0 {
		_, _ = w.Write([]byte(`<span class="muted" style="font-size:.8rem">idle</span>`))
		return
	}
	fmt.Fprintf(w, `<span class="badge pending">processing %d</span>`, n)
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

func (s *Server) handleChatPage(w http.ResponseWriter, r *http.Request) {
	if s.chat == nil {
		http.Error(w, "chat unavailable: LLM backend not configured", http.StatusServiceUnavailable)
		return
	}
	counts, _ := s.store.LinkStatusCounts(r.Context())
	s.renderPage(w, "chat", map[string]any{
		"Title":      "Chat",
		"Ready":      counts.Ready,
		"InProgress": counts.InProgress,
		"Failed":     counts.Failed,
		"Backend":    s.cfg.LLM.Backend,
		"Model":      s.activeModelName(),
	})
}

// activeModelName returns the user-facing name of whichever LLM model the
// chat is currently calling. Useful in the UI so the user knows what's
// generating the answer (qwen36-chat vs qwen3.6:35b vs whatever).
func (s *Server) activeModelName() string {
	switch s.cfg.LLM.Backend {
	case "litellm":
		return s.cfg.LLM.LiteLLM.Model
	case "ollama":
		return s.cfg.LLM.Ollama.Model
	default:
		return s.cfg.LLM.Backend
	}
}

// handleChatStream POST {message, session_id?, collection_id?} — server-sent
// events (text/event-stream): one "token" event per LLM chunk, a "sources"
// event up front with the citation payload, a final "done" event.
func (s *Server) handleChatStream(w http.ResponseWriter, r *http.Request) {
	if s.chat == nil {
		http.Error(w, "chat unavailable", http.StatusServiceUnavailable)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	msg := strings.TrimSpace(r.PostForm.Get("message"))
	if msg == "" {
		http.Error(w, "message required", http.StatusBadRequest)
		return
	}
	sessionID, _ := strconv.ParseInt(r.PostForm.Get("session_id"), 10, 64)
	collectionID, _ := strconv.ParseInt(r.PostForm.Get("collection_id"), 10, 64)

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	// http.NewResponseController works through middleware wrappers (it uses
	// Unwrap()), unlike a plain `w.(http.Flusher)` type assertion.
	rc := http.NewResponseController(w)
	flush := func() { _ = rc.Flush() }

	turn, err := s.chat.Prepare(r.Context(), sessionID, collectionID, msg)
	if err != nil {
		fmt.Fprintf(w, "event: error\ndata: %s\n\n", err.Error())
		flush()
		return
	}

	// session id + sources preamble.
	fmt.Fprintf(w, "event: session\ndata: %d\n\n", turn.SessionID)
	for _, src := range turn.Sources {
		fmt.Fprintf(w, "event: source\ndata: %d|%s|%s\n\n",
			src.LinkID, sseSafe(src.Title), sseSafe(src.URL))
	}
	flush()

	// Emit a "stats" event roughly twice per second while streaming, so
	// the chat UI can show a live tokens/sec readout without flooding the
	// browser with one extra event per delta.
	var lastStats time.Time
	_, finalStats, streamErr := s.chat.Stream(r.Context(), turn.SessionID, turn.Prompt, chat.StreamCallbacks{
		OnChunk: func(text string, st chat.StreamStats) error {
			fmt.Fprintf(w, "event: token\ndata: %s\n\n", sseSafe(text))
			if time.Since(lastStats) > 500*time.Millisecond {
				fmt.Fprintf(w, "event: stats\ndata: %d|%.2f\n\n", st.Tokens, st.TPS())
				lastStats = time.Now()
			}
			flush()
			return nil
		},
	})
	if streamErr != nil {
		fmt.Fprintf(w, "event: error\ndata: %s\n\n", sseSafe(streamErr.Error()))
		flush()
		return
	}
	// Final stats first, then done — so the UI prints the final t/s.
	fmt.Fprintf(w, "event: stats\ndata: %d|%.2f\n\n", finalStats.Tokens, finalStats.TPS())
	fmt.Fprint(w, "event: done\ndata: \n\n")
	flush()
}

// sseSafe replaces newlines (which would terminate the SSE event) with \\n
// escape sequences. The client will undo them when re-assembling tokens.
func sseSafe(s string) string {
	return strings.NewReplacer("\r", "", "\n", "\\n").Replace(s)
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

// Unwrap lets http.NewResponseController reach the real ResponseWriter
// (and its Flusher / Hijacker / etc) through this middleware wrapper.
func (s *statusWriter) Unwrap() http.ResponseWriter { return s.ResponseWriter }
