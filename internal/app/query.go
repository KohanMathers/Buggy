package app

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"time"

	"github.com/kohanmathers/buggy/internal/config"
	"github.com/kohanmathers/buggy/internal/search"
	"github.com/kohanmathers/buggy/internal/store"
)

type SearchResult struct {
	Query  string         `json:"query"`
	Issues []*store.Issue `json:"issues"`
	Total  int            `json:"total"`
	Page   int            `json:"page"`
	Pages  int            `json:"pages"`
	Error  string         `json:"error,omitempty"`
}

func (a *App) searchCtx(ctx context.Context, act *Actor) search.Context {
	return search.Context{
		Cfg:    a.Cfg(),
		UserID: act.ID(),
		Now:    time.Now(),
		UserByName: func(n string) int64 {
			if u, err := a.DB.UserByName(ctx, n); err == nil {
				return u.ID
			}
			return 0
		},
	}
}

func (a *App) CompileQuery(ctx context.Context, act *Actor, q string) (store.IssueQuery, error) {
	iq, err := search.Compile(search.Parse(q), a.searchCtx(ctx, act))
	if err != nil {
		return iq, Errf(400, "%s", err.Error())
	}
	vw, va := a.visibility(act)
	iq.Where = append(vw, iq.Where...)
	iq.Args = append(va, iq.Args...)
	return iq, nil
}

func (a *App) Search(ctx context.Context, act *Actor, q string, page, perPage int) *SearchResult {
	res := &SearchResult{Query: q, Page: page}
	if perPage <= 0 {
		perPage = a.Cfg().Instance.IssuesPerPage
	}
	if page < 1 {
		page, res.Page = 1, 1
	}
	iq, err := a.CompileQuery(ctx, act, q)
	if err != nil {
		res.Error = err.Error()
		return res
	}
	iq.Limit, iq.Offset = perPage, (page-1)*perPage
	list, total, err := a.DB.SearchIssues(ctx, iq)
	if err != nil {
		a.Log.Error("search", "err", err, "q", q)
		res.Error = "search failed"
		return res
	}
	a.DB.LoadRelations(ctx, list)
	res.Issues, res.Total = list, total
	res.Pages = (total + perPage - 1) / perPage
	return res
}

type SimilarIssue struct {
	Issue  *store.Issue `json:"issue"`
	Score  float64      `json:"score"`
	Reason string       `json:"reason"`
}

var stop = map[string]bool{"the": true, "and": true, "when": true, "with": true, "for": true, "not": true, "does": true,
	"doesn": true, "can": true, "cannot": true, "after": true, "from": true, "into": true, "this": true, "that": true,
	"are": true, "was": true, "is": true, "in": true, "on": true, "of": true, "to": true, "an": true, "it": true, "my": true,
	"bug": true, "issue": true, "problem": true, "error": false}

func keyTerms(s string) []string {
	var out []string
	for _, t := range store.Terms(s) {
		if !stop[t] {
			out = append(out, t)
		}
	}
	return out
}

func (a *App) Similar(ctx context.Context, act *Actor, project, title, description string, exclude int64) []SimilarIssue {
	pc := a.Cfg().Project(project)
	if pc == nil {
		return nil
	}
	row, err := a.DB.ProjectByKey(ctx, pc.Key)
	if err != nil {
		return nil
	}
	var out []SimilarIssue
	seen := map[int64]bool{exclude: true}
	if description != "" {
		res := a.ParseText(pc.Key, description, "")
		if res.Fingerprint != "" {
			fps, _ := a.DB.IssuesByFingerprint(ctx, row.ID, res.Fingerprint, exclude, 5)
			for _, i := range fps {
				if a.CanSeeIssue(ctx, act, i) && !seen[i.ID] {
					seen[i.ID] = true
					out = append(out, SimilarIssue{Issue: i, Score: 1, Reason: "same crash signature"})
				}
			}
		}
	}
	terms := keyTerms(title)
	if len(terms) == 0 {
		a.DB.LoadRelations(ctx, issuesOf(out))
		return out
	}
	vw, va := a.visibility(act)
	iq := store.IssueQuery{
		Where:   append(vw, "i.project_id = ?"),
		Args:    append(va, row.ID),
		Text:    terms,
		AnyText: true,
		Limit:   25,
	}
	list, _, err := a.DB.SearchIssues(ctx, iq)
	if err != nil {
		a.Log.Error("similar", "err", err)
	}
	qset := map[string]bool{}
	for _, t := range terms {
		qset[stem(t)] = true
	}
	var cands []SimilarIssue
	for rank, i := range list {
		if seen[i.ID] {
			continue
		}
		tt := keyTerms(i.Title)
		overlap := 0
		cset := map[string]bool{}
		for _, t := range tt {
			cset[stem(t)] = true
		}
		for t := range qset {
			if cset[t] {
				overlap++
			}
		}
		union := len(qset) + len(cset) - overlap
		jac := 0.0
		if union > 0 {
			jac = float64(overlap) / float64(union)
		}
		cover := float64(overlap) / float64(len(qset))
		score := 0.5*cover + 0.35*jac + 0.15*(1-float64(rank)/float64(len(list)+1))
		if i.DuplicateOf != 0 {
			score *= 0.6
		}
		if score >= 0.3 {
			reason := "similar title"
			if overlap == 0 {
				reason = "similar description"
			}
			cands = append(cands, SimilarIssue{Issue: i, Score: score, Reason: reason})
		}
	}
	sort.SliceStable(cands, func(x, y int) bool { return cands[x].Score > cands[y].Score })
	for _, c := range cands {
		if len(out) >= 8 {
			break
		}
		if c.Issue.DuplicateOf != 0 {
			if o, err := a.DB.IssueByID(ctx, c.Issue.DuplicateOf); err == nil && !seen[o.ID] && a.CanSeeIssue(ctx, act, o) {
				seen[o.ID] = true
				out = append(out, SimilarIssue{Issue: o, Score: c.Score, Reason: "original of " + c.Issue.Key})
				continue
			}
		}
		seen[c.Issue.ID] = true
		out = append(out, c)
	}
	a.DB.LoadRelations(ctx, issuesOf(out))
	return out
}

