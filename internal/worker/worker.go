// Package worker stitches Phases 3-5 into a single background pipeline:
//
//	pending  → fetch+extract  → fetched
//	fetched  → chunk+embed+summarize+tag → summarized
//
// It is intentionally simple: a polling loop with bounded concurrency.
// Resilience comes from idempotent transitions (any failure leaves the row
// in a recoverable state) plus exponential-ish backoff on LLM/embed errors.
//
// Save NEVER blocks on the worker — the HTTP handler inserts a `pending`
// row and returns immediately. The worker catches up asynchronously.
package worker

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/gabrielemastrapasqua/linklore/internal/archive"
	"github.com/gabrielemastrapasqua/linklore/internal/chunking"
	"github.com/gabrielemastrapasqua/linklore/internal/config"
	"github.com/gabrielemastrapasqua/linklore/internal/embed"
	"github.com/gabrielemastrapasqua/linklore/internal/extract"
	"github.com/gabrielemastrapasqua/linklore/internal/lang"
	"github.com/gabrielemastrapasqua/linklore/internal/llm"
	"github.com/gabrielemastrapasqua/linklore/internal/storage"
	"github.com/gabrielemastrapasqua/linklore/internal/summarize"
	"golang.org/x/sync/errgroup"
)

// Fetcher is the subset of *extract.Fetcher we need (so tests can fake it).
type Fetcher interface {
	Fetch(ctx context.Context, url string) (string, error)
}

// Worker owns the polling loop and the per-link pipeline.
type Worker struct {
	store   *storage.Store
	llm     llm.Backend
	fetch   Fetcher
	archive *archive.Store
	cfgWk   config.Worker
	cfgCh   config.Chunking
	cfgEx   config.Extract
	summary *summarize.Summarizer
	chunkFn func(string, chunking.Config) []string

	pollInterval time.Duration
	logger       *log.Logger
}

// Options bundles the optional knobs so callers don't have to re-pass
// every config for tests.
type Options struct {
	PollInterval time.Duration // default 2s
	Logger       *log.Logger
	Archive      *archive.Store // nil → archiving disabled regardless of config
}

func New(store *storage.Store, backend llm.Backend, fetcher Fetcher, cfg config.Config, opts Options) *Worker {
	if opts.PollInterval <= 0 {
		opts.PollInterval = 2 * time.Second
	}
	if opts.Logger == nil {
		opts.Logger = log.Default()
	}
	if opts.Archive == nil {
		opts.Archive, _ = archive.New("") // disabled no-op
	}
	return &Worker{
		store:        store,
		llm:          backend,
		fetch:        fetcher,
		archive:      opts.Archive,
		cfgWk:        cfg.Worker,
		cfgCh:        cfg.Chunking,
		cfgEx:        cfg.Extract,
		summary:      summarize.New(backend, summarize.Default()),
		chunkFn:      chunking.Chunk,
		pollInterval: opts.PollInterval,
		logger:       opts.Logger,
	}
}

