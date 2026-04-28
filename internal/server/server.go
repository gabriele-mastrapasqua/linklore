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
	"github.com/gabrielemastrapasqua/linklore/internal/events"
	"github.com/gabrielemastrapasqua/linklore/internal/feed"
	"github.com/gabrielemastrapasqua/linklore/internal/feedimport"
	"github.com/gabrielemastrapasqua/linklore/internal/reader"
	"github.com/gabrielemastrapasqua/linklore/internal/search"
	"github.com/gabrielemastrapasqua/linklore/internal/storage"
	"github.com/gabrielemastrapasqua/linklore/internal/tags"
	"github.com/gabrielemastrapasqua/linklore/internal/worker"
	"github.com/gabrielemastrapasqua/linklore/web"
)

type Server struct {
	cfg        config.Config
	store      *storage.Store
	r          *renderer
	search     *search.Engine // nil → search routes return empty results
	chat       *chat.Service  // nil → chat routes return 503
	feed       *feed.Builder
	feedImport *feedimport.Importer
	worker     *worker.Worker // optional, for refetch/reindex
	events     *events.Broker // optional, for SSE push to clients
	tagsCfg    tagsCfg
}

// tagsCfg is a tiny local view onto config.Tags so we don't carry the whole
// config struct into hot handlers.
type tagsCfg struct {
	MaxPerLink, ActiveCap, ReuseDistance int
}

