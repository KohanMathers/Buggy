package server

import (
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/kohanmathers/buggy/internal/markdown"
	"github.com/kohanmathers/buggy/internal/store"
)

type atomFeed struct {
	XMLName xml.Name    `xml:"http://www.w3.org/2005/Atom feed"`
	Title   string      `xml:"title"`
	ID      string      `xml:"id"`
	Updated string      `xml:"updated"`
	Link    []atomLink  `xml:"link"`
	Entries []atomEntry `xml:"entry"`
}

type atomLink struct {
	Href string `xml:"href,attr"`
	Rel  string `xml:"rel,attr,omitempty"`
}

type atomEntry struct {
	Title   string   `xml:"title"`
	ID      string   `xml:"id"`
	Updated string   `xml:"updated"`
	Link    atomLink `xml:"link"`
	Author  struct {
		Name string `xml:"name"`
	} `xml:"author"`
	Summary string `xml:"summary"`
}

func ts(t int64) string { return time.Unix(t, 0).UTC().Format(time.RFC3339) }

func (s *Server) writeFeed(w http.ResponseWriter, title, self string, entries []atomEntry) {
	f := atomFeed{Title: title, ID: self, Updated: time.Now().UTC().Format(time.RFC3339),
		Link: []atomLink{{Href: self, Rel: "self"}}, Entries: entries}
	if len(entries) > 0 {
		f.Updated = entries[0].Updated
	}
	w.Header().Set("Content-Type", "application/atom+xml; charset=utf-8")
	io.WriteString(w, xml.Header)
	enc := xml.NewEncoder(w)
	enc.Indent("", "  ")
	enc.Encode(f)
}

func (s *Server) issueEntries(issues []*store.Issue, created bool) []atomEntry {
	base := s.app.Cfg().Instance.URL
	var out []atomEntry
	for _, i := range issues {
		e := atomEntry{Title: "[" + i.Key + "] " + i.Title, ID: base + "/issues/" + i.Key,
			Link: atomLink{Href: base + "/issues/" + i.Key}, Summary: markdown.Excerpt(i.Description, 500)}
		e.Updated = ts(i.UpdatedAt)
		if created {
			e.Updated = ts(i.CreatedAt)
		}
		if i.Reporter != nil {
			e.Author.Name = i.Reporter.Username
		}
		out = append(out, e)
	}
	return out
}

func (s *Server) feedSearch(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	if !strings.Contains(q, "sort:") {
		q += " sort:updated"
	}
	res := s.app.Search(r.Context(), actorFrom(r), q, 1, 50)
	s.writeFeed(w, s.app.Cfg().Instance.Name+": "+q, s.app.Cfg().Instance.URL+r.URL.RequestURI(), s.issueEntries(res.Issues, false))
}

func (s *Server) feedProject(w http.ResponseWriter, r *http.Request) {
	key := strings.ToUpper(strings.TrimSuffix(r.PathValue("key"), ".atom"))
	if s.app.Cfg().Project(key) == nil {
		http.NotFound(w, r)
		return
	}
	res := s.app.Search(r.Context(), actorFrom(r), "project:"+key+" sort:created", 1, 50)
	s.writeFeed(w, s.app.Cfg().Instance.Name+": new in "+key, s.app.Cfg().Instance.URL+r.URL.RequestURI(), s.issueEntries(res.Issues, true))
}

func (s *Server) feedIssue(w http.ResponseWriter, r *http.Request) {
	key := strings.TrimSuffix(r.PathValue("key"), ".atom")
	act := actorFrom(r)
	iss, err := s.app.GetIssue(r.Context(), act, key)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	base := s.app.Cfg().Instance.URL
	comments, _ := s.app.Comments(r.Context(), act, iss)
	var entries []atomEntry
	for i := len(comments) - 1; i >= 0; i-- {
		c := comments[i]
		if c.Deleted || c.Moderation != "approved" {
			continue
		}
		e := atomEntry{Title: fmt.Sprintf("Comment by %s on %s", c.Author.Name(), iss.Key),
			ID: fmt.Sprintf("%s/issues/%s#comment-%d", base, iss.Key, c.ID), Updated: ts(c.CreatedAt),
			Link: atomLink{Href: fmt.Sprintf("%s/issues/%s#comment-%d", base, iss.Key, c.ID)}, Summary: markdown.Excerpt(c.Body, 1000)}
		e.Author.Name = c.Author.Username
		entries = append(entries, e)
	}
	s.writeFeed(w, "["+iss.Key+"] "+iss.Title, base+r.URL.RequestURI(), entries)
}

func (s *Server) gitHook(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 10<<20))
	if err != nil {
		writeJSON(w, 400, map[string]any{"error": "could not read body"})
		return
	}
	res, err := s.app.HandleGitHook(r.Context(), r.PathValue("id"), r.Header, body)
	if err != nil {
		errJSON(w, err)
		return
	}
	writeJSON(w, 200, res)
}
