package server

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/kohanmathers/buggy/internal/app"
	"github.com/kohanmathers/buggy/internal/markdown"
	"github.com/kohanmathers/buggy/internal/search"
	"github.com/kohanmathers/buggy/internal/store"
)

func (s *Server) apiRoutes(mux *http.ServeMux) {
	h := func(pattern string, fn func(w http.ResponseWriter, r *http.Request) (any, int, error)) {
		mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
			v, status, err := fn(w, r)
			if err != nil {
				errJSON(w, err)
				return
			}
			if status == 0 {
				status = 200
			}
			writeJSON(w, status, v)
		})
	}
	h("GET /api/v1", s.apiIndex)
	h("GET /api/v1/{$}", s.apiIndex)
	h("GET /api/v1/me", s.apiMe)
	h("GET /api/v1/projects", s.apiProjects)
	h("GET /api/v1/projects/{key}", s.apiProject)
	h("GET /api/v1/projects/{key}/versions", s.apiVersions)
	h("POST /api/v1/projects/{key}/versions", s.apiVersionUpsert)
	h("GET /api/v1/issues", s.apiSearch)
	h("POST /api/v1/issues", s.apiCreate)
	h("GET /api/v1/issues/{key}", s.apiIssue)
	h("PATCH /api/v1/issues/{key}", s.apiPatch)
	h("DELETE /api/v1/issues/{key}", s.apiDelete)
	h("GET /api/v1/issues/{key}/transitions", s.apiTransitions)
	h("POST /api/v1/issues/{key}/transitions", s.apiTransition)
	h("POST /api/v1/issues/{key}/duplicate", s.apiDuplicate)
	h("GET /api/v1/issues/{key}/comments", s.apiComments)
	h("POST /api/v1/issues/{key}/comments", s.apiComment)
	h("PATCH /api/v1/comments/{id}", s.apiCommentEdit)
	h("DELETE /api/v1/comments/{id}", s.apiCommentDelete)
	h("GET /api/v1/issues/{key}/attachments", s.apiAttachments)
	h("POST /api/v1/issues/{key}/attachments", s.apiAttach)
	h("PUT /api/v1/issues/{key}/vote", s.apiVote(true))
	h("DELETE /api/v1/issues/{key}/vote", s.apiVote(false))
	h("PUT /api/v1/issues/{key}/watch", s.apiWatch(true))
	h("DELETE /api/v1/issues/{key}/watch", s.apiWatch(false))
	h("POST /api/v1/issues/{key}/confirmations", s.apiConfirm)
	h("GET /api/v1/issues/{key}/history", s.apiHistory)
	h("POST /api/v1/issues/{key}/links", s.apiLink)
	h("DELETE /api/v1/issues/{key}/links/{id}", s.apiUnlink)
	h("POST /api/v1/bulk", s.apiBulk)
	h("GET /api/v1/similar", s.apiSimilar)
	h("POST /api/v1/parse", s.apiParse)
	h("GET /api/v1/users", s.apiUsers)
	h("GET /api/v1/users/{name}", s.apiUser)
	h("GET /api/v1/users/{name}/moderation", s.apiModLog)
	h("POST /api/v1/users/{name}/moderation", s.apiModerate)
	h("GET /api/v1/notifications", s.apiNotifications)
	h("POST /api/v1/notifications/read", s.apiNotificationsRead)
	h("POST /api/v1/preview", s.apiPreview)
	h("GET /api/v1/search/keys", func(w http.ResponseWriter, r *http.Request) (any, int, error) { return search.Keys, 0, nil })
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 404, map[string]any{"error": "unknown API endpoint", "docs": "/api/v1"})
	})
}

