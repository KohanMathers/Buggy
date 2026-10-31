package server

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path"
	"regexp"
	"strconv"
	"strings"

	"github.com/kohanmathers/buggy/internal/app"
	"github.com/kohanmathers/buggy/internal/search"
	"github.com/kohanmathers/buggy/internal/store"
)

func (s *Server) routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /{$}", s.home)
	mux.HandleFunc("GET /static/", s.static)
	mux.HandleFunc("GET /custom.css", s.customCSS)
	mux.HandleFunc("GET /favicon.svg", s.favicon)
	mux.HandleFunc("GET /healthz", s.health)
	mux.HandleFunc("GET /metrics", s.metricsHandler)
	mux.HandleFunc("GET /robots.txt", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "User-agent: *\nDisallow: /api/\nDisallow: /attachments/\nDisallow: /settings\n")
	})
	mux.HandleFunc("GET /help", s.help)

	mux.HandleFunc("GET /projects/{key}", s.project)
	mux.HandleFunc("GET /projects/{key}/changelog/{version}", s.changelog)
	mux.HandleFunc("GET /projects/{key}/releases", s.releases)
	mux.HandleFunc("GET /new", s.newIssuePick)
	mux.HandleFunc("GET /projects/{key}/new", s.newIssueForm)
	mux.HandleFunc("POST /projects/{key}/new", s.newIssueSubmit)

	mux.HandleFunc("GET /issues", s.issues)
	mux.HandleFunc("POST /issues/bulk", s.bulk)
	mux.HandleFunc("GET /issues/{key}", s.issue)
	mux.HandleFunc("GET /issues/{key}/edit", s.editIssueForm)
	mux.HandleFunc("POST /issues/{key}/edit", s.editIssueSubmit)
	mux.HandleFunc("POST /issues/{key}/comment", s.commentSubmit)
	mux.HandleFunc("POST /issues/{key}/transition", s.transitionSubmit)
	mux.HandleFunc("POST /issues/{key}/duplicate", s.duplicateSubmit)
	mux.HandleFunc("POST /issues/{key}/vote", s.voteSubmit)
	mux.HandleFunc("POST /issues/{key}/watch", s.watchSubmit)
	mux.HandleFunc("POST /issues/{key}/confirm", s.confirmSubmit)
	mux.HandleFunc("POST /issues/{key}/link", s.linkSubmit)
	mux.HandleFunc("POST /issues/{key}/unlink/{id}", s.unlinkSubmit)
	mux.HandleFunc("POST /issues/{key}/attach", s.attachSubmit)
	mux.HandleFunc("POST /issues/{key}/canned", s.cannedSubmit)
	mux.HandleFunc("POST /issues/{key}/triage", s.triageSubmit)
	mux.HandleFunc("POST /issues/{key}/delete", s.deleteIssueSubmit)
	mux.HandleFunc("POST /comments/{id}/edit", s.commentEdit)
	mux.HandleFunc("POST /comments/{id}/delete", s.commentDelete)
	mux.HandleFunc("GET /attachments/{id}/{name}", s.attachment)
	mux.HandleFunc("POST /attachments/{id}/delete", s.attachmentDelete)

	mux.HandleFunc("GET /login", s.loginForm)
	mux.HandleFunc("POST /login", s.loginSubmit)
	mux.HandleFunc("POST /logout", s.logout)
	mux.HandleFunc("GET /forgot", s.forgotForm)
	mux.HandleFunc("POST /forgot", s.forgotSubmit)
	mux.HandleFunc("GET /register", s.registerForm)
	mux.HandleFunc("POST /register", s.registerSubmit)
	mux.HandleFunc("GET /auth/{provider}", s.oauthStart)
	mux.HandleFunc("GET /auth/{provider}/callback", s.oauthCallback)

	mux.HandleFunc("GET /users/{name}", s.profile)
	mux.HandleFunc("POST /users/{name}/admin", s.userAdminSubmit)
	mux.HandleFunc("POST /users/{name}/moderate", s.userModerateSubmit)
	mux.HandleFunc("GET /settings", s.settings)
	mux.HandleFunc("POST /settings", s.settingsSubmit)
	mux.HandleFunc("POST /settings/tokens", s.tokenCreate)
	mux.HandleFunc("POST /settings/recovery-codes", s.recoveryCodesCreate)
	mux.HandleFunc("POST /settings/tokens/{id}/delete", s.tokenDelete)
	mux.HandleFunc("GET /notifications", s.notifications)
	mux.HandleFunc("POST /notifications/read", s.notificationsRead)
	mux.HandleFunc("POST /searches", s.saveSearch)
	mux.HandleFunc("POST /searches/{id}/delete", s.deleteSearch)

	mux.HandleFunc("GET /triage", s.triage)
	mux.HandleFunc("POST /moderate/{kind}/{id}", s.moderate)
	mux.HandleFunc("GET /admin", s.admin)
	mux.HandleFunc("POST /admin/reload", s.adminReload)
	mux.HandleFunc("POST /admin/automations/run", s.adminRunAutomations)
	mux.HandleFunc("POST /admin/versions", s.adminVersion)

	mux.HandleFunc("GET /feeds/issues.atom", s.feedSearch)
	mux.HandleFunc("GET /feeds/projects/{key}", s.feedProject)
	mux.HandleFunc("GET /feeds/issues/{key}", s.feedIssue)
	mux.HandleFunc("POST /hooks/git/{id}", s.gitHook)
}

