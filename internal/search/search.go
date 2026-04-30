// Package search runs hybrid retrieval (FTS5 + cosine) over linklore data.
//
// Two flavours, both grouped by link:
//
//   - SearchLinks: user-facing search bar / /search page. Unions FTS hits
//     from links_fts (link-level) and chunks_fts (chunk-level), re-ranks
//     by cosine on the embedding side, picks max-score chunk per link,
//     and returns the top-N links with a snippet.
//
//   - RetrieveChunks: chat / RAG context builder. Returns the top-K chunks
//     in a collection — already deduped per link if requested — with their
//     hybrid scores.
//
// Vector re-rank is skipped automatically when the LLM backend is unavailable
// (caller passes a nil embedder); the result then falls back to BM25.
package search

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/gabriele-mastrapasqua/linklore/internal/embed"
	"github.com/gabriele-mastrapasqua/linklore/internal/llm"
	"github.com/gabriele-mastrapasqua/linklore/internal/storage"
)

// Embedder is the subset of llm.Backend the search uses.
type Embedder interface {
	Embed(ctx context.Context, texts []string, opts *llm.EmbedOptions) (*llm.EmbedResult, error)
}

// Engine wires the store + (optional) embedder together.
type Engine struct {
	store *storage.Store
	embed Embedder // may be nil → BM25-only fallback
}

func New(store *storage.Store, e Embedder) *Engine {
	return &Engine{store: store, embed: e}
}

// LinkResult is what /search returns for one link.
type LinkResult struct {
	Link    *storage.Link
	Score   float64 // higher = better
	Snippet string  // best matching FTS snippet
}

// ChunkHit is what RAG returns: a chunk with link metadata + hybrid score.
type ChunkHit struct {
	Chunk *storage.Chunk
	Link  *storage.Link
	Score float64
}

// SearchLinks runs hybrid search across an optional collection. limit caps
// the final returned link count (default 20).
func (e *Engine) SearchLinks(ctx context.Context, query string, collectionID int64, limit int) ([]LinkResult, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, nil
	}
	if limit <= 0 {
		limit = 20
	}
	q := sanitizeMatchQuery(query)

	linkHits, err := e.store.SearchLinksFTS(ctx, q, 50)
	if err != nil {
		return nil, fmt.Errorf("links fts: %w", err)
	}
	chunkHits, err := e.store.SearchChunksFTS(ctx, q, collectionID, 50)
	if err != nil {
		return nil, fmt.Errorf("chunks fts: %w", err)
	}

	// Tag matches: any link whose tag slug or name prefix-matches the query
	// gets a small synthetic FTS score so it surfaces alongside text hits.
	// Use the original (un-sanitised) query so users can search "go" or
	// "machine-learning" and hit the tag directly.
	tagLinkIDs, err := e.store.SearchLinksByTagPrefix(ctx, query, 50)
	if err != nil {
		return nil, fmt.Errorf("tag prefix: %w", err)
	}

	// Group BM25 scores per link: keep the best (lowest BM25 → biggest signal).
	type agg struct {
		bestBM25 float64 // smallest seen → best
		snippet  string
	}
	byLink := map[int64]*agg{}
	consider := func(linkID int64, bm25 float64, snip string) {
		a, ok := byLink[linkID]
		if !ok || bm25 < a.bestBM25 {
			byLink[linkID] = &agg{bestBM25: bm25, snippet: snip}
		}
	}
	for _, h := range linkHits {
		consider(h.LinkID, h.BM25, h.Snippet)
	}
	for _, h := range chunkHits {
		consider(h.LinkID, h.BM25, h.Snippet)
	}
	// Tag matches don't have a real BM25 score; assign a soft constant so
	// they sit alongside genuine text hits without dominating them.
	const tagSyntheticBM25 = -2.0
	for _, id := range tagLinkIDs {
		consider(id, tagSyntheticBM25, "matched via tag: "+query)
	}
	if len(byLink) == 0 {
		return nil, nil
	}

	// Bring back the bm25-best links first, then optionally re-rank the top
	// of that list with cosine on the chunk side.
	type linkScore struct {
		id    int64
		score float64
		snip  string
	}
	scored := make([]linkScore, 0, len(byLink))
	for id, a := range byLink {
		// FTS5 BM25 is "smaller is better"; flip to a "bigger is better"
		// score so we can later average with cosine on the same scale.
		scored = append(scored, linkScore{id: id, score: -a.bestBM25, snip: a.snippet})
	}
	sort.Slice(scored, func(i, j int) bool { return scored[i].score > scored[j].score })

	// Optional vector re-rank: only on the top 30 by BM25 to keep cost bounded.
	if e.embed != nil {
		const topForRerank = 30
		if len(scored) > topForRerank {
			scored = scored[:topForRerank]
		}
		linkIDs := make([]int64, len(scored))
		for i, s := range scored {
			linkIDs[i] = s.id
		}
		boost, err := e.cosineRerank(ctx, query, linkIDs, collectionID)
		if err == nil && len(boost) > 0 {
			for i := range scored {
				if v, ok := boost[scored[i].id]; ok {
					// 50/50 blend between flipped-BM25 (already in scored.score)
					// and cosine. BM25 was ~[-30, 0]; cosine is [-1, 1].
					// Normalise BM25 with a soft squash before blending.
					normBM25 := scored[i].score / 30
					scored[i].score = 0.5*normBM25 + 0.5*float64(v)
				}
			}
			sort.Slice(scored, func(i, j int) bool { return scored[i].score > scored[j].score })
		}
	}

	if len(scored) > limit {
		scored = scored[:limit]
	}

	out := make([]LinkResult, 0, len(scored))
	for _, s := range scored {
		link, err := e.store.GetLink(ctx, s.id)
		if err != nil {
			if errors.Is(err, storage.ErrNotFound) {
				continue
			}
			return nil, err
		}
		out = append(out, LinkResult{Link: link, Score: s.score, Snippet: s.snip})
	}
	return out, nil
}