func decode(r *http.Request, v any) error {
	dec := json.NewDecoder(io.LimitReader(r.Body, 8<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil && err != io.EOF {
		return app.Errf(400, "invalid JSON body: %v", err)
	}
	return nil
}

func iso(t int64) string {
	if t == 0 {
		return ""
	}
	return time.Unix(t, 0).UTC().Format(time.RFC3339)
}

func vnames(vs []*store.Version) []string {
	out := []string{}
	for _, v := range vs {
		out = append(out, v.Name)
	}
	return out
}

func (s *Server) issueJSON(r *http.Request, i *store.Issue) map[string]any {
	cfg := s.app.Cfg()
	pk := i.ProjectKey()
	wf := cfg.ProjectWorkflow(pk)
	st := map[string]any{"id": i.State}
	if x := wf.State(i.State); x != nil {
		st["label"], st["category"] = x.Label, x.Category
	}
	var res any
	if i.Resolution != "" {
		rm := map[string]any{"id": i.Resolution}
		if x := wf.Resolution(i.Resolution); x != nil {
			rm["label"] = x.Label
		}
		res = rm
	}
	user := func(u *store.User) any {
		if u == nil {
			return nil
		}
		return map[string]any{"username": u.Username, "name": u.Name()}
	}
	labels := i.Labels
	if labels == nil {
		labels = []string{}
	}
	fields := i.Fields
	if fields == nil {
		fields = map[string][]string{}
	}
	out := map[string]any{
		"key": i.Key, "number": i.Number, "project": pk, "url": s.app.IssueURL(i.Key),
		"title": i.Title, "description": i.Description, "state": st, "resolution": res,
		"reporter": user(i.Reporter), "assignee": user(i.Assignee), "labels": labels, "fields": fields,
		"affects_versions": vnames(i.Affects), "fix_versions": vnames(i.FixVersions),
		"votes": i.Votes, "watchers": i.Watchers, "comments": i.CommentCount, "confirmations": i.Confirmations,
		"private": i.Private, "fingerprint": i.Fingerprint, "created_at": iso(i.CreatedAt),
		"updated_at": iso(i.UpdatedAt), "resolved_at": iso(i.ResolvedAt),
	}
	if i.Moderation != "approved" {
		out["moderation"] = i.Moderation
	}
	if i.DuplicateOf != 0 {
		if o, err := s.app.DB.IssueByID(r.Context(), i.DuplicateOf); err == nil {
			out["duplicate_of"] = o.Key
		}
	}
	return out
}

func (s *Server) apiIndex(w http.ResponseWriter, r *http.Request) (any, int, error) {
	return map[string]any{
		"name":    s.app.Cfg().Instance.Name,
		"version": s.Version,
		"auth":    "Send 'Authorization: Bearer <token>' (create tokens under /settings). Cookie sessions also work with an X-CSRF-Token header.",
		"endpoints": []string{
			"GET /api/v1/me", "GET /api/v1/projects", "GET /api/v1/projects/{key}",
			"GET|POST /api/v1/projects/{key}/versions",
			"GET /api/v1/issues?q=&page=&per_page=", "POST /api/v1/issues",
			"GET|PATCH|DELETE /api/v1/issues/{key}", "GET|POST /api/v1/issues/{key}/transitions",
			"POST /api/v1/issues/{key}/duplicate", "GET|POST /api/v1/issues/{key}/comments",
			"PATCH|DELETE /api/v1/comments/{id}", "GET|POST /api/v1/issues/{key}/attachments",
			"PUT|DELETE /api/v1/issues/{key}/vote", "PUT|DELETE /api/v1/issues/{key}/watch",
			"POST /api/v1/issues/{key}/confirmations", "GET /api/v1/issues/{key}/history",
			"POST /api/v1/issues/{key}/links", "DELETE /api/v1/issues/{key}/links/{id}",
			"POST /api/v1/bulk", "GET /api/v1/similar?project=&title=", "POST /api/v1/parse",
			"GET /api/v1/users?prefix=", "GET /api/v1/users/{name}", "GET|POST /api/v1/users/{name}/moderation", "GET /api/v1/notifications",
			"POST /api/v1/notifications/read", "GET /api/v1/search/keys",
		},
	}, 0, nil
}

func (s *Server) apiMe(w http.ResponseWriter, r *http.Request) (any, int, error) {
	act := actorFrom(r)
	if !act.LoggedIn() {
		return nil, 0, app.ErrLoginNeeded
	}
	perms := []string{}
	for p := range s.app.Cfg().Perms(s.app.Role(act.User)) {
		perms = append(perms, p)
	}
	return map[string]any{"user": act.User, "role": s.app.Role(act.User), "permissions": perms,
		"unread_notifications": s.app.DB.UnreadCount(r.Context(), act.ID())}, 0, nil
}

func (s *Server) apiProjects(w http.ResponseWriter, r *http.Request) (any, int, error) {
	var out []map[string]any
	for _, p := range s.app.VisibleProjects(actorFrom(r)) {
		out = append(out, map[string]any{"key": p.Key, "name": p.Name, "description": p.Description, "url": s.app.Cfg().Instance.URL + "/projects/" + p.Key})
	}
	return out, 0, nil
}

func (s *Server) apiProject(w http.ResponseWriter, r *http.Request) (any, int, error) {
	key := strings.ToUpper(r.PathValue("key"))
	act := actorFrom(r)
	cfg := s.app.Cfg()
	p := cfg.Project(key)
	if p == nil || !s.app.Can(act, "issue.view", key) {
		return nil, 0, app.ErrNotFound
	}
	row, _ := s.app.DB.EnsureProject(r.Context(), key)
	vs, _ := s.app.DB.Versions(r.Context(), row.ID)
	return map[string]any{
		"key": p.Key, "name": p.Name, "description": p.Description,
		"workflow": cfg.ProjectWorkflow(key), "fields": cfg.ProjectFields(key), "form": cfg.ProjectForm(key),
		"versions": vs, "labels": cfg.Labels,
	}, 0, nil
}

func (s *Server) apiVersions(w http.ResponseWriter, r *http.Request) (any, int, error) {
	key := strings.ToUpper(r.PathValue("key"))
	if s.app.Cfg().Project(key) == nil || !s.app.Can(actorFrom(r), "issue.view", key) {
		return nil, 0, app.ErrNotFound
	}
	row, err := s.app.DB.EnsureProject(r.Context(), key)
	if err != nil {
		return nil, 0, err
	}
	vs, err := s.app.DB.Versions(r.Context(), row.ID)
	if vs == nil {
		vs = []*store.Version{}
	}
	return vs, 0, err
}

func (s *Server) apiVersionUpsert(w http.ResponseWriter, r *http.Request) (any, int, error) {
	key := strings.ToUpper(r.PathValue("key"))
	if s.app.Cfg().Project(key) == nil {
		return nil, 0, app.ErrNotFound
	}
	if !s.app.Can(actorFrom(r), "version.manage", key) {
		return nil, 0, app.ErrForbidden
	}
	var in struct {
		Name        string `json:"name"`
		Released    bool   `json:"released"`
		ReleaseDate string `json:"release_date"`
		Archived    bool   `json:"archived"`
		SortOrder   *int64 `json:"sort_order"`
	}
	if err := decode(r, &in); err != nil {
		return nil, 0, err
	}
	if strings.TrimSpace(in.Name) == "" {
		return nil, 0, app.Errf(422, "name is required")
	}
	row, err := s.app.DB.EnsureProject(r.Context(), key)
	if err != nil {
		return nil, 0, err
	}
	so := int64(-1)
	if in.SortOrder != nil {
		so = *in.SortOrder
	}
	v, err := s.app.DB.UpsertVersion(r.Context(), row.ID, store.Version{Name: strings.TrimSpace(in.Name), SortOrder: so,
		Released: in.Released, ReleaseDate: in.ReleaseDate, Archived: in.Archived})
	return v, 201, err
}

func (s *Server) apiSearch(w http.ResponseWriter, r *http.Request) (any, int, error) {
	q := r.URL.Query()
	page, _ := strconv.Atoi(q.Get("page"))
	per, _ := strconv.Atoi(q.Get("per_page"))
	if per <= 0 || per > 200 {
		per = 50
	}
	res := s.app.Search(r.Context(), actorFrom(r), q.Get("q"), page, per)
	if res.Error != "" {
		return nil, 0, app.Errf(400, "%s", res.Error)
	}
	list := make([]map[string]any, 0, len(res.Issues))
	for _, i := range res.Issues {
		list = append(list, s.issueJSON(r, i))
	}
	return map[string]any{"query": res.Query, "total": res.Total, "page": res.Page, "pages": res.Pages, "issues": list}, 0, nil
}

func (s *Server) apiCreate(w http.ResponseWriter, r *http.Request) (any, int, error) {
	var in struct {
		Project     string              `json:"project"`
		Title       string              `json:"title"`
		Description string              `json:"description"`
		Fields      map[string]any      `json:"fields"`
		Labels      []string            `json:"labels"`
		Affects     []string            `json:"affects_versions"`
		Private     bool                `json:"private"`
		Captcha     string              `json:"captcha"`
		Raw         map[string][]string `json:"-"`
	}
	if err := decode(r, &in); err != nil {
		return nil, 0, err
	}
	iss, err := s.app.CreateIssue(r.Context(), actorFrom(r), app.IssueInput{
		Project: in.Project, Title: in.Title, Description: in.Description, Fields: normFields(in.Fields),
		Labels: in.Labels, Affects: in.Affects, Private: in.Private, CaptchaToken: in.Captcha,
	})
	if err != nil {
		return nil, 0, err
	}
	return s.issueJSON(r, iss), 201, nil
}

func normFields(m map[string]any) map[string][]string {
	out := map[string][]string{}
	for k, v := range m {
		switch t := v.(type) {
		case nil:
			out[k] = nil
		case string:
			out[k] = []string{t}
		case bool:
			out[k] = []string{strconv.FormatBool(t)}
		case float64:
			out[k] = []string{strconv.FormatFloat(t, 'f', -1, 64)}
		case []any:
			for _, x := range t {
				if s, ok := x.(string); ok {
					out[k] = append(out[k], s)
				}
			}
			if out[k] == nil {
				out[k] = []string{}
			}
		}
	}
	return out
}

func (s *Server) apiIssue(w http.ResponseWriter, r *http.Request) (any, int, error) {
	ctx := r.Context()
	act := actorFrom(r)
	iss, err := s.app.GetIssue(ctx, act, r.PathValue("key"))
	if err != nil {
		return nil, 0, err
	}
	out := s.issueJSON(r, iss)
	out["explanation"] = s.app.Explain(ctx, iss)
	for _, e := range strings.Split(r.URL.Query().Get("expand"), ",") {
		switch e {
		case "comments":
			out["comments"], _ = s.app.Comments(ctx, act, iss)
		case "attachments":
			out["attachments"], _ = s.app.DB.Attachments(ctx, iss.ID)
		case "links":
			out["links"] = s.app.Links(ctx, act, iss)
		case "confirmations":
			confs, _ := s.app.DB.Confirmations(ctx, iss.ID)
			out["confirmations_list"] = confs
			out["environment"] = s.app.EnvSummary(iss, confs)
		case "commits":
			out["commits"], _ = s.app.DB.Commits(ctx, iss.ID)
		case "history":
			out["history"], _ = s.app.DB.History(ctx, iss.ID)
		}
	}
	return out, 0, nil
}

func (s *Server) apiPatch(w http.ResponseWriter, r *http.Request) (any, int, error) {
	var in struct {
		Title        *string        `json:"title"`
		Description  *string        `json:"description"`
		Fields       map[string]any `json:"fields"`
		Labels       *[]string      `json:"labels"`
		AddLabels    []string       `json:"add_labels"`
		RemoveLabels []string       `json:"remove_labels"`
		Affects      *[]string      `json:"affects_versions"`
		FixVersions  *[]string      `json:"fix_versions"`
		Assignee     *string        `json:"assignee"`
		Private      *bool          `json:"private"`
	}
	if err := decode(r, &in); err != nil {
		return nil, 0, err
	}
	iss, err := s.app.UpdateIssue(r.Context(), actorFrom(r), r.PathValue("key"), app.IssuePatch{
		Title: in.Title, Description: in.Description, Fields: normFields(in.Fields), Labels: in.Labels,
		AddLabels: in.AddLabels, RemoveLabels: in.RemoveLabels, Affects: in.Affects, FixVersions: in.FixVersions,
		Assignee: in.Assignee, Private: in.Private,
	})
	if err != nil {
		return nil, 0, err
	}
	return s.issueJSON(r, iss), 0, nil
}

func (s *Server) apiDelete(w http.ResponseWriter, r *http.Request) (any, int, error) {
	return map[string]any{"deleted": true}, 0, s.app.DeleteIssue(r.Context(), actorFrom(r), r.PathValue("key"))
}

func (s *Server) apiTransitions(w http.ResponseWriter, r *http.Request) (any, int, error) {
	act := actorFrom(r)
	iss, err := s.app.GetIssue(r.Context(), act, r.PathValue("key"))
	if err != nil {
		return nil, 0, err
	}
	ts := s.app.Transitions(act, iss)
	if ts == nil {
		ts = []app.AvailableTransition{}
	}
	return ts, 0, nil
}

func (s *Server) apiTransition(w http.ResponseWriter, r *http.Request) (any, int, error) {
	var in struct {
		Transition string `json:"transition"`
		Resolution string `json:"resolution"`
		Comment    string `json:"comment"`
	}
	if err := decode(r, &in); err != nil {
		return nil, 0, err
	}
	iss, err := s.app.Transition(r.Context(), actorFrom(r), r.PathValue("key"), in.Transition, in.Resolution, in.Comment)
	if err != nil {
		return nil, 0, err
	}
	return s.issueJSON(r, iss), 0, nil
}

func (s *Server) apiDuplicate(w http.ResponseWriter, r *http.Request) (any, int, error) {
	var in struct {
		Of      string `json:"of"`
		Comment string `json:"comment"`
	}
	if err := decode(r, &in); err != nil {
		return nil, 0, err
	}
	iss, err := s.app.MarkDuplicate(r.Context(), actorFrom(r), r.PathValue("key"), in.Of, in.Comment)
	if err != nil {
		return nil, 0, err
	}
	return s.issueJSON(r, iss), 0, nil
}

func (s *Server) apiComments(w http.ResponseWriter, r *http.Request) (any, int, error) {
	act := actorFrom(r)
	iss, err := s.app.GetIssue(r.Context(), act, r.PathValue("key"))
	if err != nil {
		return nil, 0, err
	}
	cs, err := s.app.Comments(r.Context(), act, iss)
	if cs == nil {
		cs = []*store.Comment{}
	}
	return cs, 0, err
}

func (s *Server) apiComment(w http.ResponseWriter, r *http.Request) (any, int, error) {
	var in struct {
		Body       string `json:"body"`
		Restricted bool   `json:"restricted"`
		Captcha    string `json:"captcha"`
	}
	if err := decode(r, &in); err != nil {
		return nil, 0, err
	}
	c, err := s.app.AddComment(r.Context(), actorFrom(r), r.PathValue("key"), in.Body, in.Restricted, in.Captcha)
	return c, 201, err
}

func (s *Server) apiCommentEdit(w http.ResponseWriter, r *http.Request) (any, int, error) {
	var in struct {
		Body string `json:"body"`
	}
	if err := decode(r, &in); err != nil {
		return nil, 0, err
	}
	c, err := s.app.EditComment(r.Context(), actorFrom(r), atoi64(r.PathValue("id")), in.Body)
	return c, 0, err
}

func (s *Server) apiCommentDelete(w http.ResponseWriter, r *http.Request) (any, int, error) {
	return map[string]any{"deleted": true}, 0, s.app.DeleteComment(r.Context(), actorFrom(r), atoi64(r.PathValue("id")))
}

func (s *Server) apiAttachments(w http.ResponseWriter, r *http.Request) (any, int, error) {
	iss, err := s.app.GetIssue(r.Context(), actorFrom(r), r.PathValue("key"))
	if err != nil {
		return nil, 0, err
	}
	atts, err := s.app.DB.Attachments(r.Context(), iss.ID)
	out := []map[string]any{}
	for _, a := range atts {
		out = append(out, map[string]any{"id": a.ID, "filename": a.Filename, "content_type": a.ContentType, "size": a.Size,
			"created_at": iso(a.CreatedAt), "url": s.app.Cfg().Instance.URL + "/attachments/" + strconv.FormatInt(a.ID, 10) + "/" + a.Filename})
	}
	return out, 0, err
}

func (s *Server) apiAttach(w http.ResponseWriter, r *http.Request) (any, int, error) {
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		return nil, 0, app.Errf(400, "expected multipart/form-data with a 'file' field")
	}
	ups, done := uploadsFrom(r, "file")
	defer done()
	if len(ups) == 0 {
		return nil, 0, app.Errf(400, "no 'file' field in upload")
	}
	var out []*store.Attachment
	for _, u := range ups {
		at, err := s.app.AddAttachment(r.Context(), actorFrom(r), r.PathValue("key"), u)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, at)
	}
	return out, 201, nil
}