func stem(s string) string {
	for _, suf := range []string{"ing", "ed", "es", "s"} {
		if len(s) > len(suf)+3 && strings.HasSuffix(s, suf) {
			return strings.TrimSuffix(s, suf)
		}
	}
	return s
}

func issuesOf(s []SimilarIssue) []*store.Issue {
	out := make([]*store.Issue, len(s))
	for i, x := range s {
		out[i] = x.Issue
	}
	return out
}

type EnvStat struct {
	Field  string     `json:"field"`
	Label  string     `json:"label"`
	Values []EnvValue `json:"values"`
	Total  int        `json:"total"`
}

type EnvValue struct {
	Value   string  `json:"value"`
	Label   string  `json:"label"`
	Count   int     `json:"count"`
	Percent float64 `json:"percent"`
}

func (a *App) EnvSummary(iss *store.Issue, confs []*store.Confirmation) []EnvStat {
	cfg := a.Cfg()
	counts := map[string]map[string]int{}
	add := func(f, v string) {
		if v == "" {
			return
		}
		if counts[f] == nil {
			counts[f] = map[string]int{}
		}
		counts[f][v]++
	}
	var envFields []*config.Field
	for _, f := range cfg.ProjectFields(iss.ProjectKey()) {
		if f.Environment {
			envFields = append(envFields, f)
			add(f.ID, iss.Field(f.ID))
		}
	}
	for _, v := range iss.Affects {
		add("_version", v.Name)
	}
	for _, c := range confs {
		var env map[string]string
		json.Unmarshal([]byte(c.Env), &env)
		for k, v := range env {
			add(k, v)
		}
		add("_version", c.Version)
	}
	var out []EnvStat
	mk := func(id, label string, f *config.Field) {
		m := counts[id]
		if len(m) == 0 {
			return
		}
		st := EnvStat{Field: id, Label: label}
		for v, n := range m {
			st.Total += n
			l := v
			if f != nil {
				l = f.OptionLabel(v)
			}
			st.Values = append(st.Values, EnvValue{Value: v, Label: l, Count: n})
		}
		sort.Slice(st.Values, func(i, j int) bool {
			if st.Values[i].Count != st.Values[j].Count {
				return st.Values[i].Count > st.Values[j].Count
			}
			return st.Values[i].Value < st.Values[j].Value
		})
		for i := range st.Values {
			st.Values[i].Percent = float64(st.Values[i].Count) * 100 / float64(st.Total)
		}
		out = append(out, st)
	}
	mk("_version", "Versions", nil)
	for _, f := range envFields {
		mk(f.ID, f.Label, f)
	}
	return out
}

type ProjectOverview struct {
	Project    config.Project
	Categories map[string]int
	States     map[string]int
	Versions   []*VersionStat
	Trending   []*store.Issue
	Recent     []*store.Issue
	Labels     map[string]int
}

type VersionStat struct {
	*store.Version
	Affected int
	Fixed    int
}

func (a *App) Overview(ctx context.Context, act *Actor, key string) (*ProjectOverview, error) {
	cfg := a.Cfg()
	pc := cfg.Project(key)
	if pc == nil || !a.Can(act, "issue.view", pc.Key) {
		return nil, ErrNotFound
	}
	row, err := a.DB.EnsureProject(ctx, pc.Key)
	if err != nil {
		return nil, err
	}
	ov := &ProjectOverview{Project: *pc, Categories: map[string]int{}}
	ov.States, _ = a.DB.StateCounts(ctx, row.ID, a.Can(act, "issue.view_private", pc.Key))
	wf := cfg.ProjectWorkflow(pc.Key)
	for st, n := range ov.States {
		if s := wf.State(st); s != nil {
			ov.Categories[s.Category] += n
		}
	}
	vers, _ := a.DB.Versions(ctx, row.ID)
	aff, _ := a.DB.VersionCounts(ctx, row.ID, "affects")
	fix, _ := a.DB.VersionCounts(ctx, row.ID, "fixed")
	for i := len(vers) - 1; i >= 0; i-- {
		v := vers[i]
		if v.Archived {
			continue
		}
		ov.Versions = append(ov.Versions, &VersionStat{Version: v, Affected: aff[v.ID], Fixed: fix[v.ID]})
	}
	ov.Trending = a.Trending(ctx, act, row.ID, 8)
	ov.Recent = a.Search(ctx, act, "project:"+pc.Key+" sort:created", 1, 8).Issues
	ov.Labels, _ = a.DB.LabelCounts(ctx, row.ID)
	return ov, nil
}

