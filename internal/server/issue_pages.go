package server

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"github.com/kohanmathers/buggy/internal/app"
	"github.com/kohanmathers/buggy/internal/config"
	"github.com/kohanmathers/buggy/internal/store"
)

type timelineItem struct {
	Time    int64
	Comment *store.Comment
	History *store.HistoryEntry
}

func (s *Server) issue(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	act := actorFrom(r)
	iss, err := s.app.GetIssue(ctx, act, r.PathValue("key"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if wantsJSON(r) {
		writeJSON(w, 200, s.issueJSON(r, iss))
		return
	}
	cfg := s.app.Cfg()
	pk := iss.ProjectKey()
	pc := cfg.Project(pk)
	comments, _ := s.app.Comments(ctx, act, iss)
	history, _ := s.app.DB.History(ctx, iss.ID)
	atts, _ := s.app.DB.Attachments(ctx, iss.ID)
	confs, _ := s.app.DB.Confirmations(ctx, iss.ID)
	commits, _ := s.app.DB.Commits(ctx, iss.ID)
	versions, _ := s.app.DB.Versions(ctx, iss.ProjectID)
	var timeline []timelineItem
	for _, c := range comments {
		timeline = append(timeline, timelineItem{Time: c.CreatedAt, Comment: c})
	}
	for _, h := range history {
		if h.Field == "comment_deleted" {
			continue
		}
		timeline = append(timeline, timelineItem{Time: h.CreatedAt, History: h})
	}
	sort.SliceStable(timeline, func(i, j int) bool { return timeline[i].Time < timeline[j].Time })

	data := map[string]any{
		"Issue":         iss,
		"Project":       pc,
		"Explain":       s.app.Explain(ctx, iss),
		"Timeline":      timeline,
		"CommentCount":  len(comments),
		"Attachments":   atts,
		"Links":         s.app.Links(ctx, act, iss),
		"Confirmations": confs,
		"Commits":       commits,
		"Env":           s.app.EnvSummary(iss, confs),
		"Transitions":   s.app.Transitions(act, iss),
		"Versions":      versions,
		"Fields":        cfg.ProjectFields(pk),
		"Canned":        cfg.Canned,
		"LinkKinds":     []string{"relates", "blocks", "causes", "clones"},
		"Closed":        iss.ResolvedAt != 0,
	}
	var envFields []*config.Field
	for _, f := range cfg.ProjectFields(pk) {
		if f.Environment {
			envFields = append(envFields, f)
		}
	}
	data["EnvFields"] = envFields
	if act.LoggedIn() {
		data["Voted"] = s.app.DB.HasVoted(ctx, iss.ID, act.ID())
		data["Watching"] = s.app.DB.IsWatching(ctx, iss.ID, act.ID())
		s.app.DB.MarkIssueRead(ctx, act.ID(), iss.ID)
	}
	if iss.DuplicateOf != 0 {
		if o, err := s.app.DB.IssueByID(ctx, iss.DuplicateOf); err == nil && s.app.CanSeeIssue(ctx, act, o) {
			data["Original"] = o
		}
	}
	if iss.Fingerprint != "" {
		sib, _ := s.app.DB.IssuesByFingerprint(ctx, iss.ProjectID, iss.Fingerprint, iss.ID, 10)
		var vis []*store.Issue
		for _, x := range sib {
			if s.app.CanSeeIssue(ctx, act, x) {
				vis = append(vis, x)
			}
		}
		data["CrashSiblings"] = vis
	}
	if iss.ResolvedAt == 0 && s.app.Can(act, "triage", pk) {
		var sims []app.SimilarIssue
		for _, si := range s.app.Similar(ctx, act, pk, iss.Title, "", iss.ID) {
			if si.Issue.ID != iss.ID {
				sims = append(sims, si)
			}
		}
		if len(sims) > 5 {
			sims = sims[:5]
		}
		data["Similar"] = sims
	}
	p := s.newPage(w, r, "["+iss.Key+"] "+iss.Title, data)
	p.NoIndex = iss.Private || iss.Moderation != "approved"
	s.render(w, r, 200, "issue", p)
}

func (s *Server) issueKeyAction(w http.ResponseWriter, r *http.Request, iss *store.Issue, err error, okMsg string) {
	if err != nil {
		var ae *app.Error
		if errors.As(err, &ae) && ae.Status != 500 && !wantsJSON(r) && ae.Status != 401 {
			s.flash(w, r, "error", ae.Msg)
			back(w, r, "/issues/"+r.PathValue("key"))
			return
		}
		s.fail(w, r, err)
		return
	}
	if wantsJSON(r) {
		if iss == nil {
			writeJSON(w, 200, map[string]any{"ok": true})
		} else {
			writeJSON(w, 200, s.issueJSON(r, iss))
		}
		return
	}
	if okMsg != "" {
		s.flash(w, r, "ok", okMsg)
	}
	back(w, r, "/issues/"+r.PathValue("key"))
}

func (s *Server) newIssuePick(w http.ResponseWriter, r *http.Request) {
	act := actorFrom(r)
	if !act.LoggedIn() {
		s.fail(w, r, app.ErrLoginNeeded)
		return
	}
	var ps []config.Project
	for _, p := range s.app.VisibleProjects(act) {
		if s.app.Can(act, "issue.create", p.Key) {
			ps = append(ps, p)
		}
	}
	if len(ps) == 1 {
		http.Redirect(w, r, "/projects/"+ps[0].Key+"/new", http.StatusSeeOther)
		return
	}
	s.render(w, r, 200, "new_pick", s.newPage(w, r, "Report a bug", ps))
}

type formView struct {
	Project  *config.Project
	Form     *config.Form
	Versions []*store.Version
	Labels   []config.Label
	Fields   map[string]*config.Field
	Steps    []formStepView
}

type formStepView struct {
	Title, Description string
	Fields             []formFieldView
}

type formFieldView struct {
	FF       config.FormField
	Field    *config.Field
	Builtin  string
	Label    string
	Help     string
	Required bool
	ShowIf   string
}

func (s *Server) buildForm(r *http.Request, pk string) (*formView, error) {
	cfg := s.app.Cfg()
	act := actorFrom(r)
	pc := cfg.Project(pk)
	if pc == nil || !s.app.Can(act, "issue.view", pk) {
		return nil, app.ErrNotFound
	}
	row, err := s.app.DB.EnsureProject(r.Context(), pc.Key)
	if err != nil {
		return nil, err
	}
	vers, _ := s.app.DB.Versions(r.Context(), row.ID)
	var active []*store.Version
	for i := len(vers) - 1; i >= 0; i-- {
		if !vers[i].Archived {
			active = append(active, vers[i])
		}
	}
	fv := &formView{Project: pc, Form: cfg.ProjectForm(pk), Versions: active, Fields: map[string]*config.Field{}}
	for _, l := range cfg.Labels {
		if !l.Restricted || s.app.Can(act, "issue.label", pk) {
			fv.Labels = append(fv.Labels, l)
		}
	}
	for _, step := range fv.Form.Steps {
		sv := formStepView{Title: step.Title, Description: step.Description}
		for _, ff := range step.Fields {
			v := formFieldView{FF: ff, Label: ff.Label, Help: ff.Help}
			if f := cfg.Field(ff.Field); f != nil {
				if f.EditPermission != "" && !s.app.Can(act, f.EditPermission, pk) {
					continue
				}
				v.Field = f
				fv.Fields[f.ID] = f
				v.Required = f.Required
				if v.Label == "" {
					v.Label = f.Label
				}
				if v.Help == "" {
					v.Help = f.Help
				}
			} else {
				v.Builtin = ff.Field
				v.Required = ff.Field == "title"
				if v.Label == "" {
					v.Label = map[string]string{"title": "Summary", "description": "Description", "affects_versions": "Affected version",
						"attachments": "Attachments", "labels": "Labels", "private": "Private / security report"}[ff.Field]
				}
			}
			if ff.Required != nil {
				v.Required = *ff.Required
			}
			if ff.ShowIf != nil {
				vals := append([]string{}, ff.ShowIf.In...)
				if ff.ShowIf.Equals != "" {
					vals = append([]string{ff.ShowIf.Equals}, vals...)
				}
				if ff.ShowIf.NotEmpty {
					vals = []string{"*"}
				}
				v.ShowIf = ff.ShowIf.Field + "=" + strings.Join(vals, "|")
			}
			sv.Fields = append(sv.Fields, v)
		}
		fv.Steps = append(fv.Steps, sv)
	}
	return fv, nil
}

func (s *Server) newIssueForm(w http.ResponseWriter, r *http.Request) {
	act := actorFrom(r)
	if !act.LoggedIn() {
		s.fail(w, r, app.ErrLoginNeeded)
		return
	}
	pk := strings.ToUpper(r.PathValue("key"))
	if !s.app.Can(act, "issue.create", pk) {
		s.fail(w, r, app.ErrForbidden)
		return
	}
	fv, err := s.buildForm(r, pk)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	p := s.newPage(w, r, "Report a bug · "+fv.Project.Name, fv)
	p.Form = r.URL.Query()
	s.render(w, r, 200, "new_issue", p)
}

func formFields(r *http.Request, fields []*config.Field) map[string][]string {
	out := map[string][]string{}
	for _, f := range fields {
		if v, ok := r.PostForm["f_"+f.ID]; ok {
			out[f.ID] = v
		} else if f.Type == "bool" && r.Form.Has("f_"+f.ID+"_present") {
			out[f.ID] = []string{"false"}
		}
	}
	return out
}

func (s *Server) parseForm(r *http.Request) error {
	ct := r.Header.Get("Content-Type")
	if strings.HasPrefix(ct, "multipart/") {
		return r.ParseMultipartForm(32 << 20)
	}
	return r.ParseForm()
}

func uploadsFrom(r *http.Request, field string) ([]app.Upload, func()) {
	var ups []app.Upload
	var closers []io.Closer
	if r.MultipartForm != nil {
		for _, fh := range r.MultipartForm.File[field] {
			f, err := fh.Open()
			if err != nil {
				continue
			}
			closers = append(closers, f)
			ups = append(ups, app.Upload{Name: fh.Filename, ContentType: fh.Header.Get("Content-Type"), Size: fh.Size, R: f})
		}
	}
	return ups, func() {
		for _, c := range closers {
			c.Close()
		}
	}
}

func (s *Server) newIssueSubmit(w http.ResponseWriter, r *http.Request) {
	act := actorFrom(r)
	pk := strings.ToUpper(r.PathValue("key"))
	if err := s.parseForm(r); err != nil {
		s.fail(w, r, app.Errf(400, "could not read form: %v", err))
		return
	}
	ups, done := uploadsFrom(r, "attachments")
	defer done()
	in := app.IssueInput{
		Project:      pk,
		Title:        r.FormValue("title"),
		Description:  r.FormValue("description"),
		Fields:       formFields(r, s.app.Cfg().ProjectFields(pk)),
		Labels:       r.Form["labels"],
		Affects:      r.Form["affects_versions"],
		Private:      formBool(r, "private"),
		CaptchaToken: captchaToken(r),
		Attachments:  ups,
	}
	iss, err := s.app.CreateIssue(r.Context(), act, in)
	if err != nil {
		var ae *app.Error
		if errors.As(err, &ae) && (ae.Fields != nil || ae.Status == 413 || ae.Status == 415 || ae.Status == 429 || ae.Status == 400) && !wantsJSON(r) {
			fv, ferr := s.buildForm(r, pk)
			if ferr != nil {
				s.fail(w, r, ferr)
				return
			}
			p := s.newPage(w, r, "Report a bug", fv)
			p.Errors = ae.Fields
			if p.Errors == nil {
				p.Errors = map[string]string{"_": ae.Msg}
			}
			p.Form = r.Form
			s.render(w, r, 422, "new_issue", p)
			return
		}
		s.fail(w, r, err)
		return
	}
	if wantsJSON(r) {
		writeJSON(w, 201, s.issueJSON(r, iss))
		return
	}
	msg := "Thanks! Your report was filed as " + iss.Key + "."
	if iss.Moderation == "pending" {
		msg = "Thanks! Your report " + iss.Key + " will be visible once a moderator approves it."
	}
	s.flash(w, r, "ok", msg)
	http.Redirect(w, r, "/issues/"+iss.Key, http.StatusSeeOther)
}

func captchaToken(r *http.Request) string {
	for _, k := range []string{"cf-turnstile-response", "h-captcha-response", "g-recaptcha-response", "captcha"} {
		if v := r.FormValue(k); v != "" {
			return v
		}
	}
	return ""
}

func (s *Server) editIssueForm(w http.ResponseWriter, r *http.Request) {
	act := actorFrom(r)
	iss, err := s.app.GetIssue(r.Context(), act, r.PathValue("key"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	pk := iss.ProjectKey()
	if !s.app.Can(act, "issue.edit", pk) && !(iss.ReporterID == act.ID() && s.app.Can(act, "issue.edit_own", pk)) && !s.app.Can(act, "triage", pk) {
		s.fail(w, r, app.ErrForbidden)
		return
	}
	s.render(w, r, 200, "edit_issue", s.newPage(w, r, "Edit "+iss.Key, s.editData(r, iss)))
}

func (s *Server) editData(r *http.Request, iss *store.Issue) map[string]any {
	versions, _ := s.app.DB.Versions(r.Context(), iss.ProjectID)
	cfg := s.app.Cfg()
	return map[string]any{"Issue": iss, "Fields": cfg.ProjectFields(iss.ProjectKey()), "Versions": versions, "Labels": cfg.Labels}
}

func (s *Server) editIssueSubmit(w http.ResponseWriter, r *http.Request) {
	act := actorFrom(r)
	ctx := r.Context()
	key := r.PathValue("key")
	iss, err := s.app.GetIssue(ctx, act, key)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	r.ParseForm()
	pk := iss.ProjectKey()
	canEdit := s.app.Can(act, "issue.edit", pk) || (iss.ReporterID == act.ID() && s.app.Can(act, "issue.edit_own", pk))
	var p app.IssuePatch
	if canEdit {
		t, d := r.FormValue("title"), r.FormValue("description")
		p.Title, p.Description = &t, &d
		af := r.Form["affects_versions"]
		if af == nil {
			af = []string{}
		}
		p.Affects = &af
	}
	var fields []*config.Field
	for _, f := range s.app.Cfg().ProjectFields(pk) {
		if canEdit || (f.EditPermission != "" && s.app.Can(act, f.EditPermission, pk)) {
			fields = append(fields, f)
		}
	}
	p.Fields = formFields(r, fields)
	for _, f := range fields {
		if _, ok := p.Fields[f.ID]; !ok {
			p.Fields[f.ID] = nil
		}
	}
	if s.app.Can(act, "issue.label", pk) || canEdit {
		ls := r.Form["labels"]
		ls = append(ls, splitList(r.FormValue("labels_extra"))...)
		if ls == nil {
			ls = []string{}
		}
		p.Labels = &ls
	}
	if s.app.Can(act, "issue.edit", pk) || s.app.Can(act, "version.manage", pk) {
		fx := r.Form["fix_versions"]
		if fx == nil {
			fx = []string{}
		}
		p.FixVersions = &fx
	}
	if s.app.Can(act, "issue.assign", pk) && r.Form.Has("assignee") {
		a := r.FormValue("assignee")
		p.Assignee = &a
	}
	if r.Form.Has("private_present") && (s.app.Can(act, "issue.edit", pk) || s.app.Can(act, "issue.view_private", pk) || iss.ReporterID == act.ID()) {
		v := formBool(r, "private")
		p.Private = &v
	}
	if _, err := s.app.UpdateIssue(ctx, act, key, p); err != nil {
		var ae *app.Error
		if errors.As(err, &ae) && ae.Fields != nil {
			pg := s.newPage(w, r, "Edit "+iss.Key, s.editData(r, iss))
			pg.Errors, pg.Form = ae.Fields, r.Form
			s.render(w, r, 422, "edit_issue", pg)
			return
		}
		s.fail(w, r, err)
		return
	}
	s.flash(w, r, "ok", "Saved")
	http.Redirect(w, r, "/issues/"+iss.Key, http.StatusSeeOther)
}

func (s *Server) commentSubmit(w http.ResponseWriter, r *http.Request) {
	act := actorFrom(r)
	c, err := s.app.AddComment(r.Context(), act, r.PathValue("key"), r.FormValue("body"), formBool(r, "restricted"), captchaToken(r))
	if err == nil && wantsJSON(r) {
		writeJSON(w, 201, c)
		return
	}
	if err == nil {
		msg := ""
		if c.Moderation == "pending" {
			msg = "Your comment will appear after moderator approval."
		}
		if msg != "" {
			s.flash(w, r, "ok", msg)
		}
		http.Redirect(w, r, fmt.Sprintf("/issues/%s#comment-%d", r.PathValue("key"), c.ID), http.StatusSeeOther)
		return
	}
	s.issueKeyAction(w, r, nil, err, "")
}

func (s *Server) transitionSubmit(w http.ResponseWriter, r *http.Request) {
	iss, err := s.app.Transition(r.Context(), actorFrom(r), r.PathValue("key"), r.FormValue("transition"), r.FormValue("resolution"), r.FormValue("comment"))
	s.issueKeyAction(w, r, iss, err, "")
}

func (s *Server) duplicateSubmit(w http.ResponseWriter, r *http.Request) {
	orig := strings.ToUpper(strings.TrimSpace(r.FormValue("of")))
	if i := strings.LastIndex(orig, "/ISSUES/"); i >= 0 {
		orig = orig[i+8:]
	}
	iss, err := s.app.MarkDuplicate(r.Context(), actorFrom(r), r.PathValue("key"), orig, r.FormValue("comment"))
	s.issueKeyAction(w, r, iss, err, "Marked as duplicate of "+orig)
}

func (s *Server) voteSubmit(w http.ResponseWriter, r *http.Request) {
	on := r.FormValue("on") != "0"
	iss, err := s.app.Vote(r.Context(), actorFrom(r), r.PathValue("key"), on)
	s.issueKeyAction(w, r, iss, err, "")
}

func (s *Server) watchSubmit(w http.ResponseWriter, r *http.Request) {
	on := r.FormValue("on") != "0"
	iss, err := s.app.Watch(r.Context(), actorFrom(r), r.PathValue("key"), on)
	s.issueKeyAction(w, r, iss, err, "")
}

func (s *Server) confirmSubmit(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	env := map[string]string{}
	for k, v := range r.PostForm {
		if strings.HasPrefix(k, "f_") && len(v) > 0 {
			env[strings.TrimPrefix(k, "f_")] = v[0]
		}
	}
	iss, err := s.app.Confirm(r.Context(), actorFrom(r), r.PathValue("key"), r.FormValue("version"), env, r.FormValue("note"))
	s.issueKeyAction(w, r, iss, err, "Thanks, your confirmation was recorded.")
}

func (s *Server) linkSubmit(w http.ResponseWriter, r *http.Request) {
	err := s.app.Link(r.Context(), actorFrom(r), r.PathValue("key"), strings.ToUpper(strings.TrimSpace(r.FormValue("other"))), r.FormValue("kind"))
	s.issueKeyAction(w, r, nil, err, "Linked")
}

func (s *Server) unlinkSubmit(w http.ResponseWriter, r *http.Request) {
	err := s.app.Unlink(r.Context(), actorFrom(r), r.PathValue("key"), atoi64(r.PathValue("id")))
	s.issueKeyAction(w, r, nil, err, "Link removed")
}

func (s *Server) attachSubmit(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		s.issueKeyAction(w, r, nil, app.Errf(400, "upload failed: %v", err), "")
		return
	}
	ups, done := uploadsFrom(r, "file")
	defer done()
	if len(ups) == 0 {
		s.issueKeyAction(w, r, nil, app.Errf(400, "no file selected"), "")
		return
	}
	var last *store.Attachment
	for _, u := range ups {
		at, err := s.app.AddAttachment(r.Context(), actorFrom(r), r.PathValue("key"), u)
		if err != nil {
			s.issueKeyAction(w, r, nil, err, "")
			return
		}
		last = at
	}
	if wantsJSON(r) {
		writeJSON(w, 201, last)
		return
	}
	s.issueKeyAction(w, r, nil, nil, "Uploaded")
}

func (s *Server) cannedSubmit(w http.ResponseWriter, r *http.Request) {
	iss, err := s.app.ApplyCanned(r.Context(), actorFrom(r), r.PathValue("key"), r.FormValue("canned"), r.FormValue("extra"))
	s.issueKeyAction(w, r, iss, err, "")
}

func (s *Server) triageSubmit(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	act := actorFrom(r)
	var p app.IssuePatch
	if v := r.FormValue("add_label"); v != "" {
		p.AddLabels = splitList(v)
	}
	if v := r.FormValue("remove_label"); v != "" {
		p.RemoveLabels = []string{v}
	}
	if r.Form.Has("assignee") {
		v := r.FormValue("assignee")
		p.Assignee = &v
	}
	if r.Form.Has("fix_versions_present") {
		fx := r.Form["fix_versions"]
		if fx == nil {
			fx = []string{}
		}
		p.FixVersions = &fx
	}
	var fields []*config.Field
	for _, f := range s.app.Cfg().Fields {
		if r.Form.Has("f_" + f.ID) {
			f := f
			fields = append(fields, &f)
		}
	}
	if len(fields) > 0 {
		p.Fields = formFields(r, fields)
	}
	iss, err := s.app.UpdateIssue(r.Context(), act, r.PathValue("key"), p)
	s.issueKeyAction(w, r, iss, err, "")
}

func (s *Server) deleteIssueSubmit(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	if err := s.app.DeleteIssue(r.Context(), actorFrom(r), key); err != nil {
		s.fail(w, r, err)
		return
	}
	s.flash(w, r, "ok", key+" was deleted")
	http.Redirect(w, r, "/issues", http.StatusSeeOther)
}

func (s *Server) commentEdit(w http.ResponseWriter, r *http.Request) {
	c, err := s.app.EditComment(r.Context(), actorFrom(r), atoi64(r.PathValue("id")), r.FormValue("body"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if wantsJSON(r) {
		writeJSON(w, 200, c)
		return
	}
	back(w, r, "/issues")
}

func (s *Server) commentDelete(w http.ResponseWriter, r *http.Request) {
	if err := s.app.DeleteComment(r.Context(), actorFrom(r), atoi64(r.PathValue("id"))); err != nil {
		s.fail(w, r, err)
		return
	}
	if wantsJSON(r) {
		writeJSON(w, 200, map[string]any{"ok": true})
		return
	}
	back(w, r, "/issues")
}

func (s *Server) attachment(w http.ResponseWriter, r *http.Request) {
	at, rc, err := s.app.OpenAttachment(r.Context(), actorFrom(r), atoi64(r.PathValue("id")))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	defer rc.Close()
	ct := at.ContentType
	inline := strings.HasPrefix(ct, "image/") && !strings.Contains(ct, "svg") || strings.HasPrefix(ct, "text/plain")
	if !inline {
		ct = "application/octet-stream"
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Content-Security-Policy", "default-src 'none'; img-src 'self'; style-src 'unsafe-inline'; sandbox")
	disp := "attachment"
	if inline && r.URL.Query().Get("download") == "" {
		disp = "inline"
	}
	w.Header().Set("Content-Disposition", disp+"; filename*=UTF-8''"+url.PathEscape(at.Filename))
	w.Header().Set("Cache-Control", "private, max-age=3600")
	io.Copy(w, rc)
}

func (s *Server) attachmentDelete(w http.ResponseWriter, r *http.Request) {
	if err := s.app.DeleteAttachment(r.Context(), actorFrom(r), atoi64(r.PathValue("id"))); err != nil {
		s.fail(w, r, err)
		return
	}
	s.flash(w, r, "ok", "Attachment deleted")
	back(w, r, "/issues")
}

func (s *Server) bulk(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	keys := r.Form["keys"]
	keys = append(keys, splitList(r.FormValue("key_list"))...)
	b := app.BulkAction{
		Transition:   r.FormValue("transition"),
		Resolution:   r.FormValue("resolution"),
		Comment:      r.FormValue("comment"),
		AddLabels:    splitList(r.FormValue("add_labels")),
		RemoveLabels: splitList(r.FormValue("remove_labels")),
		DuplicateOf:  strings.ToUpper(strings.TrimSpace(r.FormValue("duplicate_of"))),
	}
	if r.Form.Has("assignee") && r.FormValue("assignee") != "" {
		a := r.FormValue("assignee")
		b.Assignee = &a
	}
	if c := r.FormValue("canned"); c != "" {
		var out []app.BulkResult
		for _, k := range keys {
			res := app.BulkResult{Key: k}
			if _, err := s.app.ApplyCanned(r.Context(), actorFrom(r), k, c, ""); err != nil {
				res.Error = err.Error()
			}
			out = append(out, res)
		}
		s.bulkDone(w, r, out)
		return
	}
	res, err := s.app.Bulk(r.Context(), actorFrom(r), keys, b)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.bulkDone(w, r, res)
}

func (s *Server) bulkDone(w http.ResponseWriter, r *http.Request, res []app.BulkResult) {
	if wantsJSON(r) {
		writeJSON(w, 200, map[string]any{"results": res})
		return
	}
	failed := 0
	var msgs []string
	for _, x := range res {
		if x.Error != "" {
			failed++
			if len(msgs) < 3 {
				msgs = append(msgs, x.Key+": "+x.Error)
			}
		}
	}
	if failed > 0 {
		s.flash(w, r, "error", fmt.Sprintf("%d of %d failed: %s", failed, len(res), strings.Join(msgs, "; ")))
	} else {
		s.flash(w, r, "ok", fmt.Sprintf("Updated %d issues", len(res)))
	}
	back(w, r, "/issues")
}