func (s *Server) apiVote(on bool) func(http.ResponseWriter, *http.Request) (any, int, error) {
	return func(w http.ResponseWriter, r *http.Request) (any, int, error) {
		iss, err := s.app.Vote(r.Context(), actorFrom(r), r.PathValue("key"), on)
		if err != nil {
			return nil, 0, err
		}
		return map[string]any{"voted": on, "votes": iss.Votes}, 0, nil
	}
}

func (s *Server) apiWatch(on bool) func(http.ResponseWriter, *http.Request) (any, int, error) {
	return func(w http.ResponseWriter, r *http.Request) (any, int, error) {
		iss, err := s.app.Watch(r.Context(), actorFrom(r), r.PathValue("key"), on)
		if err != nil {
			return nil, 0, err
		}
		return map[string]any{"watching": on, "watchers": iss.Watchers}, 0, nil
	}
}

func (s *Server) apiConfirm(w http.ResponseWriter, r *http.Request) (any, int, error) {
	var in struct {
		Version string            `json:"version"`
		Env     map[string]string `json:"env"`
		Note    string            `json:"note"`
	}
	if err := decode(r, &in); err != nil {
		return nil, 0, err
	}
	iss, err := s.app.Confirm(r.Context(), actorFrom(r), r.PathValue("key"), in.Version, in.Env, in.Note)
	if err != nil {
		return nil, 0, err
	}
	return s.issueJSON(r, iss), 201, nil
}

