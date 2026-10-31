package server

import (
	"bytes"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html/template"
	"io/fs"
	"math"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"
	"time"

	"github.com/kohanmathers/buggy/internal/app"
	"github.com/kohanmathers/buggy/internal/config"
	"github.com/kohanmathers/buggy/internal/markdown"
	"github.com/kohanmathers/buggy/internal/store"
)

type configT = config.Config

type tmplEntry struct{ t *template.Template }

type overlayFS struct{ top, base fs.FS }

func (o overlayFS) Open(name string) (fs.File, error) {
	if f, err := o.top.Open(name); err == nil {
		return f, nil
	}
	return o.base.Open(name)
}

func (s *Server) webFS() fs.FS {
	cfg := s.app.Cfg()
	base := s.embedFS
	if cfg.Server.Dev {
		base = os.DirFS(cfg.Server.WebDir)
	}
	if cfg.Server.ThemeDir != "" {
		return overlayFS{top: os.DirFS(cfg.Server.ThemeDir), base: base}
	}
	return base
}

func (s *Server) clearTemplates() {
	s.tmplMu.Lock()
	s.tmplCache = map[string]*tmplEntry{}
	s.assetHashes = map[string]string{}
	s.tmplMu.Unlock()
}

func cssValue(v string) string {
	return strings.Map(func(r rune) rune {
		if strings.ContainsRune("<>{};\\\n", r) {
			return -1
		}
		return r
	}, v)
}

func (s *Server) themeCSS() template.HTML {
	th := s.app.Cfg().Instance.Theme
	var b strings.Builder
	if th.FontURL != "" {
		fmt.Fprintf(&b, `<link rel="stylesheet" href="%s">`, template.HTMLEscapeString(th.FontURL))
	}
	b.WriteString("<style>:root{")
	decl := func(name, v string) {
		if v != "" {
			fmt.Fprintf(&b, "%s:%s;", name, v)
		}
	}
	if config.ColorRe.MatchString(th.Primary) {
		decl("--primary", th.Primary)
	}
	if config.ColorRe.MatchString(th.Accent) {
		decl("--accent", th.Accent)
	}
	decl("--radius", cssValue(th.Radius))
	decl("--font", cssValue(th.Font))
	decl("--mono", cssValue(th.MonoFont))
	decl("--font-size", cssValue(th.FontSize))
	b.WriteString("}")
	vars := func(m map[string]string) string {
		var vb strings.Builder
		for _, k := range store.SortedKeys(m) {
			if name, ok := config.ThemeColors[k]; ok && config.ColorRe.MatchString(m[k]) {
				fmt.Fprintf(&vb, "%s:%s;", name, m[k])
			}
		}
		return vb.String()
	}
	if v := vars(th.Colors); v != "" {
		fmt.Fprintf(&b, `:root[data-theme="light"]{%s}@media (prefers-color-scheme: light){:root[data-theme="auto"]{%s}}`, v, v)
	}
	if v := vars(th.DarkColors); v != "" {
		fmt.Fprintf(&b, `:root[data-theme="dark"]{%s}@media (prefers-color-scheme: dark){:root[data-theme="auto"]{%s}}`, v, v)
	}
	b.WriteString("</style>")
	return template.HTML(b.String())
}

func (s *Server) assetURL(name string) string {
	dev := s.app.Cfg().Server.Dev
	s.tmplMu.Lock()
	h, ok := s.assetHashes[name]
	s.tmplMu.Unlock()
	if !ok || dev {
		b, err := fs.ReadFile(s.webFS(), name)
		if err != nil {
			return "/" + name
		}
		sum := sha1.Sum(b)
		h = hex.EncodeToString(sum[:5])
		s.tmplMu.Lock()
		s.assetHashes[name] = h
		s.tmplMu.Unlock()
	}
	return "/" + name + "?v=" + h
}

func safeColor(c string) template.CSS {
	if config.ColorRe.MatchString(c) {
		return template.CSS(c)
	}
	return template.CSS("#888")
}

