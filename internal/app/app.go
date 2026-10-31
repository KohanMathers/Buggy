// Package app holds buggy's business logic.
package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/kohanmathers/buggy/internal/config"
	"github.com/kohanmathers/buggy/internal/storage"
	"github.com/kohanmathers/buggy/internal/store"
)

type App struct {
	Conf    *config.Holder
	DB      *store.DB
	Files   storage.Storage
	Log     *slog.Logger
	HTTP    *http.Client
	System  *store.User
	limiter *RateLimiter
	events  chan Event
	wg      sync.WaitGroup
	stop    chan struct{}
}

func New(ctx context.Context, h *config.Holder, db *store.DB, files storage.Storage, log *slog.Logger) (*App, error) {
	a := &App{
		Conf:    h,
		DB:      db,
		Files:   files,
		Log:     log,
		HTTP:    &http.Client{Timeout: 15 * time.Second},
		limiter: NewRateLimiter(),
		events:  make(chan Event, 1024),
		stop:    make(chan struct{}),
	}
	if err := a.ensureSystemUser(ctx); err != nil {
		return nil, err
	}
	if err := a.SyncConfig(ctx, h.Get()); err != nil {
		return nil, err
	}
	h.OnReload(func(c *config.Config) {
		if err := a.SyncConfig(context.Background(), c); err != nil {
			log.Error("sync config", "err", err)
		}
	})
	return a, nil
}

func (a *App) Start() {
	a.wg.Add(2)
	go a.eventLoop()
	go a.schedulerLoop()
}

func (a *App) Close() {
	close(a.stop)
	a.wg.Wait()
}

func (a *App) Cfg() *config.Config { return a.Conf.Get() }