func (s *Server) apiHistory(w http.ResponseWriter, r *http.Request) (any, int, error) {
	iss, err := s.app.GetIssue(r.Context(), actorFrom(r), r.PathValue("key"))
	if err != nil {
		return nil, 0, err
	}
	h, err := s.app.DB.History(r.Context(), iss.ID)
	if h == nil {
		h = []*store.HistoryEntry{}
	}
	return h, 0, err
}

func (s *Server) apiLink(w http.ResponseWriter, r *http.Request) (any, int, error) {
	var in struct {
		Other string `json:"other"`
		Kind  string `json:"kind"`
	}
	if err := decode(r, &in); err != nil {
		return nil, 0, err
	}
	return map[string]any{"linked": true}, 201, s.app.Link(r.Context(), actorFrom(r), r.PathValue("key"), in.Other, in.Kind)
}

func (s *Server) apiUnlink(w http.ResponseWriter, r *http.Request) (any, int, error) {
	return map[string]any{"deleted": true}, 0, s.app.Unlink(r.Context(), actorFrom(r), r.PathValue("key"), atoi64(r.PathValue("id")))
}

func (s *Server) apiBulk(w http.ResponseWriter, r *http.Request) (any, int, error) {
	var in struct {
		Keys         []string `json:"keys"`
		Transition   string   `json:"transition"`
		Resolution   string   `json:"resolution"`
		Comment      string   `json:"comment"`
		AddLabels    []string `json:"add_labels"`
		RemoveLabels []string `json:"remove_labels"`
		Assignee     *string  `json:"assignee"`
		DuplicateOf  string   `json:"duplicate_of"`
	}
	if err := decode(r, &in); err != nil {
		return nil, 0, err
	}
	res, err := s.app.Bulk(r.Context(), actorFrom(r), in.Keys, app.BulkAction{Transition: in.Transition, Resolution: in.Resolution,
		Comment: in.Comment, AddLabels: in.AddLabels, RemoveLabels: in.RemoveLabels, Assignee: in.Assignee, DuplicateOf: in.DuplicateOf})
	return map[string]any{"results": res}, 0, err
}