func (s *Server) funcs() template.FuncMap {
	return template.FuncMap{
		"md": func(src string) template.HTML {
			cfg := s.app.Cfg()
			return markdown.Render(src, func(k string) bool { return cfg.Project(k) != nil })
		},
		"excerpt": markdown.Excerpt,
		"ago":     ago,
		"date": func(ts int64) string {
			if ts == 0 {
				return ""
			}
			return time.Unix(ts, 0).UTC().Format("2006-01-02")
		},
		"asset":         s.assetURL,
		"themeCSS":      s.themeCSS,
		"explain":       s.app.ExplainExample,
		"forever":       func(ts int64) bool { return ts == store.Forever },
		"defaultAvatar": func() string { return s.app.Cfg().Instance.DefaultAvatar },
		"jdate": func(ts int64) string {
			if ts == 0 {
				return ""
			}
			return time.Unix(ts, 0).UTC().Format("02/Jan/06")
		},
		"datetime": func(ts int64) string { return time.Unix(ts, 0).UTC().Format("2006-01-02 15:04 UTC") },
		"iso":      func(ts int64) string { return time.Unix(ts, 0).UTC().Format(time.RFC3339) },
		"add":      func(a, b int) int { return a + b },
		"sub":      func(a, b int) int { return a - b },
		"mul":      func(a, b int) int { return a * b },
		"add64":    func(a, b int64) int64 { return a + b },
		"pct": func(a, b int) int {
			if b == 0 {
				return 0
			}
			return int(math.Round(float64(a) * 100 / float64(b)))
		},
		"join":     strings.Join,
		"lower":    strings.ToLower,
		"upper":    strings.ToUpper,
		"contains": func(list []string, v string) bool { return contains(list, v) },
		"color":    safeColor,
		"cssval": func(v string) template.CSS {
			return template.CSS(strings.Map(func(r rune) rune {
				if strings.ContainsRune("<>{};\\\n", r) {
					return -1
				}
				return r
			}, v))
		},
		"json": func(v any) template.JS {
			b, _ := json.Marshal(v)
			return template.JS(b)
		},
		"dict": func(kv ...any) map[string]any {
			m := map[string]any{}
			for i := 0; i+1 < len(kv); i += 2 {
				m[fmt.Sprint(kv[i])] = kv[i+1]
			}
			return m
		},
		"list": func(v ...string) []string { return v },
		"seq": func(from, to int) []int {
			var out []int
			for i := from; i <= to; i++ {
				out = append(out, i)
			}
			return out
		},
		"bytes": func(v any) string {
			switch n := v.(type) {
			case int64:
				return humanBytes(n)
			case config.ByteSize:
				return humanBytes(int64(n))
			case int:
				return humanBytes(int64(n))
			}
			return fmt.Sprint(v)
		},
		"userURL":  userURL,
		"initials": initials,
		"avatarColor": func(name string) template.CSS {
			var h uint32
			for _, c := range name {
				h = h*31 + uint32(c)
			}
			return template.CSS(fmt.Sprintf("hsl(%d, 55%%, 50%%)", h%360))
		},
		"isImage": func(ct string) bool { return strings.HasPrefix(ct, "image/") && !strings.Contains(ct, "svg") },
		"isText":  func(ct string) bool { return strings.HasPrefix(ct, "text/") },
		"linkLabel": func(kind string, outgoing bool) string {
			if l, ok := app.LinkKinds[kind]; ok {
				if outgoing {
					return l[0]
				}
				return l[1]
			}
			return kind
		},
		"qs": func(q url.Values, kv ...string) template.URL {
			c := url.Values{}
			for k, v := range q {
				c[k] = v
			}
			for i := 0; i+1 < len(kv); i += 2 {
				if kv[i+1] == "" {
					c.Del(kv[i])
				} else {
					c.Set(kv[i], kv[i+1])
				}
			}
			return template.URL(c.Encode())
		},
		"pathEscape":  url.PathEscape,
		"queryEscape": url.QueryEscape,
		"firstLine": func(s string) string {
			l, _, _ := strings.Cut(s, "\n")
			return l
		},
		"short": func(s string, n int) string {
			if len(s) > n {
				return s[:n]
			}
			return s
		},
		"nl2list":    func(s []string) string { return strings.Join(s, "\n") },
		"fieldVal":   func(i *store.Issue, id string) []string { return i.Fields[id] },
		"split":      strings.Split,
		"withSort":   withSort,
		"sortActive": func(q, s string) bool { return currentSort(q) == s },
		"sortAsc": func(q string) bool {
			raw := rawSort(q)
			return strings.HasSuffix(raw, "-asc") || raw == "key"
		},
		"trimPrefix": strings.TrimPrefix,
		"versionNames": func(vs []*store.Version) []string {
			out := make([]string, len(vs))
			for i, v := range vs {
				out[i] = v.Name
			}
			return out
		},
	}
}

func rawSort(q string) string {
	for _, t := range strings.Fields(q) {
		if strings.HasPrefix(t, "sort:") {
			return strings.TrimPrefix(t, "sort:")
		}
	}
	return ""
}

