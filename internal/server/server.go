// Package server wires the HTTP routes for the HTMX UI. It is intentionally
// thin: handlers parse, call storage, and render templates. No business logic.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gabrielemastrapasqua/linklore/internal/chat"
	"github.com/gabrielemastrapasqua/linklore/internal/config"
	"github.com/gabrielemastrapasqua/linklore/internal/events"
	"github.com/gabrielemastrapasqua/linklore/internal/feed"
	"github.com/gabrielemastrapasqua/linklore/internal/feedimport"
	"github.com/gabrielemastrapasqua/linklore/internal/llm"
	"github.com/gabrielemastrapasqua/linklore/internal/netscape"
	"github.com/gabrielemastrapasqua/linklore/internal/reader"
	"github.com/gabrielemastrapasqua/linklore/internal/search"
	"github.com/gabrielemastrapasqua/linklore/internal/storage"
	"github.com/gabrielemastrapasqua/linklore/internal/tags"
	"github.com/gabrielemastrapasqua/linklore/internal/urlnorm"
	"github.com/gabrielemastrapasqua/linklore/internal/worker"
	"github.com/gabrielemastrapasqua/linklore/web"
)

type Server struct {
	cfgMu      sync.RWMutex
	cfg        config.Config
	cfgPath    string // path to config.yaml; "" disables /settings save
	store      *storage.Store
	r          *renderer
	search     *search.Engine // nil → search routes return empty results
	chat       *chat.Service  // nil → chat routes return 503
	feed       *feed.Builder
	feedImport *feedimport.Importer
	worker     *worker.Worker // optional, for refetch/reindex
	events     *events.Broker // optional, for SSE push to clients
	tagsCfg    tagsCfg

	// /checks scan progress flag (true while a scan is running).
	checkScanRunning atomic.Bool
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

// SetConfigPath records the path the server was loaded from so /settings
// can save back to it. Optional: an empty path leaves /settings save
// disabled (the form renders with a "no path" notice).
func (s *Server) SetConfigPath(path string) { s.cfgPath = path }

// currentConfig returns a copy of the live config. Handlers that read
// config-derived state should call this rather than reading s.cfg
// directly so a /settings save is reflected immediately.
func (s *Server) currentConfig() config.Config {
	s.cfgMu.RLock()
	defer s.cfgMu.RUnlock()
	return s.cfg
}

// updateConfig swaps in a new in-memory config. Worker / chat / search
// keep references to the backend they got at boot — those hot paths
// only see the new values after a process restart. /settings page
// surfaces this caveat to the user.
func (s *Server) updateConfig(c config.Config) {
	s.cfgMu.Lock()
	s.cfg = c
	s.tagsCfg = tagsCfg{MaxPerLink: c.Tags.MaxPerLink, ActiveCap: c.Tags.ActiveCap, ReuseDistance: c.Tags.ReuseDistance}
	s.cfgMu.Unlock()
}

// Handler returns the configured *http.ServeMux. Kept separate from ListenAndServe
// so tests can pass it directly to httptest.NewServer.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// static assets — served from the embedded FS so the binary stays portable
	staticFS, _ := fs.Sub(web.Static, "static")
	staticHandler := http.StripPrefix("/static/", http.FileServer(http.FS(staticFS)))
	// Static assets ship from embed.FS but baked into the binary at build
	// time. We send no-store so users never look at a cached copy of CSS
	// or JS that's older than their currently-running binary — the
	// surface area is small enough that the bandwidth cost is fine.
	mux.Handle("GET /static/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		staticHandler.ServeHTTP(w, r)
	}))

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("ok"))
	})

	mux.HandleFunc("GET /{$}", s.handleHome)
	mux.HandleFunc("POST /collections", s.handleCreateCollection)
	mux.HandleFunc("POST /collections/prune", s.handleDeleteEmptyCollections)
	mux.HandleFunc("DELETE /c/{slug}", s.handleDeleteCollection)
	mux.HandleFunc("GET /c/{slug}", s.handleListLinks)
	mux.HandleFunc("POST /c/{slug}/links", s.handleCreateLink)
	mux.HandleFunc("POST /c/{slug}/add", s.handleSmartAdd)
	mux.HandleFunc("POST /c/{slug}/rename", s.handleRenameCollection)
	mux.HandleFunc("POST /c/{slug}/layout", s.handleSetLayout)
	mux.HandleFunc("POST /c/{slug}/cover", s.handleSetCover)
	mux.HandleFunc("GET /c/{slug}/feed.xml", s.handleFeed)
	mux.HandleFunc("GET /c/{slug}/stats", s.handleCollectionStats)
	mux.HandleFunc("POST /c/{slug}/feed", s.handleSetFeed)               // legacy alias
	mux.HandleFunc("POST /c/{slug}/feed/save", s.handleSaveFeed)         // unified entry point
	mux.HandleFunc("POST /c/{slug}/feed/discover", s.handleDiscoverFeed) // legacy alias
	mux.HandleFunc("POST /c/{slug}/feed/refresh", s.handleRefreshFeed)
	mux.HandleFunc("DELETE /links/{id}", s.handleDeleteLink)
	mux.HandleFunc("POST /links/bulk/delete", s.handleBulkDelete)
	mux.HandleFunc("POST /links/bulk/move", s.handleBulkMove)
	mux.HandleFunc("POST /links/{id}/move", s.handleMoveLink)
	mux.HandleFunc("POST /links/{id}/reorder", s.handleReorderLink)
	mux.HandleFunc("GET /links/{id}", s.handleLinkDetail)
	mux.HandleFunc("GET /links/{id}/row", s.handleLinkRow)
	mux.HandleFunc("GET /links/{id}/header", s.handleLinkHeader)
	mux.HandleFunc("GET /links/{id}/read", s.handleReaderMode)
	mux.HandleFunc("GET /links/{id}/preview", s.handlePreview)
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

	mux.HandleFunc("GET /duplicates", s.handleDuplicates)
	mux.HandleFunc("POST /duplicates/delete", s.handleDuplicatesDelete)
	mux.HandleFunc("POST /import", s.handleImportNetscape)
	mux.HandleFunc("GET /c/{slug}/export.html", s.handleExportNetscape)
	mux.HandleFunc("GET /tags", s.handleTagsPage)
	mux.HandleFunc("GET /tags/{slug}", s.handleTagDetail)
	mux.HandleFunc("POST /tags/merge", s.handleMergeTags)

	mux.HandleFunc("GET /bookmarklet", s.handleBookmarkletPage)
	mux.HandleFunc("POST /api/links", s.handleAPILinks)

	mux.HandleFunc("GET /inbox", s.handlePlaceholder("Inbox", "Inbox is intentionally not implemented for now."))

	mux.HandleFunc("GET /settings", s.handleSettings)
	mux.HandleFunc("POST /settings", s.handleSaveSettings)
	mux.HandleFunc("POST /settings/test", s.handleTestSettings)

	mux.HandleFunc("GET /checks", s.handleChecksPage)
	mux.HandleFunc("POST /checks/run", s.handleChecksRun)
	mux.HandleFunc("GET /checks/summary", s.handleChecksSummary)

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
	name := strings.TrimSpace(r.PostForm.Get("name"))
	if name == "" {
		http.Error(w, "name required", http.StatusBadRequest)
		return
	}
	// Slug is derived from the name. Falls back to a numeric suffix
	// when the chosen slug already exists (rather than 4xx-ing the user).
	slug := s.uniqueSlugFromName(r.Context(), name)
	col, err := s.store.CreateCollection(r.Context(), slug, name, "")
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	// Wrap in a stat with zero counts so the card template renders correctly.
	cs := storage.CollectionStat{Collection: *col}
	s.renderFragment(w, "collection_card", cs)
	// OOB-append the matching sidebar entry so the left nav stays in
	// sync without a full page reload.
	var sb strings.Builder
	if err := s.r.partials.ExecuteTemplate(&sb, "sidebar_collection_entry", map[string]any{
		"Cs": cs, "ActiveSlug": "",
	}); err == nil {
		fmt.Fprintf(w, "\n<div hx-swap-oob=\"beforeend:#sidebar-collections\">%s</div>", sb.String())
	}
}