func (a *App) ensureSystemUser(ctx context.Context) error {
	u, err := a.DB.UserByName(ctx, "buggy")
	if err == store.ErrNotFound {
		u = &store.User{Username: "buggy", DisplayName: "Buggy", Role: "admin", System: true}
		if err := a.DB.CreateUser(ctx, u); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	a.System = u
	return nil
}

func (a *App) SyncConfig(ctx context.Context, cfg *config.Config) error {
	for _, p := range cfg.Projects {
		row, err := a.DB.EnsureProject(ctx, p.Key)
		if err != nil {
			return err
		}
		for i, v := range p.Versions {
			if _, err := a.DB.UpsertVersion(ctx, row.ID, store.Version{
				Name: v.Name, SortOrder: int64((i + 1) * 10), Released: v.Released,
				ReleaseDate: v.ReleaseDate, Archived: v.Archived,
			}); err != nil {
				return err
			}
		}
	}
	return nil
}

type Error struct {
	Status int
	Msg    string
	Fields map[string]string
}

func (e *Error) Error() string { return e.Msg }

func Errf(status int, format string, a ...any) *Error {
	return &Error{Status: status, Msg: fmt.Sprintf(format, a...)}
}

var (
	ErrForbidden    = &Error{Status: 403, Msg: "you don't have permission to do that"}
	ErrNotFound     = &Error{Status: 404, Msg: "not found"}
	ErrLoginNeeded  = &Error{Status: 401, Msg: "please log in first"}
	ErrRateLimited  = &Error{Status: 429, Msg: "you're doing that too often, please wait a bit"}
	ErrCaptchaError = &Error{Status: 400, Msg: "captcha verification failed"}
)

func Invalid(fields map[string]string) *Error {
	parts := make([]string, 0, len(fields))
	for _, k := range store.SortedKeys(fields) {
		parts = append(parts, fields[k])
	}
	return &Error{Status: 422, Msg: strings.Join(parts, "; "), Fields: fields}
}

func Status(err error) int {
	var e *Error
	if errors.As(err, &e) {
		return e.Status
	}
	if errors.Is(err, store.ErrNotFound) {
		return 404
	}
	return 500
}

type Actor struct {
	User   *store.User
	IP     string
	System bool
}

func (a *Actor) ID() int64 {
	if a == nil || a.User == nil {
		return 0
	}
	return a.User.ID
}

func (a *Actor) LoggedIn() bool { return a != nil && a.User != nil }

func (a *App) SystemActor() *Actor { return &Actor{User: a.System, System: true} }

func (a *App) roleFor(cfg *config.Config, u *store.User) string {
	if u == nil || u.Suspended() {
		return cfg.Auth.AnonymousRole
	}
	if cfg.IsAdmin(u.Username) {
		return "admin"
	}
	if _, ok := cfg.Roles[u.Role]; !ok {
		return cfg.Auth.DefaultRole
	}
	return u.Role
}

func (a *App) Role(u *store.User) string { return a.roleFor(a.Cfg(), u) }

func (a *App) Can(act *Actor, perm, project string) bool {
	if act != nil && act.System {
		return true
	}
	cfg := a.Cfg()
	var u *store.User
	if act != nil {
		u = act.User
	}
	role := a.roleFor(cfg, u)
	set := cfg.Perms(role)
	if project != "" {
		p := cfg.Project(project)
		if p == nil {
			return false
		}
		member := ""
		if u != nil && !u.Suspended() {
			for name, r := range p.Members {
				if strings.EqualFold(name, u.Username) {
					member = r
				}
			}
		}
		if p.Private && member == "" && !config.HasPerm(set, "admin") {
			return false
		}
		if member != "" && config.HasPerm(cfg.Perms(member), perm) {
			return true
		}
		if p.Archived && perm != "issue.view" && !config.HasPerm(set, "admin") {
			return false
		}
	}
	return config.HasPerm(set, perm)
}

func RequireLogin(act *Actor) error {
	if !act.LoggedIn() {
		return ErrLoginNeeded
	}
	if act.User.Suspended() {
		return SuspendedError(act.User)
	}
	return nil
}

func (a *App) VisibleProjects(act *Actor) []config.Project {
	var out []config.Project
	for _, p := range a.Cfg().Projects {
		if a.Can(act, "issue.view", p.Key) {
			out = append(out, p)
		}
	}
	return out
}

func (a *App) CanSeeIssue(ctx context.Context, act *Actor, iss *store.Issue) bool {
	pk := iss.ProjectKey()
	if !a.Can(act, "issue.view", pk) {
		return false
	}
	own := act.ID() != 0 && act.ID() == iss.ReporterID
	if own {
		return true
	}
	if iss.Private && !a.Can(act, "issue.view_private", pk) {
		return false
	}
	if iss.Moderation != "approved" && !a.Can(act, "moderation", pk) {
		return false
	}
	if !a.Can(act, "moderation", pk) {
		rep := iss.Reporter
		if rep == nil {
			rep, _ = a.DB.UserByID(ctx, iss.ReporterID)
		}
		if rep != nil && rep.ShadowBanned {
			return false
		}
	}
	return true
}

func (a *App) visibility(act *Actor) ([]string, []any) {
	var keys, privKeys, modKeys []string
	for _, p := range a.Cfg().Projects {
		if !a.Can(act, "issue.view", p.Key) {
			continue
		}
		keys = append(keys, p.Key)
		if a.Can(act, "issue.view_private", p.Key) {
			privKeys = append(privKeys, p.Key)
		}
		if a.Can(act, "moderation", p.Key) {
			modKeys = append(modKeys, p.Key)
		}
	}
	if len(keys) == 0 {
		return []string{"1 = 0"}, nil
	}
	uid := act.ID()
	projIn := func(ks []string) (string, []any) {
		args := make([]any, len(ks))
		for i, k := range ks {
			args[i] = k
		}
		return "i.project_id IN (SELECT id FROM projects WHERE key IN (" + strings.TrimSuffix(strings.Repeat("?, ", len(ks)), ", ") + "))", args
	}
	var where []string
	var args []any
	s, a1 := projIn(keys)
	where, args = append(where, s), append(args, a1...)

	if len(privKeys) < len(keys) {
		cond := "(i.private = FALSE OR i.reporter_id = ?"
		args = append(args, uid)
		if len(privKeys) > 0 {
			s, a2 := projIn(privKeys)
			cond += " OR " + s
			args = append(args, a2...)
		}
		where = append(where, cond+")")
	}
	if len(modKeys) < len(keys) {
		cond := "((i.moderation = 'approved' AND i.reporter_id NOT IN (SELECT id FROM users WHERE shadow_banned = TRUE)) OR i.reporter_id = ?"
		args = append(args, uid)
		if len(modKeys) > 0 {
			s, a3 := projIn(modKeys)
			cond += " OR " + s
			args = append(args, a3...)
		}
		where = append(where, cond+")")
	}
	return where, args
}

func (a *App) commentVisible(act *Actor, pk string, c *store.Comment) bool {
	if act.ID() != 0 && c.AuthorID == act.ID() {
		return true
	}
	if c.Deleted && !a.Can(act, "comment.delete", pk) {
		return false
	}
	if c.Restricted && !a.Can(act, "comment.restricted", pk) {
		return false
	}
	if c.Moderation != "approved" && !a.Can(act, "moderation", pk) {
		return false
	}
	if c.Author != nil && c.Author.ShadowBanned && !a.Can(act, "moderation", pk) {
		return false
	}
	return true
}

func (a *App) IssueURL(key string) string { return a.Cfg().Instance.URL + "/issues/" + key }