func (s *Server) apiSimilar(w http.ResponseWriter, r *http.Request) (any, int, error) {
	q := r.URL.Query()
	sims := s.app.Similar(r.Context(), actorFrom(r), q.Get("project"), q.Get("title"), q.Get("description"), atoi64(q.Get("exclude")))
	out := []map[string]any{}
	for _, si := range sims {
		j := s.issueJSON(r, si.Issue)
		j["score"], j["reason"] = si.Score, si.Reason
		delete(j, "description")
		out = append(out, j)
	}
	return out, 0, nil
}

func (s *Server) apiParse(w http.ResponseWriter, r *http.Request) (any, int, error) {
	var in struct {
		Project  string `json:"project"`
		Text     string `json:"text"`
		Filename string `json:"filename"`
	}
	if err := decode(r, &in); err != nil {
		return nil, 0, err
	}
	if s.app.Cfg().Project(in.Project) == nil {
		return nil, 0, app.Errf(404, "unknown project")
	}
	res := s.app.ParseText(strings.ToUpper(in.Project), in.Text, in.Filename)
	if res.Labels == nil {
		res.Labels = []string{}
	}
	if res.Matched == nil {
		res.Matched = []string{}
	}
	return res, 0, nil
}

func (s *Server) apiUsers(w http.ResponseWriter, r *http.Request) (any, int, error) {
	if !actorFrom(r).LoggedIn() {
		return nil, 0, app.ErrLoginNeeded
	}
	us, err := s.app.DB.SearchUsernames(r.Context(), r.URL.Query().Get("prefix"), 8)
	out := []map[string]any{}
	for _, u := range us {
		if u.ShadowBanned {
			continue
		}
		out = append(out, map[string]any{"username": u.Username, "name": u.Name(), "avatar_url": u.AvatarURL})
	}
	return out, 0, err
}