// handleImportNetscape accepts a Netscape Bookmark File upload + a
// target collection slug (or empty to bucket each link into a
// collection named after its source folder) and ingests each <a>
// it finds. Existing URLs are skipped (CreateLinkIfMissing handles
// that). Returns a JSON-ish summary line plus a redirect to the
// imported-into collection.
func (s *Server) handleImportNetscape(w http.ResponseWriter, r *http.Request) {
	// 16 MB cap covers a year of Pinboard exports comfortably.
	if err := r.ParseMultipartForm(16 << 20); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	file, _, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "file required (multipart name=file)", http.StatusBadRequest)
		return
	}
	defer file.Close()
	slug := strings.TrimSpace(r.FormValue("collection"))
	bookmarks, err := netscape.Parse(file)
	if err != nil {
		http.Error(w, fmt.Sprintf("parse: %v", err), http.StatusBadRequest)
		return
	}

	// When no collection slug is given, group by the source folder
	// (so a Chrome export's "Toolbar" / "Other bookmarks" land in
	// matching collections) — and fall back to "imported" when there's
	// no folder either.
	colCache := map[string]*storage.Collection{}
	resolve := func(folder string) (*storage.Collection, error) {
		key := slug
		display := slug
		if key == "" {
			key = strings.ToLower(folder)
			if key == "" {
				key = "imported"
			}
			display = folder
			if display == "" {
				display = "imported"
			}
		}
		if c, ok := colCache[key]; ok {
			return c, nil
		}
		// Try to find an existing collection with this slug.
		col, err := s.store.GetCollectionBySlug(r.Context(), key)
		if err == storage.ErrNotFound {
			col, err = s.store.CreateCollection(r.Context(), s.uniqueSlugFromName(r.Context(), display), display, "")
		}
		if err != nil {
			return nil, err
		}
		colCache[key] = col
		return col, nil
	}

	added, skipped, fail := 0, 0, 0
	for _, b := range bookmarks {
		col, err := resolve(b.Folder)
		if err != nil {
			fail++
			continue
		}
		_, created, err := s.store.CreateLinkIfMissing(r.Context(), col.ID, b.URL)
		if err != nil {
			fail++
			continue
		}
		if created {
			added++
		} else {
			skipped++
		}
	}
	log.Printf("import: added=%d skipped=%d failed=%d", added, skipped, fail)
	setToast(w, "ok", fmt.Sprintf("Imported %d new (%d skipped, %d failed)", added, skipped, fail))

	// Redirect somewhere sensible: the slug-target collection if one
	// was given, else the home page.
	target := "/"
	if slug != "" {
		target = "/c/" + slug
	}
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Redirect", target)
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

// handleExportNetscape streams every link in a collection as a
// Netscape Bookmark File, suitable for re-importing into any browser
// or bookmark manager. Tags from auto-tags + user tags are merged.
func (s *Server) handleExportNetscape(w http.ResponseWriter, r *http.Request) {
	col, err := s.store.GetCollectionBySlug(r.Context(), r.PathValue("slug"))
	if err != nil {
		s.notFound(w, err)
		return
	}
	links, err := s.store.ListLinksByCollection(r.Context(), col.ID, 10000, 0)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	entries := make([]netscape.WriteEntry, 0, len(links))
	for _, l := range links {
		ts, _ := s.store.ListTagsByLink(r.Context(), l.ID)
		var tagNames []string
		for _, t := range ts {
			tagNames = append(tagNames, t.Slug)
		}
		entries = append(entries, netscape.WriteEntry{
			URL: l.URL, Title: l.Title, Description: l.Summary,
			Tags: tagNames, Folder: col.Name, AddedAt: l.CreatedAt,
		})
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Disposition",
		fmt.Sprintf(`attachment; filename="linklore-%s.html"`, col.Slug))
	if err := netscape.Write(w, entries); err != nil {
		log.Printf("export %s: %v", col.Slug, err)
	}
}

