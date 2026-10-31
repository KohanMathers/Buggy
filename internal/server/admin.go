package server

import (
	"context"
	"net/http"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/kohanmathers/buggy/internal/app"
	"github.com/kohanmathers/buggy/internal/store"
)

func (s *Server) triage(w http.ResponseWriter, r *http.Request) {
	q, err := s.app.Queue(r.Context(), actorFrom(r))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.render(w, r, 200, "triage", s.newPage(w, r, "Triage", q))
}

func (s *Server) moderate(w http.ResponseWriter, r *http.Request) {
	approve := r.FormValue("action") == "approve"
	if err := s.app.Moderate(r.Context(), actorFrom(r), r.PathValue("kind"), atoi64(r.PathValue("id")), approve); err != nil {
		s.fail(w, r, err)
		return
	}
	if wantsJSON(r) {
		writeJSON(w, 200, map[string]any{"ok": true})
		return
	}
	if approve {
		s.flash(w, r, "ok", "Approved")
	} else {
		s.flash(w, r, "ok", "Rejected")
	}
	back(w, r, "/triage")
}

func (s *Server) requireAdmin(w http.ResponseWriter, r *http.Request) bool {
	act := actorFrom(r)
	if !act.LoggedIn() {
		s.fail(w, r, app.ErrLoginNeeded)
		return false
	}
	if !s.app.Can(act, "admin", "") && !s.app.Can(act, "user.manage", "") {
		s.fail(w, r, app.ErrForbidden)
		return false
	}
	return true
}

func (s *Server) admin(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	ctx := r.Context()
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}
	users, total, _ := s.app.DB.ListUsers(ctx, r.URL.Query().Get("u"), 50, (page-1)*50)
	cerr, loaded := s.app.Conf.Status()
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)
	states, _ := s.app.DB.StateCounts(ctx, 0, true)
	issues := 0
	for _, n := range states {
		issues += n
	}
	type projVersions struct {
		Key      string
		Versions []*store.Version
	}
	var pv []projVersions
	for _, p := range s.app.Cfg().Projects {
		if row, err := s.app.DB.ProjectByKey(ctx, p.Key); err == nil {
			vs, _ := s.app.DB.Versions(ctx, row.ID)
			pv = append(pv, projVersions{Key: p.Key, Versions: vs})
		}
	}
	data := map[string]any{
		"Users":       users,
		"UserTotal":   total,
		"UserPage":    page,
		"UserPages":   (total + 49) / 50,
		"ConfigError": cerr,
		"ConfigLoad":  loaded,
		"ConfigFiles": s.app.Cfg().Files,
		"Driver":      s.app.DB.Dialect(),
		"Schema":      s.app.DB.SchemaVersion(ctx),
		"Issues":      issues,
		"MemMB":       mem.Alloc >> 20,
		"Goroutines":  runtime.NumGoroutine(),
		"Roles":       store.SortedKeys(s.app.Cfg().Roles),
		"Versions":    pv,
	}
	s.render(w, r, 200, "admin", s.newPage(w, r, "Admin", data))
}

func (s *Server) adminReload(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	if err := s.app.Conf.Reload(); err != nil {
		s.flash(w, r, "error", "Config reload failed: "+err.Error())
	} else {
		s.flash(w, r, "ok", "Configuration reloaded")
	}
	http.Redirect(w, r, "/admin", http.StatusSeeOther)
}

func (s *Server) adminRunAutomations(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	go s.app.RunScheduled(context.Background())
	s.flash(w, r, "ok", "Scheduled automations started")
	http.Redirect(w, r, "/admin", http.StatusSeeOther)
}

func (s *Server) adminVersion(w http.ResponseWriter, r *http.Request) {
	act := actorFrom(r)
	pk := strings.ToUpper(r.FormValue("project"))
	if !s.app.Can(act, "version.manage", pk) {
		s.fail(w, r, app.ErrForbidden)
		return
	}
	row, err := s.app.DB.EnsureProject(r.Context(), pk)
	if err != nil || s.app.Cfg().Project(pk) == nil {
		s.fail(w, r, app.ErrNotFound)
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		s.fail(w, r, app.Errf(422, "version name required"))
		return
	}
	if r.FormValue("action") == "delete" {
		if v, err := s.app.DB.VersionByName(r.Context(), row.ID, name); err == nil {
			s.app.DB.DeleteVersion(r.Context(), v.ID)
		}
	} else {
		_, err = s.app.DB.UpsertVersion(r.Context(), row.ID, store.Version{Name: name, SortOrder: -1,
			Released: formBool(r, "released"), Archived: formBool(r, "archived"), ReleaseDate: r.FormValue("release_date")})
		if err != nil {
			s.fail(w, r, err)
			return
		}
	}
	s.flash(w, r, "ok", "Version "+name+" saved")
	back(w, r, "/admin")
}

var startTime = time.Now()