func currentSort(q string) string {
	return strings.TrimSuffix(strings.TrimSuffix(rawSort(q), "-asc"), "-desc")
}

func withSort(q, sort string) string {
	var keep []string
	for _, t := range strings.Fields(q) {
		if !strings.HasPrefix(t, "sort:") {
			keep = append(keep, t)
		}
	}
	raw := rawSort(q)
	next := sort
	if currentSort(q) == sort {
		switch {
		case sort == "key" && raw == "key":
			next = "key-desc"
		case sort != "key" && !strings.HasSuffix(raw, "-asc"):
			next = sort + "-asc"
		}
	}
	return strings.TrimSpace(strings.Join(append(keep, "sort:"+next), " "))
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

func initials(name string) string {
	parts := strings.FieldsFunc(name, func(r rune) bool { return r == ' ' || r == '_' || r == '-' || r == '.' })
	out := ""
	for _, p := range parts {
		r := []rune(p)
		if len(r) > 0 {
			out += strings.ToUpper(string(r[0]))
		}
		if len([]rune(out)) == 2 {
			break
		}
	}
	if out == "" {
		return "?"
	}
	return out
}

func ago(ts int64) string {
	if ts == 0 {
		return "never"
	}
	d := time.Since(time.Unix(ts, 0))
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return plural(int(d.Minutes()), "minute") + " ago"
	case d < 24*time.Hour:
		return plural(int(d.Hours()), "hour") + " ago"
	case d < 30*24*time.Hour:
		return plural(int(d.Hours()/24), "day") + " ago"
	case d < 365*24*time.Hour:
		return plural(int(d.Hours()/24/30), "month") + " ago"
	}
	return plural(int(d.Hours()/24/365), "year") + " ago"
}

func plural(n int, unit string) string {
	if n == 1 {
		return "1 " + unit
	}
	return fmt.Sprintf("%d %ss", n, unit)
}

func humanBytes(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}

func (s *Server) template(page string) (*template.Template, error) {
	dev := s.app.Cfg().Server.Dev
	if !dev {
		s.tmplMu.Lock()
		e := s.tmplCache[page]
		s.tmplMu.Unlock()
		if e != nil {
			return e.t, nil
		}
	}
	fsys := s.webFS()
	t := template.New("layout").Funcs(s.funcs())
	files := []string{"templates/layout.html"}
	partials, _ := fs.Glob(s.baseFS(), "templates/partials/*.html")
	files = append(files, partials...)
	files = append(files, "templates/pages/"+page+".html")
	for _, f := range files {
		b, err := fs.ReadFile(fsys, f)
		if err != nil {
			return nil, fmt.Errorf("template %s: %w", f, err)
		}
		if _, err := t.New(path.Base(f)).Parse(string(b)); err != nil {
			return nil, fmt.Errorf("template %s: %w", f, err)
		}
	}
	if !dev {
		s.tmplMu.Lock()
		s.tmplCache[page] = &tmplEntry{t: t}
		s.tmplMu.Unlock()
	}
	return t, nil
}

func (s *Server) baseFS() fs.FS {
	if s.app.Cfg().Server.Dev {
		return os.DirFS(s.app.Cfg().Server.WebDir)
	}
	return s.embedFS
}

type Page struct {
	Title     string
	Cfg       *config.Config
	Actor     *app.Actor
	User      *store.User
	CSRF      string
	Path      string
	Query     url.Values
	FlashKind string
	Flash     string
	Unread    int
	Projects  []config.Project
	Errors    map[string]string
	Form      url.Values
	Data      any
	Version   string
	Locale    string
	NoIndex   bool

	s *Server
	r *http.Request
}

func (s *Server) newPage(w http.ResponseWriter, r *http.Request, title string, data any) *Page {
	act := actorFrom(r)
	p := &Page{
		Title: title, Cfg: s.app.Cfg(), Actor: act, User: act.User, Path: r.URL.Path, Query: r.URL.Query(),
		Data: data, Version: s.Version, s: s, r: r, Form: url.Values{},
	}
	p.CSRF, _ = r.Context().Value(csrfKey).(string)
	p.FlashKind, p.Flash = s.takeFlash(w, r)
	if act.User != nil {
		p.Unread = s.app.DB.UnreadCount(r.Context(), act.User.ID)
	}
	p.Projects = s.app.VisibleProjects(act)
	p.Locale = p.Cfg.Instance.DefaultLocale
	if act.User != nil && act.User.Locale != "" {
		p.Locale = act.User.Locale
	} else if al := r.Header.Get("Accept-Language"); al != "" {
		tag, _, _ := strings.Cut(al, ",")
		tag, _, _ = strings.Cut(tag, ";")
		tag, _, _ = strings.Cut(strings.TrimSpace(tag), "-")
		if _, ok := p.Cfg.Locales[strings.ToLower(tag)]; ok {
			p.Locale = strings.ToLower(tag)
		}
	}
	return p
}