// handleDuplicates groups every link by its normalised URL and renders
// the groups that have more than one member. Each group has a one-shot
// "delete duplicates" button that wipes everything except the oldest
// surviving link (lowest id wins — that's almost always the original
// save). Cheap enough to run synchronously: ListAllLinks streams the
// whole table and grouping happens in Go.
func (s *Server) handleDuplicates(w http.ResponseWriter, r *http.Request) {
	all, err := s.store.ListAllLinks(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	collections, _ := s.store.ListCollections(r.Context())
	colByID := map[int64]storage.Collection{}
	for _, c := range collections {
		colByID[c.ID] = c
	}
	type group struct {
		Key         string
		Links       []storage.Link
		Collections []storage.Collection
	}
	groups := map[string]*group{}
	for _, l := range all {
		k := urlnorm.Normalize(l.URL)
		if k == "" {
			continue
		}
		g, ok := groups[k]
		if !ok {
			g = &group{Key: k}
			groups[k] = g
		}
		g.Links = append(g.Links, l)
	}
	out := make([]*group, 0, len(groups))
	for _, g := range groups {
		if len(g.Links) <= 1 {
			continue
		}
		seen := map[int64]bool{}
		for _, l := range g.Links {
			if !seen[l.CollectionID] {
				seen[l.CollectionID] = true
				if c, ok := colByID[l.CollectionID]; ok {
					g.Collections = append(g.Collections, c)
				}
			}
		}
		out = append(out, g)
	}
	// Largest groups first so the worst offenders surface.
	sort.Slice(out, func(i, j int) bool { return len(out[i].Links) > len(out[j].Links) })
	s.renderPageRq(w, r, "duplicates", map[string]any{
		"Title":  "Duplicates",
		"Groups": out,
		"Total":  len(out),
	})
}

// handleDuplicatesDelete wipes every link in the comma-separated `ids`
// list except the one named `keep_id`. Returns redirects/refresh per
// HTMX so the duplicates page re-renders with the cleaned-up groups.
func (s *Server) handleDuplicatesDelete(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	keepID, _ := strconv.ParseInt(strings.TrimSpace(r.PostForm.Get("keep_id")), 10, 64)
	ids, err := parseBulkIDs(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	for _, id := range ids {
		if id == keepID {
			continue
		}
		_ = s.store.DeleteLink(r.Context(), id)
	}
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Redirect", "/duplicates")
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, "/duplicates", http.StatusSeeOther)
}

// handleSetLayout flips a collection between list/grid/headlines/
// moodboard view modes. Persists the choice; the actual class swap
// happens client-side (a tiny script driven by hx-on::after-request)
// so we don't have to re-render the whole list.
func (s *Server) handleSetLayout(w http.ResponseWriter, r *http.Request) {
	col, err := s.store.GetCollectionBySlug(r.Context(), r.PathValue("slug"))
	if err != nil {
		s.notFound(w, err)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	layout := strings.TrimSpace(r.PostForm.Get("layout"))
	if err := s.store.SetCollectionLayout(r.Context(), col.ID, layout); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleSetCover stores or clears the banner image for a collection.
// Form field "url" — empty value clears. HTMX clients get HX-Refresh
// so the new banner shows up; plain form posts get a 303 home.
func (s *Server) handleSetCover(w http.ResponseWriter, r *http.Request) {
	col, err := s.store.GetCollectionBySlug(r.Context(), r.PathValue("slug"))
	if err != nil {
		s.notFound(w, err)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := s.store.SetCollectionCover(r.Context(), col.ID,
		strings.TrimSpace(r.PostForm.Get("url"))); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if r.Header.Get("HX-Request") == "true" {
		setToast(w, "ok", "Cover updated")
		w.Header().Set("HX-Refresh", "true")
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, "/c/"+col.Slug, http.StatusSeeOther)
}

// handleDeleteEmptyCollections sweeps every collection that has zero
// links and isn't feed-backed (we don't want to lose a freshly-added
// RSS subscription that hasn't pulled its first batch yet) and deletes
// them. Returns to the home page so the user sees the cleaner list.
func (s *Server) handleDeleteEmptyCollections(w http.ResponseWriter, r *http.Request) {
	stats, err := s.store.ListCollectionsWithStats(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	deleted := 0
	for _, cs := range stats {
		if cs.Total == 0 && cs.FeedURL == "" {
			if err := s.store.DeleteCollection(r.Context(), cs.ID); err == nil {
				deleted++
			}
		}
	}
	log.Printf("collections: pruned %d empty", deleted)
	setToast(w, "ok", fmt.Sprintf("Pruned %d empty collection%s", deleted, plural(deleted)))
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Redirect", "/")
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// handleDeleteCollection wipes the collection and every link inside
// it. If the request was issued from the collection's own page we
// respond with HX-Redirect so the browser navigates away from the
// now-404 URL; otherwise we OOB-remove the matching sidebar entry and
// the (optional) collection card on the home page.
func (s *Server) handleDeleteCollection(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	col, err := s.store.GetCollectionBySlug(r.Context(), slug)
	if err != nil {
		s.notFound(w, err)
		return
	}
	if err := s.store.DeleteCollection(r.Context(), col.ID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if strings.Contains(r.Header.Get("HX-Current-URL"), "/c/"+slug) {
		w.Header().Set("HX-Redirect", "/")
		w.WriteHeader(http.StatusOK)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, `<a id="sidebar-collection-%d" hx-swap-oob="delete"></a>`, col.ID)
	fmt.Fprintf(w, `<div id="collection-%d" hx-swap-oob="delete"></div>`, col.ID)
}

// ensureCollectionForIngest resolves the collection for an inbound
// ingest call (bookmarklet, smart-add). When the slug is empty or
// "default" and missing, it auto-creates the canonical "Default"
// collection on the fly. uniqueSlugFromName guarantees we don't
// collide with a user-created entry that happens to use the slug.
func (s *Server) ensureCollectionForIngest(ctx context.Context, slug string) (*storage.Collection, error) {
	slug = strings.TrimSpace(slug)
	if slug == "" {
		slug = "default"
	}
	col, err := s.store.GetCollectionBySlug(ctx, slug)
	if err == nil {
		return col, nil
	}
	if !errors.Is(err, storage.ErrNotFound) {
		return nil, err
	}
	if slug == "default" {
		return s.store.CreateCollection(ctx, s.uniqueSlugFromName(ctx, "Default"), "Default", "")
	}
	return s.store.CreateCollection(ctx, slug, slug, "")
}

// uniqueSlugFromName turns a free-form name into a valid slug and
// resolves clashes by appending -2, -3, … until the slug is unused.
func (s *Server) uniqueSlugFromName(ctx context.Context, name string) string {
	base := tags.Slugify(name)
	if base == "" {
		base = "collection"
	}
	candidate := base
	for n := 2; n < 100; n++ {
		if _, err := s.store.GetCollectionBySlug(ctx, candidate); err == storage.ErrNotFound {
			return candidate
		}
		candidate = fmt.Sprintf("%s-%d", base, n)
	}
	// Pathological: 99 collisions. Just return the last candidate.
	return candidate
}

func (s *Server) handleListLinks(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	col, err := s.store.GetCollectionBySlug(r.Context(), slug)
	if err != nil {
		s.notFound(w, err)
		return
	}
	// Auto-refresh feed on entry when the collection is feed-backed and
	// hasn't been polled recently. Skipped when the request carries any
	// querystring (kind/layout filter clicks) — those are pure
	// view-state changes and should NEVER block on an upstream fetch.
	// Skipped fully when the gofeed call is in flight too: the refresh
	// runs in a detached goroutine so even a hung 30s upstream can't
	// freeze the page render.
	if s.feedImport != nil && col.FeedURL != "" && r.URL.RawQuery == "" {
		stale := col.LastCheckedAt == nil || time.Since(*col.LastCheckedAt) > 15*time.Minute
		if stale {
			go func(colID int64, slug string) {
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				if _, ferr := s.feedImport.RefreshOne(ctx, colID); ferr != nil {
					log.Printf("auto-refresh feed for %s: %v", slug, ferr)
				}
			}(col.ID, col.Slug)
		}
	}
	// Pagination knobs from the querystring. ?per accepts 50 / 100 / 0,
	// where 0 means "all" (capped at 5000 to avoid runaway memory). A
	// missing ?per defaults to 50; an explicit ?per=0 must be honoured
	// as "all" (the default-vs-explicit-zero distinction matters).
	per := 50
	if perRaw := r.URL.Query().Get("per"); perRaw != "" {
		if n, err := strconv.Atoi(strings.TrimSpace(perRaw)); err == nil {
			switch n {
			case 0, 50, 100:
				per = n
			}
		}
	}
	page, _ := strconv.Atoi(strings.TrimSpace(r.URL.Query().Get("page")))
	if page < 1 {
		page = 1
	}
	limit := per
	offset := (page - 1) * limit
	if per == 0 { // "all" — ignore page, hard-cap at 5000
		limit = 5000
		offset = 0
		page = 1
	}
	links, err := s.store.ListLinksByCollection(r.Context(), col.ID, limit, offset)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// Optional ?kind=video filter applied in-Go. Note: this trims a
	// page after pagination, so a filtered page may be smaller than
	// `per`. Acceptable for v1 — pagination still tracks the
	// unfiltered total. Revisit if filter+paginate combos prove common.
	kindFilter := strings.TrimSpace(r.URL.Query().Get("kind"))
	if kindFilter != "" {
		filtered := links[:0]
		for _, l := range links {
			if l.Kind == kindFilter {
				filtered = append(filtered, l)
			}
		}
		links = filtered
	}
	stats, _ := s.store.CollectionStatsByID(r.Context(), col.ID)
	totalPages := 1
	if per > 0 && stats.Total > 0 {
		totalPages = (stats.Total + per - 1) / per
	}
	allCollections, _ := s.store.ListCollections(r.Context())
	s.renderPageRq(w, r, "links", map[string]any{
		"Title":          col.Name,
		"Collection":     col,
		"Links":          links,
		"Stats":          stats,
		"AllCollections": allCollections,
		"KindFilter":     kindFilter,
		"Page":           page,
		"Per":            per,
		"TotalPages":     totalPages,
	})
}

// looksLikeFeedURL is a cheap heuristic — no network — that decides
// whether a pasted URL probably points at an RSS/Atom feed. Used by
// handleSmartAdd to route "feed-shaped" URLs into the subscribe path
// while everything else becomes a single link. False negatives just
// get added as links (user can fix that with the feed card); false
// positives surface as a "refresh failed: …" inline error and are
// trivial to clear.
func looksLikeFeedURL(raw string) bool {
	s := strings.ToLower(strings.TrimSpace(raw))
	if s == "" {
		return false
	}
	// Drop querystring + fragment for the suffix test.
	if i := strings.IndexAny(s, "?#"); i > 0 {
		s = s[:i]
	}
	suffixes := []string{
		".xml", ".atom", ".rss",
		"/feed", "/feed/", "/rss", "/rss/", "/atom", "/atom/",
		"/feed.xml", "/rss.xml", "/atom.xml", "/index.xml",
	}
	for _, sfx := range suffixes {
		if strings.HasSuffix(s, sfx) {
			return true
		}
	}
	return false
}

// handleSmartAdd is the single entry point for the unified
// "Add to this collection" input. Pasted URLs that look like an
// RSS/Atom feed (and only when the collection doesn't already
// have one) become the collection's feed_url; everything else
// is added as a regular link. This collapses what used to be two
// near-identical forms into one.
func (s *Server) handleSmartAdd(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	slug := r.PathValue("slug")
	col, err := s.store.GetCollectionBySlug(r.Context(), slug)
	if errors.Is(err, storage.ErrNotFound) && slug == "default" {
		col, err = s.ensureCollectionForIngest(r.Context(), slug)
	}
	if err != nil {
		s.notFound(w, err)
		return
	}
	raw := strings.TrimSpace(r.PostForm.Get("url"))
	if raw == "" {
		http.Error(w, "url required", http.StatusBadRequest)
		return
	}

	// Feed path: only when the user pasted something feed-shaped AND
	// this collection isn't already feed-backed. Refusing to overwrite
	// an existing feed_url avoids "I added a video, why did my RSS
	// subscription disappear?" support tickets.
	if col.FeedURL == "" && looksLikeFeedURL(raw) {
		feedURL, derr := s.feedImport.Discover(r.Context(), raw)
		if derr == nil && feedURL != "" {
			if err := s.store.SetCollectionFeed(r.Context(), col.ID, feedURL); err == nil {
				_, _ = s.feedImport.RefreshOne(r.Context(), col.ID)
				updated, _ := s.store.GetCollectionBySlugByID(r.Context(), col.ID)
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				// Re-render the feed card OOB so the URL appears with
				// the "feed detected" notice; client follows up with a
				// page reload to pull in the freshly imported links.
				w.Header().Set("HX-Refresh", "true")
				s.renderFragment(w, "collection_feed", map[string]any{
					"Collection":   updated,
					"DiscoverdMsg": feedURL,
				})
				if updated != nil {
					s.writeCollectionStatsOOB(w, r.Context(), updated)
				}
				return
			}
		}
		// Discover failed — fall through and treat as a regular link.
	}

	link, err := s.store.CreateLink(r.Context(), col.ID, raw)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	s.renderFragment(w, "link_row", link)
	s.writeCollectionStatsOOB(w, r.Context(), col)
	s.writeEmptyStateOOB(w, true)

	// Quick non-blocking probe: does this page advertise a feed via
	// <link rel="alternate">? If yes — and the collection isn't already
	// feed-backed — surface an inline "subscribe to feed instead?" banner.
	// We don't auto-subscribe; a single pasted URL stays a single link
	// unless the user explicitly opts in. Time-boxed at 3s so a slow
	// upstream can't stall the response.
	if col.FeedURL == "" && s.feedImport != nil {
		probeCtx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()
		if feedURL, derr := s.feedImport.Discover(probeCtx, raw); derr == nil && feedURL != "" && feedURL != raw {
			s.renderFragment(w, "feed_offer", map[string]any{
				"Slug":    col.Slug,
				"FeedURL": feedURL,
				"PageURL": raw,
			})
		}
	}
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
// The hint copy is tailored to the configured backend so Ollama users
// don't see "set LITELLM_API_KEY" and vice-versa.
func (s *Server) llmHealthSnapshot() (bool, string) {
	if s.worker == nil {
		return false, s.llmConfigHint()
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

// llmConfigHint produces the "how to enable the LLM" copy, varying by
// the active backend so the user sees the right env var to set.
func (s *Server) llmConfigHint() string {
	switch s.cfg.LLM.Backend {
	case llm.BackendOllama:
		return "LLM disabled — start Ollama and set OLLAMA_HOST (or llm.ollama.host + llm.ollama.model in config.yaml)"
	case llm.BackendLitellm:
		return "LLM disabled — set llm.litellm.base_url + llm.litellm.model (or LITELLM_BASE_URL + LITELLM_API_KEY env vars)"
	default:
		return "LLM disabled — set llm.backend to ollama or litellm in config.yaml"
	}
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

// handlePreview returns the article rendered for the slide-in drawer
// — just the inner content, no chrome — so HTMX can swap it into the
// global #drawer-content target. The full /links/:id/read page stays
// as the deep link.
func (s *Server) handlePreview(w http.ResponseWriter, r *http.Request) {
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
	s.renderFragment(w, "preview_drawer", map[string]any{
		"Link":    link,
		"Article": reader.Render(link.ContentMD),
	})
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

// setToast emits the HX-Trigger header that the client-side toasts.js
// listens for. JSON-encoded so HTMX dispatches a CustomEvent with the
// detail payload intact. Call BEFORE WriteHeader / first body write.
func setToast(w http.ResponseWriter, kind, message string) {
	// Conservative escaping — avoid pulling in encoding/json for a
	// fixed shape and let the message stay verbatim. Backslashes and
	// quotes are the only chars that break the JSON.
	escape := func(s string) string {
		s = strings.ReplaceAll(s, `\`, `\\`)
		return strings.ReplaceAll(s, `"`, `\"`)
	}
	w.Header().Set("HX-Trigger",
		fmt.Sprintf(`{"linklore-toast":{"kind":"%s","message":"%s"}}`,
			escape(kind), escape(message)))
}

// handleBulkDelete removes every link whose id is listed in the "ids"
// form field (comma-separated). Responds with one OOB row-removal per
// id and one OOB stats refresh per affected collection.
func (s *Server) handleBulkDelete(w http.ResponseWriter, r *http.Request) {
	ids, err := parseBulkIDs(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	affected := map[int64]struct{}{}
	deleted := 0
	for _, id := range ids {
		link, gerr := s.store.GetLink(r.Context(), id)
		if gerr != nil {
			continue
		}
		if derr := s.store.DeleteLink(r.Context(), id); derr != nil {
			continue
		}
		affected[link.CollectionID] = struct{}{}
		deleted++
	}
	setToast(w, "ok", fmt.Sprintf("Deleted %d link%s", deleted, plural(deleted)))
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	for id := range affected {
		_ = id // already counted; iterate explicit list below
	}
	for _, id := range ids {
		fmt.Fprintf(w, `<div id="link-%d" hx-swap-oob="outerHTML"></div>`, id)
	}
	for cid := range affected {
		col, _ := s.store.GetCollectionBySlugByID(r.Context(), cid)
		if col == nil {
			continue
		}
		s.writeCollectionStatsOOB(w, r.Context(), col)
		stats, _ := s.store.CollectionStatsByID(r.Context(), cid)
		if stats.Total == 0 {
			s.writeEmptyStateOOB(w, false)
		}
	}
}

// plural returns "s" when n != 1 — keeps message strings clean.
func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// handleBulkMove moves every link in "ids" to the destination
// collection. Same OOB pattern as handleBulkDelete: rows on the source
// page disappear, source + destination stats refresh.
func (s *Server) handleBulkMove(w http.ResponseWriter, r *http.Request) {
	ids, err := parseBulkIDs(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	dstID, err := strconv.ParseInt(strings.TrimSpace(r.PostForm.Get("collection_id")), 10, 64)
	if err != nil || dstID <= 0 {
		http.Error(w, "collection_id required", http.StatusBadRequest)
		return
	}
	if _, err := s.store.GetCollectionBySlugByID(r.Context(), dstID); err != nil {
		s.notFound(w, err)
		return
	}
	affected := map[int64]struct{}{dstID: {}}
	moved := 0
	movedIDs := make([]int64, 0, len(ids))
	for _, id := range ids {
		link, gerr := s.store.GetLink(r.Context(), id)
		if gerr != nil {
			continue
		}
		if link.CollectionID == dstID {
			continue
		}
		if merr := s.store.MoveLink(r.Context(), id, dstID); merr != nil {
			continue
		}
		affected[link.CollectionID] = struct{}{}
		moved++
		movedIDs = append(movedIDs, id)
	}
	dstName := ""
	if dst, _ := s.store.GetCollectionBySlugByID(r.Context(), dstID); dst != nil {
		dstName = dst.Name
	}
	setToast(w, "ok", fmt.Sprintf("Moved %d link%s to %q", moved, plural(moved), dstName))
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	for _, id := range movedIDs {
		fmt.Fprintf(w, `<div id="link-%d" hx-swap-oob="outerHTML"></div>`, id)
	}
	emptied := false
	for cid := range affected {
		col, _ := s.store.GetCollectionBySlugByID(r.Context(), cid)
		if col == nil {
			continue
		}
		s.writeCollectionStatsOOB(w, r.Context(), col)
		stats, _ := s.store.CollectionStatsByID(r.Context(), cid)
		if cid != dstID && stats.Total == 0 {
			emptied = true
		}
	}
	if emptied {
		s.writeEmptyStateOOB(w, false)
	}
}

// parseBulkIDs reads the "ids" form field — accepts both repeated
// "ids=1&ids=2" and comma-separated "ids=1,2,3" — and returns the
// parsed int64 slice. Empty input is an error so handlers can bail
// before doing any DB work.
func parseBulkIDs(r *http.Request) ([]int64, error) {
	if err := r.ParseForm(); err != nil {
		return nil, err
	}
	raw := r.PostForm["ids"]
	var parts []string
	for _, v := range raw {
		for _, p := range strings.Split(v, ",") {
			if p = strings.TrimSpace(p); p != "" {
				parts = append(parts, p)
			}
		}
	}
	if len(parts) == 0 {
		return nil, fmt.Errorf("ids required")
	}
	out := make([]int64, 0, len(parts))
	for _, p := range parts {
		id, err := strconv.ParseInt(p, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("bad id %q: %w", p, err)
		}
		out = append(out, id)
	}
	return out, nil
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

// handleRenameCollection updates the collection's name. The slug is
// re-derived from the new name (with a numeric suffix when there's a
// clash). On a slug change we redirect to the new URL.
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
	newName := strings.TrimSpace(r.PostForm.Get("name"))
	if newName == "" {
		http.Error(w, "name required", http.StatusBadRequest)
		return
	}

	// Re-derive slug. If it lands on the SAME slug we already have, no
	// rename of the slug column happens; if it differs, find a free one.
	derived := tags.Slugify(newName)
	if derived == "" {
		derived = "collection"
	}
	finalSlug := derived
	if derived != col.Slug {
		// Clash with another collection? Try -2, -3, …
		for n := 2; n < 100; n++ {
			existing, err := s.store.GetCollectionBySlug(r.Context(), finalSlug)
			if err == storage.ErrNotFound {
				break
			}
			if existing != nil && existing.ID == col.ID {
				break // matches us, fine
			}
			finalSlug = fmt.Sprintf("%s-%d", derived, n)
		}
	}

	if err := s.store.RenameCollection(r.Context(), col.ID, finalSlug, newName); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	target := "/c/" + finalSlug
	// HTMX clients: HX-Redirect navigates the whole page. We must NOT
	// also write a 303 body, otherwise HTMX swaps the redirected page
	// into the form target — that's the "iframe inside the page" bug.
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Redirect", target)
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
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
// feed card are returned so the user sees "X added · Y skipped". On
// upstream errors (e.g. the feed host returns 404) we re-render the
// card with the error message inline instead of erroring out — that
// way HTMX still swaps the fragment and the user sees what went wrong.
func (s *Server) handleRefreshFeed(w http.ResponseWriter, r *http.Request) {
	col, err := s.store.GetCollectionBySlug(r.Context(), r.PathValue("slug"))
	if err != nil {
		s.notFound(w, err)
		return
	}
	data := map[string]any{"Collection": col}
	res, err := s.feedImport.RefreshOne(r.Context(), col.ID)
	if err != nil {
		data["RefreshErr"] = err.Error()
	} else {
		data["LastResult"] = res
		data["JustRefresh"] = true
	}
	updated, _ := s.store.GetCollectionBySlugByID(r.Context(), col.ID)
	if updated != nil {
		data["Collection"] = updated
	}
	s.renderFragment(w, "collection_feed", data)
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
	col, err := s.ensureCollectionForIngest(r.Context(), slug)
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

// handleLLMHealth renders a small pill in the topbar reflecting the
// current backend health: green when healthy, red (with the error in
// title=) when the worker can't reach the model. The fragment is
// poll-friendly — handler is cheap, output is a single span.
func (s *Server) handleLLMHealth(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if s.worker == nil {
		_, _ = w.Write([]byte(`<span class="status-pill status-err" title="no LLM worker configured">LLM</span>`))
		return
	}
	healthy, err, _ := s.worker.LLMHealth()
	if healthy {
		_, _ = w.Write([]byte(`<span class="status-pill status-ok" title="LLM backend reachable">LLM</span>`))
		return
	}
	msg := "offline"
	if err != nil {
		msg = err.Error()
		if len(msg) > 200 {
			msg = msg[:200] + "…"
		}
	}
	fmt.Fprintf(w, `<span class="status-pill status-err" title="%s">LLM</span>`, htmlEscape(msg))
}

func (s *Server) handleWorkerStatus(w http.ResponseWriter, r *http.Request) {
	n, err := s.store.CountInProgress(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if n == 0 {
		_, _ = w.Write([]byte(
			`<span class="status-dot status-idle" title="worker idle"></span>`))
		return
	}
	fmt.Fprintf(w,
		`<span class="status-dot status-busy" title="worker processing %d link%s"></span>`,
		n, plural(n))
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
	counts, _ := s.store.LinkStatusCounts(r.Context())
	data := map[string]any{
		"Title":      "Chat",
		"Ready":      counts.Ready,
		"InProgress": counts.InProgress,
		"Failed":     counts.Failed,
		"Backend":    s.cfg.LLM.Backend,
		"Model":      s.activeModelName(),
		"Ask":        strings.TrimSpace(r.URL.Query().Get("ask")),
		"LinkID":     strings.TrimSpace(r.URL.Query().Get("link")),
	}
	if s.chat == nil {
		data["Disabled"] = true
		data["DisabledHint"] = s.llmConfigHint()
	}
	s.renderPageRq(w, r, "chat", data)
}

// activeModelName returns the user-facing name of whichever LLM model the
// chat is currently calling. Useful in the UI so the user knows what's
// generating the answer.
func (s *Server) activeModelName() string {
	switch s.cfg.LLM.Backend {
	case llm.BackendLitellm:
		return s.cfg.LLM.LiteLLM.Model
	case llm.BackendOllama:
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
	// Retrieval transparency: emit before LLM streaming so the user sees
	// how broadly RAG fired (chunks|distinct links|context bytes) up front.
	fmt.Fprintf(w, "event: retrieval\ndata: %d|%d|%d\n\n",
		turn.Stats.Chunks, turn.Stats.LinkCount, turn.Stats.ContextBytes)
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

// ---- /settings ----

// settingsForm holds the editable subset of llm config exposed to /settings.
// We collapse the per-backend host/base_url into a single "endpoint" field
// so the user only sees one URL input.
type settingsForm struct {
	Backend    string
	Endpoint   string
	Model      string
	EmbedModel string
	APIKey     string
}

func (s *Server) settingsFormFromConfig() settingsForm {
	c := s.currentConfig()
	f := settingsForm{Backend: c.LLM.Backend}
	switch c.LLM.Backend {
	case llm.BackendLitellm:
		f.Endpoint = c.LLM.LiteLLM.BaseURL
		f.Model = c.LLM.LiteLLM.Model
		f.EmbedModel = c.LLM.LiteLLM.EmbedModel
		f.APIKey = c.LLM.LiteLLM.APIKey
	case llm.BackendOllama:
		f.Endpoint = c.LLM.Ollama.Host
		f.Model = c.LLM.Ollama.Model
		f.EmbedModel = c.LLM.Ollama.EmbedModel
	}
	return f
}

func parseSettingsForm(r *http.Request) settingsForm {
	return settingsForm{
		Backend:    strings.TrimSpace(r.PostForm.Get("backend")),
		Endpoint:   strings.TrimSpace(r.PostForm.Get("endpoint")),
		Model:      strings.TrimSpace(r.PostForm.Get("model")),
		EmbedModel: strings.TrimSpace(r.PostForm.Get("embed_model")),
		APIKey:     r.PostForm.Get("api_key"),
	}
}

func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	s.renderPageRq(w, r, "settings", map[string]any{
		"Title":    "Settings",
		"Form":     s.settingsFormFromConfig(),
		"CfgPath":  s.cfgPath,
		"Backends": []string{llm.BackendNone, llm.BackendOllama, llm.BackendLitellm},
	})
}

func (s *Server) handleSaveSettings(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	form := parseSettingsForm(r)
	switch form.Backend {
	case llm.BackendNone, llm.BackendOllama, llm.BackendLitellm:
	default:
		s.writeSettingsBanner(w, "err", "Unknown backend: "+form.Backend)
		return
	}

	// Apply to a copy of the current config.
	c := s.currentConfig()
	c.LLM.Backend = form.Backend
	switch form.Backend {
	case llm.BackendLitellm:
		c.LLM.LiteLLM.BaseURL = form.Endpoint
		c.LLM.LiteLLM.Model = form.Model
		c.LLM.LiteLLM.EmbedModel = form.EmbedModel
		c.LLM.LiteLLM.APIKey = form.APIKey
	case llm.BackendOllama:
		c.LLM.Ollama.Host = form.Endpoint
		c.LLM.Ollama.Model = form.Model
		c.LLM.Ollama.EmbedModel = form.EmbedModel
	}
	if err := c.Validate(); err != nil {
		s.writeSettingsBanner(w, "err", err.Error())
		return
	}

	// Persist when we have a path; otherwise just update memory.
	if s.cfgPath != "" {
		if err := c.SaveYAML(s.cfgPath); err != nil {
			s.writeSettingsBanner(w, "err", err.Error())
			return
		}
	}
	s.updateConfig(c)
	s.writeSettingsBanner(w, "ok", "Settings saved. Worker keeps the previous backend until restart.")
}

func (s *Server) writeSettingsBanner(w http.ResponseWriter, kind, msg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	cls := "status-pill status-ok"
	if kind == "err" {
		cls = "status-pill status-err"
	}
	fmt.Fprintf(w, `<span class="%s">%s</span>`, cls, htmlEscape(msg))
}

// handleTestSettings probes the configured backend without mutating any
// persisted state. Reads only the form fields. 5s context-bound timeout.
func (s *Server) handleTestSettings(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	form := parseSettingsForm(r)
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	switch form.Backend {
	case llm.BackendNone, "":
		s.writeSettingsBanner(w, "err", "Backend disabled")
		return
	case llm.BackendLitellm:
		count, err := probeLitellm(ctx, form.Endpoint, form.APIKey)
		if err != nil {
			s.writeSettingsBanner(w, "err", err.Error())
			return
		}
		s.writeSettingsBanner(w, "ok", fmt.Sprintf("Reachable — %d models", count))
	case llm.BackendOllama:
		count, err := probeOllama(ctx, form.Endpoint)
		if err != nil {
			s.writeSettingsBanner(w, "err", err.Error())
			return
		}
		s.writeSettingsBanner(w, "ok", fmt.Sprintf("Reachable — %d models", count))
	default:
		s.writeSettingsBanner(w, "err", "Unknown backend: "+form.Backend)
	}
}

// probeLitellm GET base/models with optional Bearer auth. Returns the
// length of the .data array.
func probeLitellm(ctx context.Context, baseURL, apiKey string) (int, error) {
	if baseURL == "" {
		return 0, fmt.Errorf("base_url required")
	}
	url := strings.TrimRight(baseURL, "/") + "/models"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, err
	}
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		snip := strings.TrimSpace(string(body))
		if snip == "" {
			snip = http.StatusText(resp.StatusCode)
		}
		return 0, fmt.Errorf("HTTP %d: %s", resp.StatusCode, snip)
	}
	var out struct {
		Data []json.RawMessage `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return 0, fmt.Errorf("parse response: %w", err)
	}
	return len(out.Data), nil
}

// probeOllama GET host/api/tags. Returns len(.models).
func probeOllama(ctx context.Context, host string) (int, error) {
	if host == "" {
		return 0, fmt.Errorf("host required")
	}
	url := strings.TrimRight(host, "/") + "/api/tags"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return 0, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	var out struct {
		Models []json.RawMessage `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return 0, fmt.Errorf("parse response: %w", err)
	}
	return len(out.Models), nil
}

// ---- /checks ----

func (s *Server) handleChecksPage(w http.ResponseWriter, r *http.Request) {
	counts, err := s.store.CountLinkChecks(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	broken, _ := s.store.ListBrokenLinks(r.Context(), 50)
	s.renderPageRq(w, r, "checks", map[string]any{
		"Title":   "Link checker",
		"Counts":  counts,
		"Broken":  broken,
		"Running": s.checkScanRunning.Load(),
	})
}

func (s *Server) handleChecksSummary(w http.ResponseWriter, r *http.Request) {
	counts, err := s.store.CountLinkChecks(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w,
		`<span class="muted">%d links · %d ok · %d broken · %d timed out · %d never checked</span>`,
		counts.Total, counts.OK, counts.Broken, counts.Timeout, counts.NeverChecked)
}

func (s *Server) handleChecksRun(w http.ResponseWriter, r *http.Request) {
	if !s.checkScanRunning.CompareAndSwap(false, true) {
		setToast(w, "warn", "A scan is already running")
		w.WriteHeader(http.StatusOK)
		return
	}
	go s.runDeadLinkScan(context.Background())

	setToast(w, "ok", "Scan started — refresh to see progress")
	w.WriteHeader(http.StatusOK)
}

// runDeadLinkScan walks every link, HEADs each one with a 5s timeout,
// and persists the result. Cap parallelism at 8 simultaneous requests.
// Publishes a stats_changed event per link so any open SSE client picks
// up live progress.
func (s *Server) runDeadLinkScan(ctx context.Context) {
	defer s.checkScanRunning.Store(false)

	links, err := s.store.ListAllLinks(ctx)
	if err != nil {
		log.Printf("checks: list links: %v", err)
		return
	}

	// Bounded fan-out: 8 simultaneous HEAD requests.
	sem := make(chan struct{}, 8)
	var wg sync.WaitGroup
	client := &http.Client{
		Timeout: 5 * time.Second,
		// HEAD redirects are followed by default — that's what we want;
		// a 200 after redirect is "ok". Cap to 5 hops to stop loops.
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return http.ErrUseLastResponse
			}
			return nil
		},
	}
	for _, l := range links {
		l := l
		sem <- struct{}{}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			status, code := probeLink(ctx, client, l.URL)
			if err := s.store.UpdateLinkCheck(ctx, l.ID, status, code); err != nil {
				log.Printf("checks: update %d: %v", l.ID, err)
				return
			}
			if s.events != nil {
				s.events.Publish(events.Event{
					Kind: events.KindStatsChanged, LinkID: l.ID, CollectionID: l.CollectionID,
				})
			}
		}()
	}
	wg.Wait()
}

// probeLink classifies the outcome of one HEAD request.
// status is one of: ok, broken, timeout, dns, 5xx.
func probeLink(ctx context.Context, client *http.Client, url string) (status string, code int) {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, url, nil)
	if err != nil {
		return "broken", 0
	}
	resp, err := client.Do(req)
	if err != nil {
		// Distinguish timeout / DNS / generic transport.
		var netErr net.Error
		if errors.As(err, &netErr) && netErr.Timeout() {
			return "timeout", 0
		}
		var dnsErr *net.DNSError
		if errors.As(err, &dnsErr) {
			return "dns", 0
		}
		return "broken", 0
	}
	defer resp.Body.Close()
	code = resp.StatusCode
	switch {
	case code >= 200 && code < 400:
		return "ok", code
	case code >= 500:
		return "5xx", code
	default:
		return "broken", code
	}
}
