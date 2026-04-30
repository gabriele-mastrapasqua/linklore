// Package urlnorm normalises URLs for duplicate detection. The
// canonical form drops the protocol, the leading "www.", any
// trailing slash, the fragment, and tracking querystring keys
// (utm_*, fbclid, gclid, ref, ref_src, mc_*, _hsenc, _hsmi).
package urlnorm

import (
	"net/url"
	"sort"
	"strings"
)

// trackerPrefixes are matched as case-insensitive querystring-key
// prefixes; any key starting with one of these is dropped.
var trackerPrefixes = []string{
	"utm_", "mc_", "_hs",
}

// trackerExact are exact (case-insensitive) querystring keys to drop.
var trackerExact = map[string]struct{}{
	"fbclid":           {},
	"gclid":            {},
	"yclid":            {},
	"msclkid":          {},
	"dclid":            {},
	"igshid":           {},
	"ref":              {},
	"ref_src":          {},
	"ref_url":          {},
	"source":           {},
	"si":               {},
	"feature":          {},
	"vero_id":          {},
	"vero_conv":        {},
	"_branch_match_id": {},
}

// Normalize returns a canonical key for raw. Bad input → "" so callers
// can short-circuit without crashing.
func Normalize(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return strings.ToLower(raw)
	}
	host := strings.ToLower(u.Host)
	host = strings.TrimPrefix(host, "www.")
	path := u.Path
	if path != "/" {
		path = strings.TrimRight(path, "/")
	}

	// Filter querystring.
	q := u.Query()
	for k := range q {
		lk := strings.ToLower(k)
		if _, hit := trackerExact[lk]; hit {
			delete(q, k)
			continue
		}
		for _, pref := range trackerPrefixes {
			if strings.HasPrefix(lk, pref) {
				delete(q, k)
				break
			}
		}
	}
	// Re-encode with sorted keys so the canonical form is stable.
	var qs string
	if len(q) > 0 {
		keys := make([]string, 0, len(q))
		for k := range q {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var b strings.Builder
		for i, k := range keys {
			vs := q[k]
			sort.Strings(vs)
			for j, v := range vs {
				if i > 0 || j > 0 {
					b.WriteByte('&')
				}
				b.WriteString(url.QueryEscape(k))
				b.WriteByte('=')
				b.WriteString(url.QueryEscape(v))
			}
		}
		qs = "?" + b.String()
	}
	return host + path + qs
}