// RetrieveChunks is the RAG context builder. Returns top-K chunks across
// the collection scored by hybrid (BM25 ∪ cosine) ranking.
//
// dedupePerLink: when true, no link contributes more than one chunk, so the
// model sees diverse sources. False keeps multiple chunks from the same link
// when the link is very on-topic.
func (e *Engine) RetrieveChunks(ctx context.Context, query string, collectionID int64, k int, dedupePerLink bool) ([]ChunkHit, error) {
	query = strings.TrimSpace(query)
	if query == "" || k <= 0 {
		return nil, nil
	}
	q := sanitizeMatchQuery(query)

	ftsHits, err := e.store.SearchChunksFTS(ctx, q, collectionID, 50)
	if err != nil {
		return nil, err
	}

	scores := map[int64]float64{}
	for _, h := range ftsHits {
		// flipped-BM25 → big = good; squash by /30 same as SearchLinks.
		scores[h.ChunkID] = -h.BM25 / 30
	}

	// Vector pass over all embedded chunks in the collection (or just the
	// FTS candidates if embedder absent).
	if e.embed != nil {
		embRes, err := e.embed.Embed(ctx, []string{query}, &llm.EmbedOptions{BatchSize: 1})
		if err == nil && len(embRes.Vectors) == 1 {
			qv := embRes.Vectors[0]
			chunks, err := e.store.ListChunksByCollection(ctx, collectionID)
			if err != nil {
				return nil, err
			}
			for _, c := range chunks {
				if len(c.Embedding) == 0 {
					continue
				}
				v, derr := embed.Decode(c.Embedding)
				if derr != nil {
					continue
				}
				cos, cerr := embed.Cosine(qv, v)
				if cerr != nil {
					continue
				}
				prev := scores[c.ID]
				// 50/50 blend with cosine. Chunks not in FTS still score on cosine alone.
				scores[c.ID] = 0.5*prev + 0.5*float64(cos)
			}
		}
	}
	if len(scores) == 0 {
		return nil, nil
	}

	type pair struct {
		id    int64
		score float64
	}
	ranked := make([]pair, 0, len(scores))
	for id, sc := range scores {
		ranked = append(ranked, pair{id, sc})
	}
	sort.Slice(ranked, func(i, j int) bool { return ranked[i].score > ranked[j].score })

	if !dedupePerLink && len(ranked) > k {
		ranked = ranked[:k]
	}

	ids := make([]int64, len(ranked))
	for i, p := range ranked {
		ids[i] = p.id
	}
	chunks, err := e.store.GetChunksByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	chunkByID := make(map[int64]*storage.Chunk, len(chunks))
	for i := range chunks {
		chunkByID[chunks[i].ID] = &chunks[i]
	}
	linkCache := map[int64]*storage.Link{}

	out := make([]ChunkHit, 0, k)
	seenLink := map[int64]struct{}{}
	for _, p := range ranked {
		c := chunkByID[p.id]
		if c == nil {
			continue
		}
		if dedupePerLink {
			if _, dup := seenLink[c.LinkID]; dup {
				continue
			}
			seenLink[c.LinkID] = struct{}{}
		}
		l, ok := linkCache[c.LinkID]
		if !ok {
			fetched, err := e.store.GetLink(ctx, c.LinkID)
			if err != nil {
				continue
			}
			l = fetched
			linkCache[c.LinkID] = l
		}
		out = append(out, ChunkHit{Chunk: c, Link: l, Score: p.score})
		if len(out) >= k {
			break
		}
	}
	return out, nil
}

