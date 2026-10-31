// Package search parses buggy's query language into SQL filters.
package search

import (
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/kohanmathers/buggy/internal/config"
	"github.com/kohanmathers/buggy/internal/store"
)

type Term struct {
	Neg   bool
	Key   string
	Op    string
	Value string
}

type Parsed struct {
	Terms []Term
	Text  []string
	Sort  string
}

func Parse(q string) Parsed {
	var p Parsed
	for _, tok := range tokenize(q) {
		neg := false
		raw := tok
		if strings.HasPrefix(tok, "-") && len(tok) > 1 && strings.Contains(tok, ":") {
			neg = true
			tok = tok[1:]
		}
		k, v, ok := strings.Cut(tok, ":")
		if !ok || k == "" || strings.ContainsFunc(k, func(r rune) bool { return !(unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_') }) {
			p.Text = append(p.Text, strings.Trim(raw, `"`))
			continue
		}
		op := ":"
		for _, o := range []string{">=", "<=", ">", "<", "="} {
			if strings.HasPrefix(v, o) {
				op, v = o, v[len(o):]
				break
			}
		}
		v = strings.Trim(v, `"`)
		k = strings.ToLower(k)
		if k == "sort" || k == "order" {
			p.Sort = strings.ToLower(v)
			continue
		}
		p.Terms = append(p.Terms, Term{Neg: neg, Key: k, Op: op, Value: v})
	}
	return p
}

func tokenize(s string) []string {
	var out []string
	var b strings.Builder
	inQ := false
	for _, r := range s {
		switch {
		case r == '"':
			inQ = !inQ
			b.WriteRune(r)
		case unicode.IsSpace(r) && !inQ:
			if b.Len() > 0 {
				out = append(out, b.String())
				b.Reset()
			}
		default:
			b.WriteRune(r)
		}
	}
	if b.Len() > 0 {
		out = append(out, b.String())
	}
	return out
}

type Context struct {
	Cfg        *config.Config
	UserID     int64
	Now        time.Time
	UserByName func(name string) int64
}

var Keys = []struct{ Key, Help string }{
	{"project:APP", "Project key(s)"},
	{"status:open,confirmed", "Workflow state id(s)"},
	{"is:open / is:closed", "Unresolved / resolved issues"},
	{"is:active / is:waiting", "State category"},
	{"is:private / is:public / is:duplicate / is:pending / is:unassigned", "Flags"},
	{"resolution:fixed", "Resolution id(s)"},
	{"label:crash", "Has label"},
	{"reporter:me / assignee:none", "People (username, me, none)"},
	{"version:1.2 / version:>=1.2", "Affected versions (comparisons use release order)"},
	{"fixed:1.3", "Fix versions"},
	{"votes:>10 / comments:>5 / confirmations:>=3", "Counters"},
	{"created:<7d / updated:>30d / resolved:>2024-01-01", "Age (d, w, h) or date"},
	{"voted:me / watching:me / commenter:alice", "Your activity"},
	{"has:attachment / has:fingerprint / has:commits", "Presence checks"},
	{"fingerprint:ab12cd", "Crash signature"},
	{"key:APP-12 / title:\"map\"", "Exact key / title contains"},
	{"<field>:value / <field>:>5 / <field>:none", "Any custom field"},
	{"-label:wontfix", "Prefix with - to negate"},
	{"sort:votes / sort:created / sort:updated / sort:comments / sort:created-asc", "Ordering"},
}

func Compile(p Parsed, c Context) (store.IssueQuery, error) {
	var q store.IssueQuery
	for _, t := range p.Text {
		q.Text = append(q.Text, store.Terms(t)...)
	}
	for _, t := range p.Terms {
		sql, args, err := compileTerm(t, c)
		if err != nil {
			return q, err
		}
		if sql == "" {
			continue
		}
		if t.Neg {
			sql = "NOT (" + sql + ")"
		}
		q.Where = append(q.Where, sql)
		q.Args = append(q.Args, args...)
	}
	if p.Sort == "key-desc" {
		p.Sort = "key-newest"
	}
	switch strings.TrimSuffix(p.Sort, "-desc") {
	case "key-newest":
		q.Order = "i.number DESC"
	case "", "relevance":
		if len(q.Text) == 0 {
			q.Order = "i.updated_at DESC"
		}
	case "votes", "popular":
		q.Order = "i.votes DESC"
	case "votes-asc":
		q.Order = "i.votes ASC"
	case "created", "newest":
		q.Order = "i.created_at DESC"
	case "created-asc", "oldest":
		q.Order = "i.created_at ASC"
	case "updated":
		q.Order = "i.updated_at DESC"
	case "updated-asc":
		q.Order = "i.updated_at ASC"
	case "comments":
		q.Order = "i.comment_count DESC"
	case "confirmations", "affected":
		q.Order = "i.confirmations DESC"
	case "watchers":
		q.Order = "i.watchers DESC"
	case "activity":
		q.Order = "i.activity_at DESC"
	case "key", "number":
		q.Order = "i.number ASC"
	case "key-asc":
		q.Order = "i.number ASC"
	default:
		return q, fmt.Errorf("unknown sort %q", p.Sort)
	}
	return q, nil
}

func list(v string) []string {
	var out []string
	for _, s := range strings.Split(v, ",") {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func in(col string, vals []string) (string, []any) {
	if len(vals) == 1 {
		return col + " = ?", []any{vals[0]}
	}
	ph := strings.TrimSuffix(strings.Repeat("?, ", len(vals)), ", ")
	args := make([]any, len(vals))
	for i, v := range vals {
		args[i] = v
	}
	return col + " IN (" + ph + ")", args
}

func sqlOp(op string) string {
	if op == ":" {
		return "="
	}
	return op
}

func (c Context) user(name string) int64 {
	switch strings.ToLower(name) {
	case "me":
		if c.UserID == 0 {
			return -1
		}
		return c.UserID
	case "none", "nobody":
		return 0
	}
	if c.UserByName != nil {
		if id := c.UserByName(name); id != 0 {
			return id
		}
	}
	return -1
}

func statesInCategory(cfg *config.Config, cat string) []string {
	seen := map[string]bool{}
	var out []string
	for _, wf := range cfg.Workflows {
		for _, s := range wf.States {
			if s.Category == cat && !seen[s.ID] {
				seen[s.ID] = true
				out = append(out, s.ID)
			}
		}
	}
	if len(out) == 0 {
		out = []string{"__none__"}
	}
	return out
}

func compileTerm(t Term, c Context) (string, []any, error) {
	v := t.Value
	vals := list(v)
	if len(vals) == 0 {
		return "", nil, fmt.Errorf("%s: missing value", t.Key)
	}
	switch t.Key {
	case "project", "p":
		up := make([]string, len(vals))
		for i, x := range vals {
			up[i] = strings.ToUpper(x)
		}
		s, a := in("key", up)
		return "i.project_id IN (SELECT id FROM projects WHERE " + s + ")", a, nil
	case "status", "state", "s":
		s, a := in("i.state", vals)
		return s, a, nil
	case "category":
		var states []string
		for _, x := range vals {
			states = append(states, statesInCategory(c.Cfg, x)...)
		}
		s, a := in("i.state", states)
		return s, a, nil
	case "resolution", "res":
		if v == "none" {
			return "i.resolution = ''", nil, nil
		}
		s, a := in("i.resolution", vals)
		return s, a, nil
	case "is":
		var parts []string
		var args []any
		for _, x := range vals {
			switch strings.ToLower(x) {
			case "open", "unresolved":
				parts = append(parts, "i.resolved_at = 0")
			case "closed", "resolved", "done":
				parts = append(parts, "i.resolved_at <> 0")
			case "active", "waiting":
				s, a := in("i.state", statesInCategory(c.Cfg, x))
				parts, args = append(parts, s), append(args, a...)
			case "private", "security":
				parts = append(parts, "i.private = TRUE")
			case "public":
				parts = append(parts, "i.private = FALSE")
			case "duplicate", "dupe":
				parts = append(parts, "i.duplicate_of <> 0")
			case "pending":
				parts = append(parts, "i.moderation = 'pending'")
			case "unassigned":
				parts = append(parts, "i.assignee_id = 0")
			case "assigned":
				parts = append(parts, "i.assignee_id <> 0")
			default:
				return "", nil, fmt.Errorf("is: unknown value %q", x)
			}
		}
		return "(" + strings.Join(parts, " OR ") + ")", args, nil
	case "label", "l", "labels":
		if v == "none" {
			return "NOT EXISTS (SELECT 1 FROM issue_labels il WHERE il.issue_id = i.id)", nil, nil
		}
		s, a := in("il.label", vals)
		return "EXISTS (SELECT 1 FROM issue_labels il WHERE il.issue_id = i.id AND " + s + ")", a, nil
	case "reporter", "author", "by":
		return "i.reporter_id = ?", []any{c.user(v)}, nil
	case "assignee", "assigned":
		return "i.assignee_id = ?", []any{c.user(v)}, nil
	case "voted", "voter":
		return "EXISTS (SELECT 1 FROM votes vt WHERE vt.issue_id = i.id AND vt.user_id = ?)", []any{c.user(v)}, nil
	case "watching", "watcher":
		return "EXISTS (SELECT 1 FROM watchers w WHERE w.issue_id = i.id AND w.user_id = ?)", []any{c.user(v)}, nil
	case "commenter", "commented":
		return "EXISTS (SELECT 1 FROM comments cm WHERE cm.issue_id = i.id AND cm.author_id = ? AND cm.deleted = FALSE)", []any{c.user(v)}, nil
	case "confirmed_by", "affects_me":
		return "EXISTS (SELECT 1 FROM confirmations cf WHERE cf.issue_id = i.id AND cf.user_id = ?)", []any{c.user(v)}, nil
	case "version", "affects", "av", "affected":
		return versionTerm("affects", t)
	case "fixed", "fix", "fv", "fixversion":
		return versionTerm("fixed", t)
	case "votes", "comments", "watchers", "confirmations":
		col := map[string]string{"votes": "i.votes", "comments": "i.comment_count", "watchers": "i.watchers", "confirmations": "i.confirmations"}[t.Key]
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return "", nil, fmt.Errorf("%s: expected a number", t.Key)
		}
		return col + " " + sqlOp(t.Op) + " ?", []any{n}, nil
	case "created", "updated", "resolved", "activity":
		col := map[string]string{"created": "i.created_at", "updated": "i.updated_at", "resolved": "i.resolved_at", "activity": "i.activity_at"}[t.Key]
		return dateTerm(col, t, c.Now)
	case "fingerprint", "fp", "signature":
		s, a := in("i.fingerprint", vals)
		return s, a, nil
	case "key", "id":
		up := make([]string, len(vals))
		for i, x := range vals {
			up[i] = strings.ToUpper(x)
		}
		s, a := in("i.key", up)
		return s, a, nil
	case "title":
		return "lower(i.title) LIKE ?", []any{"%" + strings.ToLower(v) + "%"}, nil
	case "has":
		switch v {
		case "attachment", "attachments", "file", "files":
			return "EXISTS (SELECT 1 FROM attachments a WHERE a.issue_id = i.id AND a.deleted = FALSE)", nil, nil
		case "fingerprint", "crash":
			return "i.fingerprint <> ''", nil, nil
		case "assignee":
			return "i.assignee_id <> 0", nil, nil
		case "votes":
			return "i.votes > 0", nil, nil
		case "comments":
			return "i.comment_count > 0", nil, nil
		case "commits":
			return "EXISTS (SELECT 1 FROM commits cm WHERE cm.issue_id = i.id)", nil, nil
		case "links":
			return "EXISTS (SELECT 1 FROM links lk WHERE lk.source_id = i.id OR lk.target_id = i.id)", nil, nil
		}
		return "", nil, fmt.Errorf("has: unknown value %q", v)
	}
	if f := c.Cfg.Field(t.Key); f != nil {
		return fieldTerm(f, t, c.Now)
	}
	return "", nil, fmt.Errorf("unknown search key %q", t.Key)
}

func versionTerm(kind string, t Term) (string, []any, error) {
	base := "SELECT 1 FROM issue_versions iv JOIN versions v ON v.id = iv.version_id WHERE iv.issue_id = i.id AND iv.kind = '" + kind + "'"
	if t.Value == "none" {
		return "NOT EXISTS (" + base + ")", nil, nil
	}
	if t.Op == ":" || t.Op == "=" {
		s, a := in("v.name", list(t.Value))
		return "EXISTS (" + base + " AND " + s + ")", a, nil
	}
	return "EXISTS (" + base + " AND v.sort_order " + t.Op +
		" (SELECT v2.sort_order FROM versions v2 WHERE v2.project_id = v.project_id AND v2.name = ?))", []any{t.Value}, nil
}

func dateTerm(col string, t Term, now time.Time) (string, []any, error) {
	if now.IsZero() {
		now = time.Now()
	}
	if d, err := config.ParseDuration(t.Value); err == nil && t.Value != "" && !strings.Contains(t.Value, "-") {
		cut := now.Add(-d).Unix()
		switch t.Op {
		case ">", ">=":
			return col + " <> 0 AND " + col + " < ?", []any{cut}, nil
		default:
			return col + " >= ?", []any{cut}, nil
		}
	}
	day, err := time.Parse("2006-01-02", t.Value)
	if err != nil {
		return "", nil, fmt.Errorf("%s: expected an age like 7d or a date like 2024-01-31", t.Value)
	}
	start, end := day.Unix(), day.Add(24*time.Hour).Unix()
	switch t.Op {
	case ">":
		return col + " >= ?", []any{end}, nil
	case ">=":
		return col + " >= ?", []any{start}, nil
	case "<":
		return col + " <> 0 AND " + col + " < ?", []any{start}, nil
	case "<=":
		return col + " <> 0 AND " + col + " < ?", []any{end}, nil
	}
	return col + " >= ? AND " + col + " < ?", []any{start, end}, nil
}

func fieldTerm(f *config.Field, t Term, now time.Time) (string, []any, error) {
	base := "SELECT 1 FROM field_values fv WHERE fv.issue_id = i.id AND fv.field = ?"
	args := []any{f.ID}
	if t.Value == "none" {
		return "NOT EXISTS (" + base + ")", args, nil
	}
	if t.Value == "any" {
		return "EXISTS (" + base + ")", args, nil
	}
	if t.Op != ":" && t.Op != "=" {
		var n float64
		switch f.Type {
		case "number":
			x, err := strconv.ParseFloat(t.Value, 64)
			if err != nil {
				return "", nil, fmt.Errorf("%s: expected a number", f.ID)
			}
			n = x
		case "date":
			d, err := time.Parse("2006-01-02", t.Value)
			if err != nil {
				return "", nil, fmt.Errorf("%s: expected a date like 2024-01-31", f.ID)
			}
			n = float64(d.Unix())
		default:
			return "", nil, fmt.Errorf("%s: comparisons only work on number and date fields", f.ID)
		}
		return "EXISTS (" + base + " AND fv.num " + t.Op + " ?)", append(args, n), nil
	}
	vals := list(t.Value)
	if f.Type == "text" || f.Type == "textarea" || f.Type == "url" {
		return "EXISTS (" + base + " AND lower(fv.value) LIKE ?)", append(args, "%"+strings.ToLower(t.Value)+"%"), nil
	}
	s, a := in("fv.value", vals)
	return "EXISTS (" + base + " AND " + s + ")", append(args, a...), nil
}