func (s *Server) static(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
	f, err := fs.ReadFile(s.webFS(), name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if ct := mime.TypeByExtension(path.Ext(name)); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	switch {
	case s.app.Cfg().Server.Dev:
		w.Header().Set("Cache-Control", "no-cache")
	case r.URL.Query().Get("v") != "":
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	default:
		w.Header().Set("Cache-Control", "public, max-age=300")
	}
	w.Write(f)
}

func (s *Server) customCSS(w http.ResponseWriter, r *http.Request) {
	cfg := s.app.Cfg()
	w.Header().Set("Content-Type", "text/css; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	if cfg.Instance.CustomCSSFile != "" {
		if b, err := os.ReadFile(cfg.Instance.CustomCSSFile); err == nil {
			w.Write(b)
			w.Write([]byte("\n"))
		}
	}
	io.WriteString(w, cfg.Instance.CustomCSS)
}

func (s *Server) favicon(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "image/svg+xml")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	fmt.Fprintf(w, `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 64 64"><rect width="64" height="64" rx="12" fill="%s"/></svg>`,
		htmlEscape(string(safeColor(s.app.Cfg().Instance.Theme.Primary))))
}

func htmlEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;").Replace(s)
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	if err := s.app.DB.Ping(r.Context()); err != nil {
		writeJSON(w, 503, map[string]any{"status": "unhealthy", "error": err.Error()})
		return
	}
	cerr, loaded := s.app.Conf.Status()
	body := map[string]any{"status": "ok", "version": s.Version, "config_loaded": loaded.Unix()}
	if cerr != nil {
		body["config_error"] = cerr.Error()
	}
	writeJSON(w, 200, body)
}

func (s *Server) home(w http.ResponseWriter, r *http.Request) {
	act := actorFrom(r)
	ctx := r.Context()
	type projectCard struct {
		Key, Name, Description, Color, Icon string
		Open, Total                         int
	}
	var cards []projectCard
	for _, p := range s.app.VisibleProjects(act) {
		if len(s.app.VisibleProjects(act)) == 1 && !wantsJSON(r) && r.URL.Query().Get("all") == "" {
			http.Redirect(w, r, "/projects/"+p.Key, http.StatusSeeOther)
			return
		}
		c := projectCard{Key: p.Key, Name: p.Name, Description: p.Description, Color: p.Color, Icon: p.Icon}
		if row, err := s.app.DB.EnsureProject(ctx, p.Key); err == nil {
			counts, _ := s.app.DB.StateCounts(ctx, row.ID, false)
			wf := s.app.Cfg().ProjectWorkflow(p.Key)
			for st, n := range counts {
				c.Total += n
				if ws := wf.State(st); ws != nil && !ws.Closed() {
					c.Open += n
				}
			}
		}
		cards = append(cards, c)
	}
	data := map[string]any{
		"Projects": cards,
		"Trending": s.app.Trending(ctx, act, 0, 10),
		"Recent":   s.app.Search(ctx, act, "sort:created", 1, 10).Issues,
	}
	if act.LoggedIn() {
		data["Mine"] = s.app.Search(ctx, act, "reporter:me is:open sort:updated", 1, 5).Issues
		data["Watching"] = s.app.Search(ctx, act, "watching:me -reporter:me sort:activity", 1, 5).Issues
	}
	s.render(w, r, 200, "home", s.newPage(w, r, "", data))
}