func (s *Server) apiUser(w http.ResponseWriter, r *http.Request) (any, int, error) {
	u, err := s.app.DB.UserByName(r.Context(), r.PathValue("name"))
	if err != nil || u.ShadowBanned {
		return nil, 0, app.ErrNotFound
	}
	return map[string]any{"user": u, "role": s.app.Role(u), "reputation": s.app.Reputation(r.Context(), u.ID)}, 0, nil
}

func (s *Server) apiNotifications(w http.ResponseWriter, r *http.Request) (any, int, error) {
	act := actorFrom(r)
	if !act.LoggedIn() {
		return nil, 0, app.ErrLoginNeeded
	}
	list, err := s.app.DB.Notifications(r.Context(), act.ID(), r.URL.Query().Get("unread") != "", 100)
	if list == nil {
		list = []*store.Notification{}
	}
	return map[string]any{"notifications": list, "unread": s.app.DB.UnreadCount(r.Context(), act.ID())}, 0, err
}

func (s *Server) apiNotificationsRead(w http.ResponseWriter, r *http.Request) (any, int, error) {
	act := actorFrom(r)
	if !act.LoggedIn() {
		return nil, 0, app.ErrLoginNeeded
	}
	var in struct {
		ID int64 `json:"id"`
	}
	decode(r, &in)
	return map[string]any{"ok": true}, 0, s.app.DB.MarkRead(r.Context(), act.ID(), in.ID)
}