// cosineRerank returns linkID → max cosine across that link's chunks.
// Bounded to the supplied linkIDs so we don't scan the whole collection.
func (e *Engine) cosineRerank(ctx context.Context, query string, linkIDs []int64, collectionID int64) (map[int64]float32, error) {
	embRes, err := e.embed.Embed(ctx, []string{query}, &llm.EmbedOptions{BatchSize: 1})
	if err != nil {
		return nil, err
	}
	if len(embRes.Vectors) != 1 {
		return nil, errors.New("expected 1 query vector")
	}
	qv := embRes.Vectors[0]
	wanted := make(map[int64]struct{}, len(linkIDs))
	for _, id := range linkIDs {
		wanted[id] = struct{}{}
	}
	chunks, err := e.store.ListChunksByCollection(ctx, collectionID)
	if err != nil {
		return nil, err
	}
	out := map[int64]float32{}
	for _, c := range chunks {
		if _, ok := wanted[c.LinkID]; !ok {
			continue
		}
		if len(c.Embedding) == 0 {
			continue
		}
		v, derr := embed.Decode(c.Embedding)
		if derr != nil {
			continue
		}
		cos, cerr := embed.Cosine(qv, v)
		if cerr != nil {
			continue
		}
		if prev, ok := out[c.LinkID]; !ok || cos > prev {
			out[c.LinkID] = cos
		}
	}
	return out, nil
}

// sanitizeMatchQuery converts a free-form user query into a safe FTS5
// MATCH expression. Three things matter:
//
//  1. Strip every FTS5 syntax character so a stray quote/paren/dash
//     can't blow up the parser. Same dumb-but-safe approach as before.
//  2. Append "*" to every token for prefix matching: "bit" must hit
//     a chunk containing "bitnet". FTS5 only does prefix search when
//     the query says so explicitly, otherwise it's exact-token-only.
//  3. Join the prefix terms with OR. FTS5's default operator between
//     terms is AND, which is the wrong default for natural questions
//     ("bitnet spiega", "what is rust ownership") — a single rare word
//     in the question torpedoes the whole match.
//
// Common words ("a", "the", "che", …) aren't a problem in practice
// because BM25 ranks the rarer term high anyway. Tokens of length 1
// keep the "*" too — that's expected when a user types a single letter.
func sanitizeMatchQuery(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	bad := []string{`"`, "'", "(", ")", "*", ":", "^", "[", "]", "+", "-", "?", "!", ";", ",", ".", "/", "\\"}
	for _, ch := range bad {
		s = strings.ReplaceAll(s, ch, " ")
	}
	var toks []string
	for _, t := range strings.Fields(s) {
		if t == "" {
			continue
		}
		// Prefix-match every term so "bit" matches "bitnet" / "bitcoin"
		// the same way LIKE 'bit%' would. The user's mental model is
		// "search-as-you-type", not "exact whole-word match".
		toks = append(toks, t+"*")
	}
	if len(toks) == 0 {
		return ""
	}
	return strings.Join(toks, " OR ")
}
