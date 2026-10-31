// Package importer migrates issues from Jira and GitHub Issues into buggy.
package importer

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/kohanmathers/buggy/internal/app"
	"github.com/kohanmathers/buggy/internal/config"
	"github.com/kohanmathers/buggy/internal/store"
)

type Options struct {
	Project       string
	URL           string
	Query         string
	Repo          string
	User          string
	Token         string
	Limit         int
	KeepNumbers   bool
	StatusMap     map[string]string
	ResolutionMap map[string]string
	Log           func(format string, v ...any)
}

type importComment struct {
	Author  string
	Body    string
	Created int64
}

type importIssue struct {
	Number       int64
	Title        string
	Description  string
	Status       string
	StatusCat    string
	Resolution   string
	Reporter     string
	ReporterName string
	Created      int64
	Updated      int64
	Resolved     int64
	Labels       []string
	Affects      []string
	Fixes        []string
	Votes        int64
	Comments     []importComment
	Links        []importLink
	SourceKey    string
}

type importLink struct {
	Kind string
	To   string
}

type runner struct {
	a      *app.App
	opts   Options
	row    *store.ProjectRow
	wf     *config.Workflow
	users  map[string]int64
	source string
	keyMap map[string]string
	links  []struct{ from, kind, to string }
}

func newRunner(ctx context.Context, a *app.App, opts Options, source string) (*runner, error) {
	pc := a.Cfg().Project(opts.Project)
	if pc == nil {
		return nil, fmt.Errorf("project %s is not configured; add it to config.yml first", opts.Project)
	}
	row, err := a.DB.EnsureProject(ctx, pc.Key)
	if err != nil {
		return nil, err
	}
	if opts.Log == nil {
		opts.Log = func(string, ...any) {}
	}
	return &runner{a: a, opts: opts, row: row, wf: a.Cfg().ProjectWorkflow(pc.Key), users: map[string]int64{},
		source: source, keyMap: map[string]string{}}, nil
}

var badUser = regexp.MustCompile(`[^A-Za-z0-9_.-]+`)

func (r *runner) user(ctx context.Context, name, display string) (int64, error) {
	if name == "" {
		name = "ghost"
	}
	if id, ok := r.users[name]; ok {
		return id, nil
	}
	provider := "import-" + r.source
	if u, err := r.a.DB.UserByIdentity(ctx, provider, name); err == nil {
		r.users[name] = u.ID
		return u.ID, nil
	}
	base := strings.Trim(badUser.ReplaceAllString(name, "_"), "._-")
	if len(base) < 3 {
		base += "_user"
	}
	if len(base) > 28 {
		base = base[:28]
	}
	uname := base
	for i := 2; !app.ValidUsername(uname) || r.a.DB.UsernameTaken(ctx, uname); i++ {
		uname = fmt.Sprintf("%s_%d", base, i)
	}
	u := &store.User{Username: uname, DisplayName: display, Role: r.a.Cfg().Auth.DefaultRole, Bio: "Imported from " + r.source}
	if err := r.a.DB.CreateUser(ctx, u); err != nil {
		return 0, err
	}
	if err := r.a.DB.LinkIdentity(ctx, provider, name, u.ID); err != nil {
		return 0, err
	}
	r.users[name] = u.ID
	return u.ID, nil
}

func norm(s string) string {
	return strings.Trim(regexp.MustCompile(`[^a-z0-9]+`).ReplaceAllString(strings.ToLower(s), "_"), "_")
}

func (r *runner) mapState(src, cat string) string {
	if s, ok := r.opts.StatusMap[strings.ToLower(src)]; ok && r.wf.State(s) != nil {
		return s
	}
	if r.wf.State(norm(src)) != nil {
		return norm(src)
	}
	switch cat {
	case "done":
		if s := r.wf.FirstStateIn("closed"); s != "" {
			return s
		}
	case "indeterminate":
		if s := r.wf.FirstStateIn("active"); s != "" {
			return s
		}
	}
	return r.wf.Initial
}

func (r *runner) mapResolution(src string) string {
	if src == "" {
		src = "fixed"
	}
	if s, ok := r.opts.ResolutionMap[strings.ToLower(src)]; ok && r.wf.Resolution(s) != nil {
		return s
	}
	if r.wf.Resolution(norm(src)) != nil {
		return norm(src)
	}
	aliases := map[string]string{"won_t_fix": "wont_fix", "not_planned": "wont_fix", "completed": "fixed", "done": "fixed",
		"works_as_designed": "works_as_intended", "awaiting_response": "incomplete"}
	if a, ok := aliases[norm(src)]; ok && r.wf.Resolution(a) != nil {
		return a
	}
	if r.wf.Resolution("fixed") != nil {
		return "fixed"
	}
	if len(r.wf.Resolutions) > 0 {
		return r.wf.Resolutions[0].ID
	}
	return ""
}