func (s *Server) apiPreview(w http.ResponseWriter, r *http.Request) (any, int, error) {
	var in struct {
		Text string `json:"text"`
	}
	if err := decode(r, &in); err != nil {
		return nil, 0, err
	}
	cfg := s.app.Cfg()
	return map[string]any{"html": markdown.Render(in.Text, func(k string) bool { return cfg.Project(k) != nil })}, 0, nil
}

func (s *Server) apiModLog(w http.ResponseWriter, r *http.Request) (any, int, error) {
	act := actorFrom(r)
	u, err := s.app.DB.UserByName(r.Context(), r.PathValue("name"))
	if err != nil {
		return nil, 0, app.ErrNotFound
	}
	log, err := s.app.ModerationLog(r.Context(), act, u.ID)
	if err != nil {
		return nil, 0, err
	}
	if log == nil {
		log = []*store.ModLogEntry{}
	}
	return map[string]any{"status": accountStatus(u), "log": log}, 0, nil
}

func (s *Server) apiModerate(w http.ResponseWriter, r *http.Request) (any, int, error) {
	var in struct {
		Action   string `json:"action"`
		Duration string `json:"duration"`
		Reason   string `json:"reason"`
	}
	if err := decode(r, &in); err != nil {
		return nil, 0, err
	}
	dur, err := app.ParseModDuration(in.Duration)
	if err != nil {
		return nil, 0, err
	}
	u, err := s.app.ModerateUser(r.Context(), actorFrom(r), r.PathValue("name"), app.ModerationRequest{Action: in.Action, Duration: dur, Reason: in.Reason})
	if err != nil {
		return nil, 0, err
	}
	return map[string]any{"status": accountStatus(u)}, 0, nil
}

func accountStatus(u *store.User) map[string]any {
	st := map[string]any{"suspended": u.Suspended(), "locked": u.Locked(), "muted": u.Muted(),
		"shadow_banned": u.ShadowBanned, "trusted": u.Trusted}
	if u.Suspended() {
		st["ban_reason"] = u.BanReason
		if u.BannedUntil != 0 {
			st["locked_until"] = iso(u.BannedUntil)
		}
	}
	if u.Muted() {
		st["mute_reason"] = u.MuteReason
		if u.MutedUntil != store.Forever {
			st["muted_until"] = iso(u.MutedUntil)
		}
	}
	return st
}
