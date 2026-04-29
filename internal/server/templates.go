package server

import (
	"fmt"
	"html/template"
	"io/fs"

	"github.com/gabrielemastrapasqua/linklore/web"
)

// funcMap holds the template helpers shared across both the page tree and
// the partials tree. `dict` lets templates pass keyed args to {{template}}
// invocations without a wrapping struct on the Go side.
func funcMap() template.FuncMap {
	return template.FuncMap{
		"dict": func(values ...any) (map[string]any, error) {
			if len(values)%2 != 0 {
				return nil, fmt.Errorf("dict: odd number of args")
			}
			out := make(map[string]any, len(values)/2)
			for i := 0; i < len(values); i += 2 {
				k, ok := values[i].(string)
				if !ok {
					return nil, fmt.Errorf("dict: non-string key")
				}
				out[k] = values[i+1]
			}
			return out, nil
		},
		"list": func(values ...any) []any { return values },
		"add":  func(a, b int) int { return a + b },
	}
}

// renderer holds two template trees:
//   - pages: full pages (base + content + all partials)
//   - partials: HTMX fragments returned for partial swaps
//
// Splitting them avoids the "two contents" problem: the base template
// expects a {{template "content"}} block that fragment responses don't have.
type renderer struct {
	pages    map[string]*template.Template
	partials *template.Template
}

func newRenderer() (*renderer, error) {
	r := &renderer{pages: map[string]*template.Template{}}

	partialsFS, err := fs.Sub(web.Templates, "templates/partials")
	if err != nil {
		return nil, err
	}
	partialFiles, err := listGlob(partialsFS, "*.html")
	if err != nil {
		return nil, err
	}
	r.partials, err = template.New("partials").Funcs(funcMap()).ParseFS(partialsFS, partialFiles...)
	if err != nil {
		return nil, fmt.Errorf("parse partials: %w", err)
	}

	pageFiles, err := listGlob(web.Templates, "templates/*.html")
	if err != nil {
		return nil, err
	}
	for _, f := range pageFiles {
		// Each page template clones partials so it can use {{template "link_row"}}
		// without a second parse, then layers in base.html and the page itself.
		t, err := r.partials.Clone()
		if err != nil {
			return nil, err
		}
		t, err = t.ParseFS(web.Templates, "templates/base.html", f)
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", f, err)
		}
		// Index by file basename without extension (e.g. "collections").
		name := basename(f)
		r.pages[name] = t
	}
	return r, nil
}

func listGlob(src fs.FS, pattern string) ([]string, error) {
	matches, err := fs.Glob(src, pattern)
	if err != nil {
		return nil, err
	}
	if len(matches) == 0 {
		return nil, fmt.Errorf("no templates match %q", pattern)
	}
	return matches, nil
}

func basename(path string) string {
	// strip "templates/" prefix and ".html" suffix
	out := path
	if i := indexByte(out, '/'); i >= 0 {
		out = out[i+1:]
	}
	if n := len(out); n > 5 && out[n-5:] == ".html" {
		out = out[:n-5]
	}
	return out
}

func indexByte(s string, c byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == c {
			return i
		}
	}
	return -1
}