func (r *runner) versionIDs(ctx context.Context, names []string) ([]int64, error) {
	var out []int64
	for _, n := range names {
		v, err := r.a.DB.VersionByName(ctx, r.row.ID, n)
		if err == store.ErrNotFound {
			v, err = r.a.DB.UpsertVersion(ctx, r.row.ID, store.Version{Name: n, SortOrder: -1, Released: true})
		}
		if err != nil {
			return nil, err
		}
		out = append(out, v.ID)
	}
	return out, nil
}

func (r *runner) save(ctx context.Context, in importIssue) (bool, error) {
	db := r.a.DB
	number := int64(0)
	if r.opts.KeepNumbers {
		number = in.Number
		if _, err := db.IssueByKey(ctx, fmt.Sprintf("%s-%d", r.row.Key, number)); err == nil {
			r.keyMap[in.SourceKey] = fmt.Sprintf("%s-%d", r.row.Key, number)
			return false, nil
		}
	}
	reporter, err := r.user(ctx, in.Reporter, in.ReporterName)
	if err != nil {
		return false, err
	}
	state := r.mapState(in.Status, in.StatusCat)
	res := ""
	if s := r.wf.State(state); s != nil && s.Closed() {
		res = r.mapResolution(in.Resolution)
		if in.Resolved == 0 {
			in.Resolved = in.Updated
		}
	} else {
		in.Resolved = 0
	}
	affects, err := r.versionIDs(ctx, in.Affects)
	if err != nil {
		return false, err
	}
	fixes, err := r.versionIDs(ctx, in.Fixes)
	if err != nil {
		return false, err
	}
	commentAuthors := make([]int64, len(in.Comments))
	for i, c := range in.Comments {
		if commentAuthors[i], err = r.user(ctx, c.Author, c.Author); err != nil {
			return false, err
		}
	}
	iss := &store.Issue{ProjectID: r.row.ID, Number: number, Title: truncate(in.Title, 250), Description: in.Description,
		State: state, Resolution: res, ReporterID: reporter, Votes: in.Votes, CreatedAt: in.Created, UpdatedAt: in.Updated,
		ResolvedAt: in.Resolved, ActivityAt: in.Updated, CommentCount: int64(len(in.Comments))}
	err = db.InTx(ctx, func(tx *store.Tx) error {
		if err := db.CreateIssue(ctx, tx, iss, r.row.Key); err != nil {
			return err
		}
		if err := db.SetLabels(ctx, tx, iss.ID, in.Labels); err != nil {
			return err
		}
		if err := db.SetIssueVersions(ctx, tx, iss.ID, "affects", affects); err != nil {
			return err
		}
		if err := db.SetIssueVersions(ctx, tx, iss.ID, "fixed", fixes); err != nil {
			return err
		}
		for i, c := range in.Comments {
			if err := db.CreateComment(ctx, tx, &store.Comment{IssueID: iss.ID, AuthorID: commentAuthors[i], Body: c.Body, CreatedAt: c.Created}); err != nil {
				return err
			}
		}
		if err := db.AddHistory(ctx, tx, iss.ID, r.a.System.ID, "imported", "", r.source+" "+in.SourceKey); err != nil {
			return err
		}
		return db.UpdateFTS(ctx, tx, iss.ID)
	})
	if err != nil {
		return false, err
	}
	r.keyMap[in.SourceKey] = iss.Key
	for _, l := range in.Links {
		r.links = append(r.links, struct{ from, kind, to string }{in.SourceKey, l.Kind, l.To})
	}
	return true, nil
}

func (r *runner) finish(ctx context.Context) {
	db := r.a.DB
	for _, l := range r.links {
		from, to := r.keyMap[l.from], r.keyMap[l.to]
		if from == "" || to == "" {
			continue
		}
		a, err1 := db.IssueByKey(ctx, from)
		b, err2 := db.IssueByKey(ctx, to)
		if err1 != nil || err2 != nil {
			continue
		}
		db.AddLink(ctx, db, a.ID, b.ID, l.kind, r.a.System.ID)
		if l.kind == "duplicates" && a.DuplicateOf == 0 && a.ResolvedAt != 0 {
			db.Exec(ctx, `UPDATE issues SET duplicate_of = ? WHERE id = ?`, b.ID, a.ID)
		}
	}
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n])
	}
	return s
}

func getJSON(ctx context.Context, cl *http.Client, url string, hdr map[string]string, out any) error {
	for attempt := 0; ; attempt++ {
		req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
		req.Header.Set("Accept", "application/json")
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		resp, err := cl.Do(req)
		if err != nil {
			return err
		}
		if (resp.StatusCode == 429 || resp.StatusCode == 403 && resp.Header.Get("X-RateLimit-Remaining") == "0") && attempt < 5 {
			resp.Body.Close()
			wait := time.Duration(10*(attempt+1)) * time.Second
			time.Sleep(wait)
			continue
		}
		defer resp.Body.Close()
		if resp.StatusCode >= 300 {
			return fmt.Errorf("GET %s: HTTP %d", url, resp.StatusCode)
		}
		return json.NewDecoder(resp.Body).Decode(out)
	}
}

func parseTime(s string) int64 {
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05.000-0700", "2006-01-02T15:04:05-0700"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.Unix()
		}
	}
	return 0
}
