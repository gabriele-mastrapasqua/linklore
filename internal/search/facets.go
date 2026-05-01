package search

import (
	"context"
	"strings"

	"github.com/gabriele-mastrapasqua/linklore/internal/storage"
)

// Facets are filters extracted from the user's query string. They run
// as post-filters on the FTS hit list — single-user scale (linklore
// caps out at low tens of thousands of links) so an in-memory filter
// pass is cheap and avoids growing the SQL surface.
//
// Supported syntax (mirrored in docs/search.md):
//
//	tag:foo      → must have tag with slug or name "foo"
//	-tag:foo     → must NOT have tag "foo"
//	kind:video   → kind == "video" (one of the storage.Kind* constants)
//	in:title     → restrict text matching to title (sets Scope=ScopeTitle)
//	in:url       → restrict text matching to URL   (sets Scope=ScopeURL)
//
// Multiple facets compose with AND. Anything that doesn't parse as a
// facet stays in the residual text query, which is what FTS sees.
type Facets struct {
	Tags    []string // include — link must have all of these
	NotTags []string // exclude — link must have none of these
	Kind    string   // empty = any
	Scope   string   // "", "title", "url"
}

// ScopeTitle / ScopeURL are valid values for Facets.Scope.
const (
	ScopeTitle = "title"
	ScopeURL   = "url"
)

// ParseFacets pulls facet pairs out of the query string and returns
// (residualText, facets). Order is preserved in the residual so users
// can quote phrases around facet pairs without fearing token reorder.
func ParseFacets(query string) (string, Facets) {
	var f Facets
	var keep []string
	for raw := range strings.FieldsSeq(query) {
		neg := false
		tok := raw
		if strings.HasPrefix(tok, "-") {
			neg = true
			tok = tok[1:]
		}
		colon := strings.IndexByte(tok, ':')
		if colon <= 0 || colon == len(tok)-1 {
			// Not a facet token — keep verbatim (with the leading dash if
			// present; FTS5 treats it as a NOT, which preserves user intent).
			keep = append(keep, raw)
			continue
		}
		key := strings.ToLower(tok[:colon])
		val := strings.ToLower(tok[colon+1:])
		switch key {
		case "tag":
			if neg {
				f.NotTags = append(f.NotTags, val)
			} else {
				f.Tags = append(f.Tags, val)
			}
		case "kind":
			// Only set on positive facet; -kind: would mean "not this kind"
			// which we don't currently model. Keep raw token then.
			if !neg {
				f.Kind = val
			} else {
				keep = append(keep, raw)
			}
		case "in":
			if !neg && (val == ScopeTitle || val == ScopeURL) {
				f.Scope = val
			} else {
				keep = append(keep, raw)
			}
		default:
			keep = append(keep, raw)
		}
	}
	return strings.Join(keep, " "), f
}

// Empty reports whether no facet was parsed; callers can skip the
// post-filter pass entirely on common bare-text queries.
func (f Facets) Empty() bool {
	return f.Kind == "" && f.Scope == "" && len(f.Tags) == 0 && len(f.NotTags) == 0
}

// Apply returns the subset of `in` that satisfies the facet predicates.
// `linkTags` maps link ID → set of tag slugs/names (lowercased) so the
// caller can build it once and reuse across searches.
func (f Facets) Apply(in []LinkResult, linkTags map[int64]map[string]struct{}) []LinkResult {
	if f.Empty() {
		return in
	}
	out := in[:0]
	for _, r := range in {
		if r.Link == nil {
			continue
		}
		if f.Kind != "" && r.Link.Kind != f.Kind {
			continue
		}
		if len(f.Tags) > 0 || len(f.NotTags) > 0 {
			tags := linkTags[r.Link.ID]
			ok := true
			for _, t := range f.Tags {
				if _, has := tags[t]; !has {
					ok = false
					break
				}
			}
			if !ok {
				continue
			}
			bad := false
			for _, t := range f.NotTags {
				if _, has := tags[t]; has {
					bad = true
					break
				}
			}
			if bad {
				continue
			}
		}
		// Scope filter (in:title / in:url) is checked at the engine layer
		// because we'd need the FTS column-level info to do it correctly.
		// For now this is a hint the handler can use to issue a tighter
		// query. We intentionally don't enforce here.
		out = append(out, r)
	}
	return out
}

// BuildLinkTags loads the tags for each result in one query per link
// and returns a map indexed by link ID. Set values are lowercased slug
// *and* display name so users can write either form. Single-user scale
// — at low tens of thousands of links, one query per result is fine.
func BuildLinkTags(ctx context.Context, s *storage.Store, results []LinkResult) (map[int64]map[string]struct{}, error) {
	if len(results) == 0 {
		return nil, nil
	}
	out := make(map[int64]map[string]struct{}, len(results))
	for _, r := range results {
		if r.Link == nil {
			continue
		}
		tags, err := s.ListTagsByLink(ctx, r.Link.ID)
		if err != nil {
			return nil, err
		}
		set := make(map[string]struct{}, 2*len(tags))
		for _, t := range tags {
			set[strings.ToLower(t.Slug)] = struct{}{}
			set[strings.ToLower(t.Name)] = struct{}{}
		}
		out[r.Link.ID] = set
	}
	return out, nil
}