func (p *Page) Can(perm string, project ...string) bool {
	pk := ""
	if len(project) > 0 {
		pk = project[0]
	}
	return p.s.app.Can(p.Actor, perm, pk)
}

func (p *Page) T(key, def string) string { return p.Cfg.T(p.Locale, key, def) }

func (p *Page) Workflow(project string) *config.Workflow { return p.Cfg.ProjectWorkflow(project) }

func (p *Page) State(project, id string) config.State {
	if wf := p.Cfg.ProjectWorkflow(project); wf != nil {
		if s := wf.State(id); s != nil {
			return *s
		}
	}
	return config.State{ID: id, Label: id, Color: "#888", Category: "open"}
}

func (p *Page) Resolution(project, id string) config.Resolution {
	if wf := p.Cfg.ProjectWorkflow(project); wf != nil {
		if r := wf.Resolution(id); r != nil {
			return *r
		}
	}
	return config.Resolution{ID: id, Label: id}
}

func (p *Page) Field(id string) *config.Field { return p.Cfg.Field(id) }

func (p *Page) LabelColor(name string) template.CSS {
	if l := p.Cfg.Label(name); l != nil && l.Color != "" {
		return safeColor(l.Color)
	}
	return template.CSS("#64748b")
}

func (p *Page) AllListFields() []*config.Field {
	var out []*config.Field
	for i := range p.Cfg.Fields {
		if p.Cfg.Fields[i].ShowInList && (!p.Cfg.Fields[i].Private || p.Can("issue.view_private")) {
			out = append(out, &p.Cfg.Fields[i])
		}
	}
	return out
}

func (p *Page) ListFields(project string) []*config.Field {
	var out []*config.Field
	for _, f := range p.Cfg.ProjectFields(project) {
		if f.ShowInList {
			out = append(out, f)
		}
	}
	return out
}

func (p *Page) Badge(u *store.User) (out struct {
	Text  string
	Color template.CSS
}) {
	if u == nil {
		return
	}
	if u.System {
		out.Text, out.Color = "Bot", "#64748b"
		return
	}
	role := p.s.app.Role(u)
	if r, ok := p.Cfg.Roles[role]; ok && r.Badge != "" {
		out.Text, out.Color = r.Badge, safeColor(r.Color)
	}
	return
}

func (p *Page) IsMe(u *store.User) bool { return u != nil && p.User != nil && u.ID == p.User.ID }

func (p *Page) Err(field string) string { return p.Errors[field] }

func (p *Page) FormValue(k string) string { return p.Form.Get(k) }

func (p *Page) FormValues(k string) []string { return p.Form[k] }

func (p *Page) QueryGet(k string) string { return p.Query.Get(k) }

func (p *Page) Captcha(action string) bool {
	if !p.Cfg.Spam.Captcha.Enabled(action) {
		return false
	}
	if action != "register" && p.User != nil && (p.User.Trusted || p.Can("triage")) {
		return false
	}
	return true
}

func (s *Server) render(w http.ResponseWriter, r *http.Request, status int, page string, p *Page) {
	t, err := s.template(page)
	if err != nil {
		s.app.Log.Error("template load", "page", page, "err", err)
		http.Error(w, "template error: "+err.Error(), 500)
		return
	}
	var buf bytes.Buffer
	if err := t.ExecuteTemplate(&buf, "layout.html", p); err != nil {
		s.app.Log.Error("template exec", "page", page, "err", err)
		msg := "template error"
		if s.app.Cfg().Server.Dev {
			msg += ": " + err.Error()
		}
		http.Error(w, msg, 500)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	w.Write(buf.Bytes())
}

func (s *Server) fail(w http.ResponseWriter, r *http.Request, err error) {
	if wantsJSON(r) {
		errJSON(w, err)
		return
	}
	status := app.Status(err)
	if status == 401 {
		http.Redirect(w, r, "/login?next="+url.QueryEscape(r.URL.RequestURI()), http.StatusSeeOther)
		return
	}
	msg := err.Error()
	if status == 500 {
		s.app.Log.Error("request failed", "path", r.URL.Path, "err", err)
		msg = "Something went wrong on our side."
	}
	p := s.newPage(w, r, http.StatusText(status), map[string]any{"Status": status, "Message": msg})
	s.render(w, r, status, "error", p)
}