// Run blocks until ctx is cancelled, polling for pending/needs-reindex links
// and processing them in batches of cfgWk.Concurrency.
func (w *Worker) Run(ctx context.Context) error {
	t := time.NewTicker(w.pollInterval)
	defer t.Stop()
	for {
		if err := w.tick(ctx); err != nil && !errors.Is(err, context.Canceled) {
			w.logger.Printf("worker tick: %v", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
		}
	}
}

// tick processes one batch — exposed for tests so we don't have to wait on the timer.
func (w *Worker) tick(ctx context.Context) error {
	// Pending fetch+extract.
	pending, err := w.store.ListLinksByStatus(ctx, storage.StatusPending, w.cfgWk.Concurrency*2)
	if err != nil {
		return fmt.Errorf("list pending: %w", err)
	}
	// Fetched but un-summarised — typical "needs reindex" state when the LLM
	// was down at first ingest. Process these once we're done with pending.
	fetched, err := w.store.ListLinksByStatus(ctx, storage.StatusFetched, w.cfgWk.Concurrency*2)
	if err != nil {
		return fmt.Errorf("list fetched: %w", err)
	}

	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(w.cfgWk.Concurrency)
	for i := range pending {
		l := pending[i]
		g.Go(func() error {
			w.processFetch(gctx, l)
			return nil
		})
	}
	for i := range fetched {
		l := fetched[i]
		g.Go(func() error {
			w.processIndex(gctx, l)
			return nil
		})
	}
	return g.Wait()
}

func (w *Worker) processFetch(ctx context.Context, l storage.Link) {
	body, err := w.fetch.Fetch(ctx, l.URL)
	if err != nil {
		w.logger.Printf("worker: fetch %d (%s): %v", l.ID, l.URL, err)
		_ = w.store.MarkLinkFailed(ctx, l.ID, truncErr(err))
		return
	}
	a, err := extract.Extract(body, l.URL)
	if err != nil {
		w.logger.Printf("worker: extract %d: %v", l.ID, err)
		_ = w.store.MarkLinkFailed(ctx, l.ID, truncErr(err))
		return
	}
	// Optional HTML archive (gzipped on disk, path persisted alongside).
	archivePath := ""
	if w.cfgEx.ArchiveHTML && w.archive.Enabled() && a.RawHTML != "" {
		if p, aerr := w.archive.Save(l.ID, a.RawHTML); aerr == nil {
			archivePath = p
		} else {
			w.logger.Printf("worker: archive %d: %v", l.ID, aerr)
		}
	}
	contentLang := lang.Detect(a.ContentMD)
	if err := w.store.UpdateLinkExtraction(ctx, l.ID, a.Title, a.Description, a.ImageURL, a.ContentMD, contentLang, archivePath); err != nil {
		w.logger.Printf("worker: persist extraction %d: %v", l.ID, err)
		return
	}
	// Continue straight into the index pass — saves a poll cycle on the happy path.
	got, err := w.store.GetLink(ctx, l.ID)
	if err == nil {
		w.processIndex(ctx, *got)
	}
}

func (w *Worker) processIndex(ctx context.Context, l storage.Link) {
	if l.ContentMD == "" {
		return // nothing to chunk; refetch needed
	}

	// 1) Chunk + insert.
	chunks := w.chunkFn(l.ContentMD, chunking.Config{
		TargetTokens:  w.cfgCh.TargetTokens,
		OverlapTokens: w.cfgCh.OverlapTokens,
		MinTokens:     w.cfgCh.MinTokens,
	})
	if len(chunks) == 0 {
		w.logger.Printf("worker: no chunks produced for %d", l.ID)
		return
	}
	chunkIDs, err := w.store.ReplaceChunks(ctx, l.ID, chunks)
	if err != nil {
		w.logger.Printf("worker: insert chunks %d: %v", l.ID, err)
		return
	}

	// 2) Embed all chunks. On embed failure we leave the link at `fetched`
	// so the next tick retries — the embeddings stay NULL, search falls back
	// to BM25, and the UI shows a "needs reindex" badge by virtue of
	// status != summarized.
	res, err := w.llm.Embed(ctx, chunks, &llm.EmbedOptions{BatchSize: w.cfgWk.EmbedBatchSize})
	if err != nil {
		w.logger.Printf("worker: embed %d: %v", l.ID, err)
		return
	}
	if len(res.Vectors) != len(chunkIDs) {
		w.logger.Printf("worker: embed length mismatch on %d: %d vs %d", l.ID, len(res.Vectors), len(chunkIDs))
		return
	}
	for i, id := range chunkIDs {
		if err := w.store.SetChunkEmbedding(ctx, id, embed.Encode(res.Vectors[i])); err != nil {
			w.logger.Printf("worker: persist embedding chunk %d: %v", id, err)
		}
	}

	// 3) Summarise + tag. Existing tags help the LLM bias toward reuse.
	existing, _ := w.store.ListTopTagSlugs(ctx, 50)
	sum, err := w.summary.Summarize(ctx, l.Title, l.ContentMD, existing)
	if err != nil {
		w.logger.Printf("worker: summarize %d: %v", l.ID, err)
		return
	}
	if err := w.store.UpdateLinkSummary(ctx, l.ID, sum.TLDR); err != nil {
		w.logger.Printf("worker: persist summary %d: %v", l.ID, err)
		return
	}
	for _, slug := range sum.Tags {
		tag, err := w.store.UpsertTag(ctx, slug, slug)
		if err != nil {
			w.logger.Printf("worker: upsert tag %q: %v", slug, err)
			continue
		}
		if err := w.store.AttachTag(ctx, l.ID, tag.ID, storage.TagSourceAuto); err != nil {
			w.logger.Printf("worker: attach tag %q: %v", slug, err)
		}
	}
}

// truncErr keeps the persisted error message short — full stack traces in
// fetch_error get unwieldy in HTML tooltips.
func truncErr(err error) string {
	const max = 200
	s := err.Error()
	if len(s) > max {
		return s[:max] + "…"
	}
	return s
}

// ProcessOne processes a single link by ID synchronously. Used by `linklore
// reindex --id N` and by tests that want deterministic behaviour without a
// timer-driven loop.
func (w *Worker) ProcessOne(ctx context.Context, id int64) error {
	l, err := w.store.GetLink(ctx, id)
	if err != nil {
		return err
	}
	switch l.Status {
	case storage.StatusPending, storage.StatusFailed:
		w.processFetch(ctx, *l)
	default:
		w.processIndex(ctx, *l)
	}
	return nil
}