func New(cfg config.Config, store *storage.Store, eng *search.Engine, chatSvc *chat.Service, w *worker.Worker, broker *events.Broker) (*Server, error) {
	r, err := newRenderer()
	if err != nil {
		return nil, err
	}
	return &Server{
		cfg: cfg, store: store, r: r,
		search:     eng,
		chat:       chatSvc,
		feed:       feed.New(store),
		feedImport: feedimport.New(store),
		worker:     w,
		events:     broker,
		tagsCfg:    tagsCfg{MaxPerLink: cfg.Tags.MaxPerLink, ActiveCap: cfg.Tags.ActiveCap, ReuseDistance: cfg.Tags.ReuseDistance},
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
	mux.HandleFunc("POST /c/{slug}/rename", s.handleRenameCollection)
	mux.HandleFunc("GET /c/{slug}/feed.xml", s.handleFeed)
	mux.HandleFunc("GET /c/{slug}/stats", s.handleCollectionStats)
	mux.HandleFunc("POST /c/{slug}/feed", s.handleSetFeed)               // legacy alias
	mux.HandleFunc("POST /c/{slug}/feed/save", s.handleSaveFeed)         // unified entry point
	mux.HandleFunc("POST /c/{slug}/feed/discover", s.handleDiscoverFeed) // legacy alias
	mux.HandleFunc("POST /c/{slug}/feed/refresh", s.handleRefreshFeed)
	mux.HandleFunc("DELETE /links/{id}", s.handleDeleteLink)
	mux.HandleFunc("POST /links/{id}/move", s.handleMoveLink)
	mux.HandleFunc("POST /links/{id}/reorder", s.handleReorderLink)
	mux.HandleFunc("GET /links/{id}", s.handleLinkDetail)
	mux.HandleFunc("GET /links/{id}/row", s.handleLinkRow)
	mux.HandleFunc("GET /links/{id}/header", s.handleLinkHeader)
	mux.HandleFunc("GET /links/{id}/read", s.handleReaderMode)
	mux.HandleFunc("POST /links/{id}/refetch", s.handleRefetch)
	mux.HandleFunc("POST /links/{id}/reindex", s.handleReindex)
	mux.HandleFunc("POST /links/{id}/note", s.handleSaveNote)
	mux.HandleFunc("POST /links/{id}/tags", s.handleAddUserTag)
	mux.HandleFunc("DELETE /links/{id}/tags/{slug}", s.handleRemoveTag)

	mux.HandleFunc("GET /search", s.handleSearchPage)
	mux.HandleFunc("GET /search/live", s.handleSearchLive)

	mux.HandleFunc("GET /worker/status", s.handleWorkerStatus)
	mux.HandleFunc("GET /healthz/llm", s.handleLLMHealth)
	mux.HandleFunc("GET /events", s.handleEvents)
	mux.HandleFunc("POST /links/{id}/summarize", s.handleReindex) // alias for the LLM-onboarding flow
	mux.HandleFunc("POST /preferences/previews", s.handleTogglePreviews)
	mux.HandleFunc("POST /preferences/theme", s.handleSetTheme)

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
	s.renderPageRq(w, r, "collections", map[string]any{
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
	// Auto-refresh feed on entry when the collection is feed-backed and
	// hasn't been polled recently. We do it inline (not in a goroutine)
	// so the rendered list already reflects the new entries — without
	// blocking too long: the importer has its own 30s timeout.
	if s.feedImport != nil && col.FeedURL != "" {
		stale := col.LastCheckedAt == nil || time.Since(*col.LastCheckedAt) > 15*time.Minute
		if stale {
			if _, ferr := s.feedImport.RefreshOne(r.Context(), col.ID); ferr != nil {
				log.Printf("auto-refresh feed for %s: %v", col.Slug, ferr)
			}
			col, _ = s.store.GetCollectionBySlug(r.Context(), slug)
		}
	}
	links, err := s.store.ListLinksByCollection(r.Context(), col.ID, 200, 0)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	stats, _ := s.store.CollectionStatsByID(r.Context(), col.ID)
	s.renderPageRq(w, r, "links", map[string]any{
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
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	s.renderFragment(w, "link_row", link)
	s.writeCollectionStatsOOB(w, r.Context(), col)
	s.writeEmptyStateOOB(w, true) // hide "No links yet" since we just added one
}

// writeCollectionStatsOOB appends an out-of-band swap that re-renders
// the collection's stats card with up-to-date counters AND the matching
// sidebar entry (so its count badge reflects the change too). HTMX picks
// both up because of the hx-swap-oob attribute.
func (s *Server) writeCollectionStatsOOB(w http.ResponseWriter, ctx context.Context, col *storage.Collection) {
	stats, err := s.store.CollectionStatsByID(ctx, col.ID)
	if err != nil {
		return
	}
	fmt.Fprintf(w, "\n<div id=\"collection-stats-%d\" hx-swap-oob=\"outerHTML\">", col.ID)
	s.renderFragment(w, "collection_stats", map[string]any{
		"Collection": col,
		"Stats":      stats,
	})
	fmt.Fprint(w, "</div>")

	// Sidebar entry refresh: render the partial, then inject the
	// hx-swap-oob attribute so HTMX picks the new <a> up by id and
	// replaces the existing sidebar item in place.
	var sb strings.Builder
	if err := s.r.partials.ExecuteTemplate(&sb, "sidebar_collection_entry", map[string]any{
		"Cs":         stats,
		"ActiveSlug": "",
	}); err == nil {
		// Inject hx-swap-oob on the rendered <a> tag.
		out := strings.Replace(sb.String(), "<a ", `<a hx-swap-oob="outerHTML" `, 1)
		fmt.Fprint(w, "\n", out)
	}
}

// writeEmptyStateOOB shows or hides the "No links yet" placeholder.
func (s *Server) writeEmptyStateOOB(w http.ResponseWriter, hide bool) {
	if hide {
		fmt.Fprint(w, `<div id="links-empty" hx-swap-oob="outerHTML" hidden></div>`)
	} else {
		fmt.Fprint(w, `<div id="links-empty" hx-swap-oob="outerHTML"><div class="empty">No links yet.</div></div>`)
	}
}

func (s *Server) handleCollectionStats(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	col, err := s.store.GetCollectionBySlug(r.Context(), slug)
	if err != nil {
		s.notFound(w, err)
		return
	}
	stats, err := s.store.CollectionStatsByID(r.Context(), col.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.renderFragment(w, "collection_stats", map[string]any{
		"Collection": col,
		"Stats":      stats,
	})
}

func (s *Server) handleDeleteLink(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	// Look up the link first to know which collection to refresh stats for.
	link, err := s.store.GetLink(r.Context(), id)
	if err != nil {
		s.notFound(w, err)
		return
	}
	if err := s.store.DeleteLink(r.Context(), id); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	col, _ := s.store.GetCollectionBySlugByID(r.Context(), link.CollectionID)
	// HTMX swaps outerHTML with empty body → row disappears.
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if col != nil {
		s.writeCollectionStatsOOB(w, r.Context(), col)
		// If the collection is now empty, show the "No links yet" hint.
		stats, _ := s.store.CollectionStatsByID(r.Context(), col.ID)
		if stats.Total == 0 {
			s.writeEmptyStateOOB(w, false)
		}
	}
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
	llmHealthy, llmErr := s.llmHealthSnapshot()
	allCollections, _ := s.store.ListCollections(r.Context())
	s.renderPageRq(w, r, "link_detail", map[string]any{
		"Title":          "Link",
		"Link":           link,
		"Collection":     col,
		"Tags":           linkTags,
		"AllCollections": allCollections,
		// Banner data: only show "no summary yet, configure LLM" when
		// status=fetched (extraction OK, summary missing) AND the LLM is
		// either unhealthy or unconfigured.
		"NeedsSummary": link.Status == storage.StatusFetched,
		"LLMHealthy":   llmHealthy,
		"LLMError":     llmErr,
	})
}

// llmHealthSnapshot returns a (healthy, message) pair for the templates.
// When there's no worker / no backend configured, healthy=false with a
// clear "not configured" message instead of a stack trace.
func (s *Server) llmHealthSnapshot() (bool, string) {
	if s.worker == nil {
		return false, "no LLM backend configured — set llm.backend + LITELLM_API_KEY in your config"
	}
	healthy, err, _ := s.worker.LLMHealth()
	if healthy {
		return true, ""
	}
	if err == nil {
		return false, "LLM gateway not yet probed — try again in a few seconds"
	}
	return false, err.Error()
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
	s.renderPageRq(w, r, "reader", map[string]any{
		"Title":       firstNonEmpty(link.Title, link.URL),
		"Link":        link,
		"Article":     reader.Render(link.ContentMD),
		"HideSidebar": true,
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

// handleMoveLink reassigns a link to a different collection. Triggered
// either by the "Move to…" select on link_detail or by the future DnD
// flow on the row itself. Always responds with the OOB stats refresh
// for both the source and destination collections, and removes the row
// out-of-band when the request comes from the source collection page.
func (s *Server) handleMoveLink(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	dstID, err := strconv.ParseInt(strings.TrimSpace(r.PostForm.Get("collection_id")), 10, 64)
	if err != nil || dstID <= 0 {
		// Allow specifying by slug too.
		if slug := strings.TrimSpace(r.PostForm.Get("collection_slug")); slug != "" {
			col, sErr := s.store.GetCollectionBySlug(r.Context(), slug)
			if sErr != nil {
				s.notFound(w, sErr)
				return
			}
			dstID = col.ID
		} else {
			http.Error(w, "collection_id or collection_slug required", http.StatusBadRequest)
			return
		}
	}

	link, err := s.store.GetLink(r.Context(), id)
	if err != nil {
		s.notFound(w, err)
		return
	}
	srcID := link.CollectionID
	if srcID == dstID {
		w.WriteHeader(http.StatusOK)
		return
	}
	if err := s.store.MoveLink(r.Context(), id, dstID); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	src, _ := s.store.GetCollectionBySlugByID(r.Context(), srcID)
	dst, _ := s.store.GetCollectionBySlugByID(r.Context(), dstID)

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// Remove the row from the source page out-of-band; the destination
	// stats also update so a sidebar/page refresh isn't needed.
	fmt.Fprintf(w, `<div id="link-%d" hx-swap-oob="outerHTML"></div>`, id)
	if src != nil {
		s.writeCollectionStatsOOB(w, r.Context(), src)
		stats, _ := s.store.CollectionStatsByID(r.Context(), src.ID)
		if stats.Total == 0 {
			s.writeEmptyStateOOB(w, false)
		}
	}
	if dst != nil {
		s.writeCollectionStatsOOB(w, r.Context(), dst)
	}
}

// handleReorderLink reorders a link relative to a pivot, optionally
// across collections. Form params: pivot_id (required), position
// ("before"|"after", default "after"). Empty body responds 200 — the
// browser already updated optimistically; OOB stats refresh covers
// any cross-collection cases.
func (s *Server) handleReorderLink(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	pivotID, err := strconv.ParseInt(strings.TrimSpace(r.PostForm.Get("pivot_id")), 10, 64)
	if err != nil || pivotID == id {
		http.Error(w, "pivot_id required and != link id", http.StatusBadRequest)
		return
	}
	after := strings.TrimSpace(r.PostForm.Get("position")) != "before"

	link, err := s.store.GetLink(r.Context(), id)
	if err != nil {
		s.notFound(w, err)
		return
	}
	pivot, err := s.store.GetLink(r.Context(), pivotID)
	if err != nil {
		s.notFound(w, err)
		return
	}
	srcID := link.CollectionID
	if err := s.store.ReorderLink(r.Context(), id, pivotID, pivot.CollectionID, after); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// Cross-collection reorder: refresh both source and destination stats.
	if srcID != pivot.CollectionID {
		if src, _ := s.store.GetCollectionBySlugByID(r.Context(), srcID); src != nil {
			s.writeCollectionStatsOOB(w, r.Context(), src)
		}
	}
	if dst, _ := s.store.GetCollectionBySlugByID(r.Context(), pivot.CollectionID); dst != nil {
		s.writeCollectionStatsOOB(w, r.Context(), dst)
	}
}

// handleSaveNote stores the user's free-form personal note on a link
// and renders the note panel back as an HTMX fragment so the user
// gets a "saved" confirmation without losing their place.
func (s *Server) handleSaveNote(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	note := r.PostForm.Get("note")
	if err := s.store.UpdateLinkNote(r.Context(), id, note); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	link, err := s.store.GetLink(r.Context(), id)
	if err != nil {
		s.notFound(w, err)
		return
	}
	s.renderFragment(w, "link_note", map[string]any{
		"Link":      link,
		"JustSaved": true,
	})
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
	s.renderPageRq(w, r, "tags", map[string]any{
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
	s.renderPageRq(w, r, "tag_detail", map[string]any{
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

// handleRenameCollection updates the collection's slug and/or name.
// On a slug change we redirect to the new URL so the user's browser
// reflects the move; on a name-only change we just re-render the
// header card.
func (s *Server) handleRenameCollection(w http.ResponseWriter, r *http.Request) {
	col, err := s.store.GetCollectionBySlug(r.Context(), r.PathValue("slug"))
	if err != nil {
		s.notFound(w, err)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	newSlug := strings.TrimSpace(r.PostForm.Get("slug"))
	newName := strings.TrimSpace(r.PostForm.Get("name"))
	if newSlug == "" && newName == "" {
		http.Error(w, "slug or name required", http.StatusBadRequest)
		return
	}
	if err := s.store.RenameCollection(r.Context(), col.ID, newSlug, newName); err != nil {
		switch err {
		case storage.ErrSlugTaken:
			http.Error(w, "slug already taken", http.StatusConflict)
			return
		default:
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	// Redirect to the (possibly new) collection URL — works for both
	// htmx and plain form posts because htmx follows HX-Redirect.
	final := col.Slug
	if newSlug != "" {
		final = newSlug
	}
	w.Header().Set("HX-Redirect", "/c/"+final)
	http.Redirect(w, r, "/c/"+final, http.StatusSeeOther)
}

// handleSetFeed assigns or clears collection.feed_url. Empty value
// turns the collection back into a regular one.
func (s *Server) handleSetFeed(w http.ResponseWriter, r *http.Request) {
	col, err := s.store.GetCollectionBySlug(r.Context(), r.PathValue("slug"))
	if err != nil {
		s.notFound(w, err)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	url := strings.TrimSpace(r.PostForm.Get("feed_url"))
	if err := s.store.SetCollectionFeed(r.Context(), col.ID, url); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// Re-render the feed-control card.
	updated, _ := s.store.GetCollectionBySlugByID(r.Context(), col.ID)
	s.renderFragment(w, "collection_feed", map[string]any{
		"Collection": updated,
	})
}

// handleSaveFeed is the unified entry point for the feed form. The
// user pastes either a site URL or a direct feed URL into the same
// "url" field; we run Discover() which:
//
//   - returns the URL as-is when it's already a valid feed,
//   - otherwise fetches the page and looks for <link rel="alternate"
//     type="application/rss+xml|atom+xml">,
//   - or falls back to well-known paths under the site root.
//
// On success the resolved feed_url is saved on the collection. Empty
// "url" clears the feed (collection becomes a regular one again).
func (s *Server) handleSaveFeed(w http.ResponseWriter, r *http.Request) {
	col, err := s.store.GetCollectionBySlug(r.Context(), r.PathValue("slug"))
	if err != nil {
		s.notFound(w, err)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	raw := strings.TrimSpace(r.PostForm.Get("url"))

	// Empty → clear.
	if raw == "" {
		_ = s.store.SetCollectionFeed(r.Context(), col.ID, "")
		updated, _ := s.store.GetCollectionBySlugByID(r.Context(), col.ID)
		s.renderFragment(w, "collection_feed", map[string]any{"Collection": updated})
		return
	}

	feedURL, derr := s.feedImport.Discover(r.Context(), raw)
	if derr != nil {
		updated, _ := s.store.GetCollectionBySlugByID(r.Context(), col.ID)
		s.renderFragment(w, "collection_feed", map[string]any{
			"Collection":   updated,
			"DiscoverErr":  derr.Error(),
			"DiscoverFrom": raw,
		})
		return
	}
	if err := s.store.SetCollectionFeed(r.Context(), col.ID, feedURL); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	updated, _ := s.store.GetCollectionBySlugByID(r.Context(), col.ID)
	// Same trigger that "Save" used to do: optimistically pull the feed
	// once so the user immediately sees the entries.
	res, _ := s.feedImport.RefreshOne(r.Context(), col.ID)
	updated, _ = s.store.GetCollectionBySlugByID(r.Context(), col.ID)
	data := map[string]any{
		"Collection":   updated,
		"DiscoverdMsg": feedURL,
	}
	if res != nil {
		data["JustRefresh"] = true
		data["LastResult"] = res
	}
	s.renderFragment(w, "collection_feed", data)
	if updated != nil {
		s.writeCollectionStatsOOB(w, r.Context(), updated)
	}
}

// handleDiscoverFeed takes a free-form site URL ("page_url") and tries
// to detect the canonical RSS / Atom feed for it. On success the
// feed is saved on the collection and we re-render the feed card so
// the user sees the auto-detected URL pre-filled. On failure the
// card carries an inline "couldn't detect" message.
func (s *Server) handleDiscoverFeed(w http.ResponseWriter, r *http.Request) {
	col, err := s.store.GetCollectionBySlug(r.Context(), r.PathValue("slug"))
	if err != nil {
		s.notFound(w, err)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	pageURL := strings.TrimSpace(r.PostForm.Get("page_url"))
	if pageURL == "" {
		http.Error(w, "page_url required", http.StatusBadRequest)
		return
	}
	feedURL, err := s.feedImport.Discover(r.Context(), pageURL)
	if err != nil {
		updated, _ := s.store.GetCollectionBySlugByID(r.Context(), col.ID)
		s.renderFragment(w, "collection_feed", map[string]any{
			"Collection":   updated,
			"DiscoverErr":  err.Error(),
			"DiscoverFrom": pageURL,
		})
		return
	}
	if err := s.store.SetCollectionFeed(r.Context(), col.ID, feedURL); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	updated, _ := s.store.GetCollectionBySlugByID(r.Context(), col.ID)
	s.renderFragment(w, "collection_feed", map[string]any{
		"Collection":     updated,
		"DiscoverdMsg":   feedURL,
	})
}

// handleRefreshFeed triggers a one-shot poll. Result + freshly-rendered
// feed card are returned so the user sees "X added · Y skipped".
func (s *Server) handleRefreshFeed(w http.ResponseWriter, r *http.Request) {
	col, err := s.store.GetCollectionBySlug(r.Context(), r.PathValue("slug"))
	if err != nil {
		s.notFound(w, err)
		return
	}
	res, err := s.feedImport.RefreshOne(r.Context(), col.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	updated, _ := s.store.GetCollectionBySlugByID(r.Context(), col.ID)
	s.renderFragment(w, "collection_feed", map[string]any{
		"Collection":   updated,
		"LastResult":   res,
		"JustRefresh":  true,
	})
	// Stats refresh OOB so the link counter updates without a page reload.
	if updated != nil {
		s.writeCollectionStatsOOB(w, r.Context(), updated)
	}
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

func (s *Server) handleBookmarkletPage(w http.ResponseWriter, r *http.Request) {
	s.renderPageRq(w, r, "bookmarklet", map[string]any{"Title": "Bookmarklet"})
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

// themeFromRequest returns the active theme: "auto" (default, follows
// system), "light", or "dark". Reads the cookie first; falls back to
// the persisted preference; defaults to "auto" so the very first
// pageview matches the user's OS preference.
func (s *Server) themeFromRequest(r *http.Request) string {
	if c, err := r.Cookie("theme"); err == nil && validTheme(c.Value) {
		return c.Value
	}
	if v, err := s.store.GetPref(r.Context(), "theme"); err == nil && validTheme(v) {
		return v
	}
	return "auto"
}

func validTheme(v string) bool {
	return v == "auto" || v == "light" || v == "dark"
}

// handleSetTheme writes the new theme to both the cookie (so the
// next page load is correct without a DB round-trip) and the
// preferences table (so the choice survives a cookie purge).
func (s *Server) handleSetTheme(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	v := strings.TrimSpace(r.PostForm.Get("theme"))
	if !validTheme(v) {
		http.Error(w, "invalid theme", http.StatusBadRequest)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: "theme", Value: v, Path: "/",
		MaxAge: 60 * 60 * 24 * 365, SameSite: http.SameSiteLaxMode,
	})
	_ = s.store.SetPref(r.Context(), "theme", v)
	w.Header().Set("HX-Refresh", "true")
	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, `theme: <strong>%s</strong>`, v)
}

// previewsEnabled reads the show_previews cookie. Default ON: only an
// explicit "0" disables image/favicon previews so people who actively
// turned them off keep their choice across requests.
func previewsEnabled(r *http.Request) bool {
	if c, err := r.Cookie("show_previews"); err == nil {
		return c.Value != "0"
	}
	return true
}

// handleTogglePreviews flips the show_previews cookie and reloads the
// caller (HTMX-friendly: returns the new label fragment + sets the
// HX-Refresh header so the page picks up the new state immediately).
func (s *Server) handleTogglePreviews(w http.ResponseWriter, r *http.Request) {
	on := previewsEnabled(r)
	newVal := "1"
	if on {
		newVal = "0"
	}
	http.SetCookie(w, &http.Cookie{
		Name: "show_previews", Value: newVal, Path: "/", MaxAge: 60 * 60 * 24 * 365,
		HttpOnly: false, SameSite: http.SameSiteLaxMode,
	})
	w.Header().Set("HX-Refresh", "true")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if newVal == "1" {
		_, _ = w.Write([]byte(`previews: <strong>on</strong>`))
	} else {
		_, _ = w.Write([]byte(`previews: <strong>off</strong>`))
	}
}

// handleEvents is the SSE endpoint clients connect to once at page
// load. The worker publishes status changes to the broker; we forward
// them as text/event-stream frames the JS layer turns into targeted
// fragment refreshes. Replaces the old hx-trigger="every Ns" polling.
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	if s.events == nil {
		http.Error(w, "events broker not wired", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no") // disable proxy buffering
	rc := http.NewResponseController(w)
	flush := func() { _ = rc.Flush() }

	ch, cancel := s.events.Subscribe()
	defer cancel()

	// Initial nudge so the browser commits the response and the client
	// EventSource fires its onopen.
	fmt.Fprintf(w, "event: hello\ndata: ok\n\n")
	flush()

	// Keep-alive every 25s — proxies / browsers will close idle SSE
	// connections after a minute of silence.
	tick := time.NewTicker(25 * time.Second)
	defer tick.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-tick.C:
			fmt.Fprint(w, ": ping\n\n") // SSE comment
			flush()
		case ev, ok := <-ch:
			if !ok {
				return
			}
			fmt.Fprintf(w, "event: %s\ndata: {\"link_id\":%d,\"collection_id\":%d}\n\n",
				ev.Kind, ev.LinkID, ev.CollectionID)
			flush()
		}
	}
}

// handleLLMHealth returns "ok" when the worker reports a healthy LLM,
// "down: <reason>" otherwise. UI / topbar polls this every few seconds.
func (s *Server) handleLLMHealth(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if s.worker == nil {
		_, _ = w.Write([]byte(`<span class="muted" style="font-size:.8rem">no LLM</span>`))
		return
	}
	healthy, err, _ := s.worker.LLMHealth()
	if healthy {
		_, _ = w.Write([]byte(`<span class="muted" style="font-size:.8rem">LLM ok</span>`))
		return
	}
	msg := "offline"
	if err != nil {
		msg = err.Error()
		if len(msg) > 80 {
			msg = msg[:80] + "…"
		}
	}
	fmt.Fprintf(w, `<span class="badge failed" title="%s">LLM offline</span>`, htmlEscape(msg))
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
	s.renderPageRq(w, r, "search", map[string]any{
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
	s.renderPageRq(w, r, "chat", map[string]any{
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

// renderPageRq is a thin wrapper that injects per-request layout state
// (preview toggle, sidebar collections, theme, active route) into the
// page data so base.html can pick the right body class.
func (s *Server) renderPageRq(w http.ResponseWriter, r *http.Request, name string, data map[string]any) {
	if data == nil {
		data = map[string]any{}
	}
	data["Previews"] = previewsEnabled(r)
	data["Theme"] = s.themeFromRequest(r)

	// Sidebar list. Cheap query + we want it on every page; renderer
	// suppresses it by setting HideSidebar=true (e.g. reader mode).
	if _, hide := data["HideSidebar"]; !hide {
		if cols, err := s.store.ListCollectionsWithStats(r.Context()); err == nil {
			data["Sidebar"] = cols
		}
	}
	if _, has := data["ActiveSlug"]; !has {
		data["ActiveSlug"] = activeSlug(r.URL.Path)
	}
	s.renderPage(w, name, data)
}

// activeSlug picks the collection slug out of the request URL when we're
// inside /c/{slug}/.... Empty string for everything else so the sidebar's
// "All" entry highlights on the home page.
func activeSlug(path string) string {
	const prefix = "/c/"
	if !strings.HasPrefix(path, prefix) {
		return ""
	}
	rest := path[len(prefix):]
	if i := strings.IndexByte(rest, '/'); i >= 0 {
		return rest[:i]
	}
	return rest
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