func (a *App) Trending(ctx context.Context, act *Actor, projectID int64, n int) []*store.Issue {
	sc, err := a.DB.Trending(ctx, time.Now().Add(-7*24*time.Hour).Unix(), projectID, n*3)
	if err != nil || len(sc) == 0 {
		return nil
	}
	ids := make([]int64, len(sc))
	for i, s := range sc {
		ids[i] = s.IssueID
	}
	m, _ := a.DB.IssuesByIDs(ctx, ids)
	var out []*store.Issue
	for _, s := range sc {
		if i := m[s.IssueID]; i != nil && a.CanSeeIssue(ctx, act, i) {
			out = append(out, i)
			if len(out) >= n {
				break
			}
		}
	}
	a.DB.LoadRelations(ctx, out)
	return out
}

func (a *App) Changelog(ctx context.Context, act *Actor, project, version string) (*store.Version, []*store.Issue, error) {
	pc := a.Cfg().Project(project)
	if pc == nil || !a.Can(act, "issue.view", pc.Key) {
		return nil, nil, ErrNotFound
	}
	row, err := a.DB.ProjectByKey(ctx, pc.Key)
	if err != nil {
		return nil, nil, ErrNotFound
	}
	v, err := a.DB.VersionByName(ctx, row.ID, version)
	if err != nil {
		return nil, nil, ErrNotFound
	}
	q := "project:" + pc.Key + ` fixed:"` + version + `" is:closed sort:key`
	res := a.Search(ctx, act, q, 1, 1000)
	return v, res.Issues, nil
}

type Reputation struct {
	Score      int            `json:"score"`
	Breakdown  map[string]int `json:"breakdown"`
	Reported   int            `json:"reported"`
	Fixed      int            `json:"fixed"`
	Duplicates int            `json:"duplicates_found"`
	Confirmed  int            `json:"confirmations"`
	Comments   int            `json:"comments"`
}

func (a *App) Reputation(ctx context.Context, userID int64) Reputation {
	cfg := a.Cfg()
	var invalid []string
	seen := map[string]bool{}
	for _, wf := range cfg.Workflows {
		for _, r := range wf.Resolutions {
			if !r.IsValid() && !seen[r.ID] {
				seen[r.ID] = true
				invalid = append(invalid, r.ID)
			}
		}
	}
	q := `SELECT COUNT(*) FROM issues WHERE reporter_id = ? AND moderation = 'approved' AND duplicate_of = 0
		AND (resolution <> '' OR state NOT IN (` + placeholdersN(len(initialStates(cfg))) + `))`
	args := []any{userID}
	for _, s := range initialStates(cfg) {
		args = append(args, s)
	}
	if len(invalid) > 0 {
		q += ` AND resolution NOT IN (` + placeholdersN(len(invalid)) + `)`
		for _, r := range invalid {
			args = append(args, r)
		}
	}
	r := Reputation{Breakdown: map[string]int{}}
	r.Reported = a.DB.Count(ctx, q, args...)
	r.Fixed = a.DB.Count(ctx, `SELECT COUNT(*) FROM issues WHERE reporter_id = ? AND resolution = 'fixed'`, userID)
	r.Duplicates = a.DB.Count(ctx, `SELECT COUNT(*) FROM history WHERE actor_id = ? AND field = 'duplicate'`, userID)
	r.Confirmed = a.DB.Count(ctx, `SELECT COUNT(*) FROM confirmations WHERE user_id = ?`, userID)
	r.Comments = a.DB.Count(ctx, `SELECT COUNT(*) FROM comments WHERE author_id = ? AND deleted = FALSE AND moderation = 'approved'`, userID)
	w := cfg.Reputation
	r.Breakdown["report_valid"] = r.Reported * w["report_valid"]
	r.Breakdown["report_fixed"] = r.Fixed * w["report_fixed"]
	r.Breakdown["duplicate_found"] = r.Duplicates * w["duplicate_found"]
	r.Breakdown["confirmation"] = r.Confirmed * w["confirmation"]
	r.Breakdown["comment"] = r.Comments * w["comment"]
	for _, v := range r.Breakdown {
		r.Score += v
	}
	return r
}

func initialStates(cfg *config.Config) []string {
	seen := map[string]bool{}
	var out []string
	for _, wf := range cfg.Workflows {
		for _, s := range append([]string{wf.Initial}, wf.StatesIn("waiting")...) {
			if !seen[s] {
				seen[s] = true
				out = append(out, s)
			}
		}
	}
	return out
}

func placeholdersN(n int) string {
	return strings.TrimSuffix(strings.Repeat("?, ", n), ", ")
}
