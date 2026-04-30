// Package netscape parses and writes Netscape Bookmark Files —
// the lingua franca that every browser (Chrome / Firefox / Safari /
// Edge) and most bookmark managers (Pocket / Pinboard / Raindrop /
// Linkding / Mymind) export to and import from.
//
// The format is a tag-soup of nested <DT> + <DL> + <A> elements; we
// don't try to preserve the folder hierarchy beyond capturing the
// nearest enclosing <H3> as a "folder" name (used as the destination
// collection when the importer doesn't override it).
package netscape

import (
	"fmt"
	"html"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
)

// Bookmark is one row from a Netscape file. Title and URL are the
// only required fields; everything else is best-effort.
type Bookmark struct {
	URL         string
	Title       string
	Description string
	Tags        []string
	Folder      string    // nearest <H3> ancestor (e.g. "Toolbar/News")
	AddedAt     time.Time // from ADD_DATE attribute (Unix epoch)
}

// Parse reads a Netscape bookmark file from r and returns every <A>
// it finds, with the nearest-ancestor <H3> chain captured as Folder.
// Bad input returns nil, error.
func Parse(r io.Reader) ([]Bookmark, error) {
	doc, err := goquery.NewDocumentFromReader(r)
	if err != nil {
		return nil, fmt.Errorf("parse netscape html: %w", err)
	}
	var out []Bookmark
	doc.Find("a").Each(func(_ int, sel *goquery.Selection) {
		href, _ := sel.Attr("href")
		href = strings.TrimSpace(href)
		if href == "" {
			return
		}
		b := Bookmark{
			URL:    href,
			Title:  strings.TrimSpace(sel.Text()),
			Folder: collectFolder(sel),
		}
		if v, ok := sel.Attr("add_date"); ok {
			if n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64); err == nil && n > 0 {
				b.AddedAt = time.Unix(n, 0).UTC()
			}
		}
		if v, ok := sel.Attr("tags"); ok {
			for _, t := range strings.Split(v, ",") {
				if t = strings.TrimSpace(t); t != "" {
					b.Tags = append(b.Tags, t)
				}
			}
		}
		// <DD> sibling carries the description in the canonical format.
		if dd := sel.Parent().Next(); dd.Length() > 0 && strings.EqualFold(dd.Get(0).Data, "dd") {
			b.Description = strings.TrimSpace(dd.Text())
		}
		out = append(out, b)
	})
	return out, nil
}

// collectFolder walks up the DOM looking for <H3> headers (folder
// labels in the Netscape spec). Returns "/"-joined path or "" if none.
//
// goquery's HTML parser nests Netscape's malformed tag soup as:
//
//	<dt>           ← folder DT, contains <h3>"Folder"</h3>
//	  <dl>         ← inner DL
//	    <dt>       ← link DT
//	      <a/>     ← the link itself
//	    </dt>
//	  </dl>
//	</dt>
//
// So for each <a>, walk up Parent (DT) → Parent (DL) → Parent (folder DT
// or body), and grab the <h3> child of each "folder DT" we encounter.
func collectFolder(sel *goquery.Selection) string {
	var parts []string
	dl := sel.ParentsFiltered("dl").First()
	for dl.Length() > 0 {
		dt := dl.Parent() // either the folder <dt> or <body>
		if dt.Length() == 0 {
			break
		}
		if dt.Get(0).Data == "dt" {
			h3 := dt.ChildrenFiltered("h3").First()
			if h3.Length() > 0 {
				parts = append([]string{strings.TrimSpace(h3.Text())}, parts...)
			}
		}
		// Step up to the next enclosing <dl>.
		dl = dt.ParentsFiltered("dl").First()
	}
	return strings.Join(parts, "/")
}

// WriteEntry is the input shape Write expects: minimum URL + Title,
// optional folder/tags/description.
type WriteEntry struct {
	URL         string
	Title       string
	Description string
	Tags        []string
	Folder      string
	AddedAt     time.Time
}

// Write emits a Netscape Bookmark File for entries grouped by Folder.
// Folder ordering is preserved by the order of first appearance.
func Write(w io.Writer, entries []WriteEntry) error {
	if _, err := io.WriteString(w, header); err != nil {
		return err
	}
	groups := groupByFolder(entries)
	for _, g := range groups {
		if g.Folder != "" {
			fmt.Fprintf(w, "    <DT><H3>%s</H3>\n    <DL><p>\n", html.EscapeString(g.Folder))
		}
		for _, e := range g.Entries {
			fmt.Fprint(w, "        <DT><A HREF=\"")
			io.WriteString(w, html.EscapeString(e.URL))
			fmt.Fprint(w, "\"")
			if !e.AddedAt.IsZero() {
				fmt.Fprintf(w, " ADD_DATE=\"%d\"", e.AddedAt.Unix())
			}
			if len(e.Tags) > 0 {
				fmt.Fprintf(w, " TAGS=\"%s\"", html.EscapeString(strings.Join(e.Tags, ",")))
			}
			fmt.Fprint(w, ">")
			title := e.Title
			if title == "" {
				title = e.URL
			}
			io.WriteString(w, html.EscapeString(title))
			fmt.Fprint(w, "</A>\n")
			if e.Description != "" {
				fmt.Fprintf(w, "        <DD>%s\n", html.EscapeString(e.Description))
			}
		}
		if g.Folder != "" {
			io.WriteString(w, "    </DL><p>\n")
		}
	}
	io.WriteString(w, footer)
	return nil
}

const header = `<!DOCTYPE NETSCAPE-Bookmark-file-1>
<META HTTP-EQUIV="Content-Type" CONTENT="text/html; charset=UTF-8">
<TITLE>Bookmarks</TITLE>
<H1>Bookmarks</H1>
<DL><p>
`

const footer = `</DL><p>
`

type folderGroup struct {
	Folder  string
	Entries []WriteEntry
}

func groupByFolder(entries []WriteEntry) []folderGroup {
	idx := map[string]int{}
	var out []folderGroup
	for _, e := range entries {
		i, ok := idx[e.Folder]
		if !ok {
			out = append(out, folderGroup{Folder: e.Folder})
			i = len(out) - 1
			idx[e.Folder] = i
		}
		out[i].Entries = append(out[i].Entries, e)
	}
	return out
}