func (s *Server) project(w http.ResponseWriter, r *http.Request) {
	ov, err := s.app.Overview(r.Context(), actorFrom(r), r.PathValue("key"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.render(w, r, 200, "project", s.newPage(w, r, ov.Project.Name, ov))
}

func (s *Server) releases(w http.ResponseWriter, r *http.Request) {
	ov, err := s.app.Overview(r.Context(), actorFrom(r), r.PathValue("key"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.render(w, r, 200, "releases", s.newPage(w, r, ov.Project.Name+" releases", ov))
}

func (s *Server) changelog(w http.ResponseWriter, r *http.Request) {
	key := strings.ToUpper(r.PathValue("key"))
	v, issues, err := s.app.Changelog(r.Context(), actorFrom(r), key, r.PathValue("version"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	p := s.app.Cfg().Project(key)
	s.render(w, r, 200, "changelog", s.newPage(w, r, p.Name+" "+v.Name+" changelog",
		map[string]any{"Project": p, "Version": v, "Issues": issues}))
}

func (s *Server) issues(w http.ResponseWriter, r *http.Request) {
	act := actorFrom(r)
	q := r.URL.Query().Get("q")
	if _, ok := r.URL.Query()["q"]; !ok {
		q = "is:open"
		if p := r.URL.Query().Get("project"); p != "" {
			q = "project:" + p + " " + q
		}
	}
	if clean := normalizeQuery(q); clean != q {
		v := r.URL.Query()
		v.Set("q", clean)
		http.Redirect(w, r, "/issues?"+v.Encode(), http.StatusMovedPermanently)
		return
	}
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	res := s.app.Search(r.Context(), act, q, page, 0)
	var saved []*store.SavedSearch
	if act.LoggedIn() {
		saved, _ = s.app.DB.SavedSearches(r.Context(), act.ID())
	}
	project := ""
	pq := search.Parse(q)
	for _, t := range pq.Terms {
		if (t.Key == "project" || t.Key == "p") && !t.Neg && !strings.Contains(t.Value, ",") {
			project = strings.ToUpper(t.Value)
		}
	}
	if project == "" && len(s.app.VisibleProjects(act)) == 1 {
		project = s.app.VisibleProjects(act)[0].Key
	}
	data := map[string]any{"Result": res, "Saved": saved, "Project": project, "Canned": s.app.Cfg().Canned, "PerPage": s.app.Cfg().Instance.IssuesPerPage}
	title := "Issues"
	if q != "" {
		title = q + " · Issues"
	}
	s.render(w, r, 200, "issues", s.newPage(w, r, title, data))
}

func (s *Server) help(w http.ResponseWriter, r *http.Request) {
	cfg := s.app.Cfg()
	type kv struct{ Key, Help string }
	var fields []kv
	for _, f := range cfg.Fields {
		ex := f.ID + ":value"
		if len(f.Options) > 0 {
			ex = f.ID + ":" + f.Options[0].Value
		}
		fields = append(fields, kv{ex, f.Label})
	}
	s.render(w, r, 200, "help", s.newPage(w, r, "Help", map[string]any{"Keys": search.Keys, "Fields": fields}))
}

func (s *Server) profile(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	act := actorFrom(r)
	u, err := s.app.DB.UserByName(ctx, r.PathValue("name"))
	if err != nil || (u.ShadowBanned && !s.app.Can(act, "moderation", "") && act.ID() != u.ID) {
		s.fail(w, r, app.ErrNotFound)
		return
	}
	reported := s.app.Search(ctx, act, "reporter:"+u.Username+" sort:created", 1, 20)
	comments, _ := s.app.DB.UserComments(ctx, u.ID, 10)
	var visible []map[string]any
	if len(comments) > 0 {
		ids := make([]int64, len(comments))
		for i, c := range comments {
			ids[i] = c.IssueID
		}
		issues, _ := s.app.DB.IssuesByIDs(ctx, ids)
		for _, c := range comments {
			if iss := issues[c.IssueID]; iss != nil && s.app.CanSeeIssue(ctx, act, iss) {
				visible = append(visible, map[string]any{"Comment": c, "Issue": iss})
			}
		}
	}
	ids, _ := s.app.DB.UserIdentities(ctx, u.ID)
	var modLog []*store.ModLogEntry
	sessions := 0
	if s.app.Can(act, "moderation", "") {
		modLog, _ = s.app.ModerationLog(ctx, act, u.ID)
		sessions = s.app.DB.CountUserSessions(ctx, u.ID)
	}
	data := map[string]any{
		"ModLog":     modLog,
		"Sessions":   sessions,
		"Profile":    u,
		"Role":       s.app.Role(u),
		"Reputation": s.app.Reputation(ctx, u.ID),
		"Reported":   reported,
		"Comments":   visible,
		"Identities": ids,
		"Roles":      store.SortedKeys(s.app.Cfg().Roles),
	}
	s.render(w, r, 200, "profile", s.newPage(w, r, u.Name(), data))
}

func (s *Server) userAdminSubmit(w http.ResponseWriter, r *http.Request) {
	var ch app.UserAdmin
	if v := r.FormValue("role"); v != "" {
		ch.Role = &v
	}
	for _, f := range []struct {
		k string
		p **bool
	}{{"banned", &ch.Banned}, {"shadow_banned", &ch.ShadowBanned}, {"trusted", &ch.Trusted}} {
		if r.Form.Has(f.k) {
			v := formBool(r, f.k)
			*f.p = &v
		}
	}
	if _, err := s.app.AdminUpdateUser(r.Context(), actorFrom(r), r.PathValue("name"), ch); err != nil {
		s.fail(w, r, err)
		return
	}
	s.flash(w, r, "ok", "User updated")
	back(w, r, "/users/"+r.PathValue("name"))
}

func (s *Server) userModerateSubmit(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	dur, err := app.ParseModDuration(r.FormValue("duration"))
	if err == nil {
		_, err = s.app.ModerateUser(r.Context(), actorFrom(r), name, app.ModerationRequest{
			Action: r.FormValue("action"), Duration: dur, Reason: r.FormValue("reason")})
	}
	if err != nil {
		var ae *app.Error
		if errors.As(err, &ae) && ae.Status < 500 {
			s.flash(w, r, "error", ae.Msg)
			http.Redirect(w, r, "/users/"+url.PathEscape(name)+"#moderation", http.StatusSeeOther)
			return
		}
		s.fail(w, r, err)
		return
	}
	labels := map[string]string{"kick": "signed out everywhere", "lock": "locked", "ban": "banned", "unban": "reinstated",
		"mute": "muted", "unmute": "unmuted", "shadowban": "shadow-banned", "unshadowban": "no longer shadow-banned",
		"trust": "marked as trusted", "untrust": "no longer trusted", "revoke_tokens": "had their API tokens revoked"}
	s.flash(w, r, "ok", name+" was "+labels[r.FormValue("action")]+".")
	http.Redirect(w, r, "/users/"+url.PathEscape(name)+"#moderation", http.StatusSeeOther)
}

func (s *Server) settings(w http.ResponseWriter, r *http.Request) {
	act := actorFrom(r)
	if !act.LoggedIn() {
		s.fail(w, r, app.ErrLoginNeeded)
		return
	}
	s.render(w, r, 200, "settings", s.newPage(w, r, "Settings", s.settingsData(r, "")))
}

func (s *Server) settingsData(r *http.Request, newToken string) map[string]any {
	act := actorFrom(r)
	tokens, _ := s.app.DB.ListAPITokens(r.Context(), act.ID())
	ids, _ := s.app.DB.UserIdentities(r.Context(), act.ID())
	unused, total := s.app.RecoveryStatus(r.Context(), act.ID())
	return map[string]any{"Tokens": tokens, "Identities": ids, "NewToken": newToken,
		"RecoveryUnused": unused, "RecoveryTotal": total}
}

func (s *Server) recoveryCodesCreate(w http.ResponseWriter, r *http.Request) {
	codes, err := s.app.GenerateRecoveryCodes(r.Context(), actorFrom(r))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.renderRecoveryCodes(w, r, codes, "/settings")
}

func (s *Server) renderRecoveryCodes(w http.ResponseWriter, r *http.Request, codes []string, next string) {
	if !safeRedirect(next) {
		next = "/"
	}
	p := s.newPage(w, r, "Recovery codes", map[string]any{"Codes": codes, "Next": next})
	p.NoIndex = true
	s.render(w, r, 200, "recovery_codes", p)
}

func (s *Server) settingsSubmit(w http.ResponseWriter, r *http.Request) {
	act := actorFrom(r)
	err := s.app.UpdateProfile(r.Context(), act, app.ProfileUpdate{
		DisplayName: r.FormValue("display_name"), Email: r.FormValue("email"), Bio: r.FormValue("bio"),
		Locale: r.FormValue("locale"), AvatarURL: r.FormValue("avatar_url"), EmailNotifications: formBool(r, "email_notifications"),
		CurrentPassword: r.FormValue("current_password"), NewPassword: r.FormValue("new_password"),
	})
	if err != nil {
		if ae, ok := err.(*app.Error); ok && ae.Fields != nil {
			p := s.newPage(w, r, "Settings", s.settingsData(r, ""))
			p.Errors, p.Form = ae.Fields, r.Form
			s.render(w, r, 422, "settings", p)
			return
		}
		s.fail(w, r, err)
		return
	}
	s.flash(w, r, "ok", "Settings saved")
	http.Redirect(w, r, "/settings", http.StatusSeeOther)
}

func (s *Server) tokenCreate(w http.ResponseWriter, r *http.Request) {
	act := actorFrom(r)
	tok, _, err := s.app.CreateAPIToken(r.Context(), act, r.FormValue("name"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.render(w, r, 200, "settings", s.newPage(w, r, "Settings", s.settingsData(r, tok)))
}

func (s *Server) tokenDelete(w http.ResponseWriter, r *http.Request) {
	act := actorFrom(r)
	if !act.LoggedIn() {
		s.fail(w, r, app.ErrLoginNeeded)
		return
	}
	s.app.DB.DeleteAPIToken(r.Context(), act.ID(), atoi64(r.PathValue("id")))
	s.flash(w, r, "ok", "Token revoked")
	http.Redirect(w, r, "/settings", http.StatusSeeOther)
}

func (s *Server) notifications(w http.ResponseWriter, r *http.Request) {
	act := actorFrom(r)
	if !act.LoggedIn() {
		s.fail(w, r, app.ErrLoginNeeded)
		return
	}
	list, _ := s.app.DB.Notifications(r.Context(), act.ID(), r.URL.Query().Get("unread") != "", 100)
	if wantsJSON(r) {
		writeJSON(w, 200, map[string]any{"notifications": list, "unread": s.app.DB.UnreadCount(r.Context(), act.ID())})
		return
	}
	s.render(w, r, 200, "notifications", s.newPage(w, r, "Notifications", list))
}

func (s *Server) notificationsRead(w http.ResponseWriter, r *http.Request) {
	act := actorFrom(r)
	if !act.LoggedIn() {
		s.fail(w, r, app.ErrLoginNeeded)
		return
	}
	s.app.DB.MarkRead(r.Context(), act.ID(), atoi64(r.FormValue("id")))
	if wantsJSON(r) {
		writeJSON(w, 200, map[string]any{"ok": true})
		return
	}
	back(w, r, "/notifications")
}

func (s *Server) saveSearch(w http.ResponseWriter, r *http.Request) {
	act := actorFrom(r)
	if !act.LoggedIn() || !s.app.Can(act, "search.save", "") {
		s.fail(w, r, app.ErrForbidden)
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	q := r.FormValue("q")
	if name == "" {
		name = q
	}
	pub := formBool(r, "public") && s.app.Can(act, "search.save_public", "")
	if err := s.app.DB.CreateSavedSearch(r.Context(), &store.SavedSearch{UserID: act.ID(), Name: name, Query: q, Public: pub}); err != nil {
		s.fail(w, r, err)
		return
	}
	s.flash(w, r, "ok", "Search saved")
	http.Redirect(w, r, "/issues?q="+url.QueryEscape(q), http.StatusSeeOther)
}

func (s *Server) deleteSearch(w http.ResponseWriter, r *http.Request) {
	act := actorFrom(r)
	if !act.LoggedIn() {
		s.fail(w, r, app.ErrLoginNeeded)
		return
	}
	s.app.DB.DeleteSavedSearch(r.Context(), atoi64(r.PathValue("id")), act.ID(), s.app.Can(act, "search.save_public", ""))
	back(w, r, "/issues")
}

var leftoverEscape = regexp.MustCompile(`%[0-9A-Fa-f]{2}`)

func normalizeQuery(q string) string {
	for i := 0; i < 3 && leftoverEscape.MatchString(q); i++ {
		d, err := url.QueryUnescape(q)
		if err != nil || d == q {
			break
		}
		q = d
	}
	return strings.Join(strings.Fields(q), " ")
}
