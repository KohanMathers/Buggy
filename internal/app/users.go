package app

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"net/mail"
	"regexp"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/kohanmathers/buggy/internal/store"
)

var (
	usernameRe = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{1,30}[A-Za-z0-9_]$`)
	reserved   = map[string]bool{"buggy": true, "me": true, "none": true, "nobody": true, "anonymous": true, "system": true, "api": true, "new": true}
)

func ValidUsername(s string) bool { return usernameRe.MatchString(s) && !reserved[strings.ToLower(s)] }

func RandomToken(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func HashPassword(pw string) (string, error) {
	h, err := bcrypt.GenerateFromPassword([]byte(pw), 12)
	return string(h), err
}

func (a *App) newUserRole(ctx context.Context, username, providerRole string) string {
	cfg := a.Cfg()
	if cfg.IsAdmin(username) {
		return "admin"
	}
	if cfg.Auth.FirstUserAdmin {
		if n, _ := a.DB.CountAdmins(ctx); n == 0 {
			if c, _ := a.DB.CountUsers(ctx); c == 0 {
				return "admin"
			}
		}
	}
	if providerRole != "" {
		return providerRole
	}
	return cfg.Auth.DefaultRole
}

func (a *App) Register(ctx context.Context, act *Actor, username, email, password, captcha string) (*store.User, error) {
	cfg := a.Cfg()
	if !cfg.Auth.Local.Enabled || cfg.Auth.Local.Registration != "open" {
		return nil, Errf(403, "registration is closed")
	}
	if !a.limiter.Allow("register:"+act.IP, cfg.Spam.RateLimits["register"]) {
		return nil, ErrRateLimited
	}
	if cfg.Spam.Captcha.Enabled("register") {
		if err := a.VerifyCaptcha(ctx, captcha, act.IP); err != nil {
			return nil, err
		}
	}
	return a.CreateLocalUser(ctx, username, email, password, "")
}

func (a *App) CreateLocalUser(ctx context.Context, username, email, password, role string) (*store.User, error) {
	cfg := a.Cfg()
	errs := map[string]string{}
	username = strings.TrimSpace(username)
	if !ValidUsername(username) {
		errs["username"] = "Username must be 3-32 letters, digits, _ . or -"
	} else if a.DB.UsernameTaken(ctx, username) {
		errs["username"] = "That username is taken"
	}
	email = strings.TrimSpace(email)
	if email != "" {
		if _, err := mail.ParseAddress(email); err != nil {
			errs["email"] = "Invalid email address"
		} else if _, err := a.DB.UserByEmail(ctx, email); err == nil {
			errs["email"] = "That email is already registered"
		}
	}
	if len(password) < cfg.Auth.Local.MinPassword {
		errs["password"] = fmt.Sprintf("Password must be at least %d characters", cfg.Auth.Local.MinPassword)
	}
	if len(errs) > 0 {
		return nil, Invalid(errs)
	}
	hash, err := HashPassword(password)
	if err != nil {
		return nil, err
	}
	if role == "" {
		role = a.newUserRole(ctx, username, "")
	}
	u := &store.User{Username: username, Email: email, PasswordHash: hash, Role: role, EmailNotifications: true}
	if err := a.DB.CreateUser(ctx, u); err != nil {
		return nil, err
	}
	a.Log.Info("user registered", "username", username, "role", role)
	return u, nil
}

func (a *App) Login(ctx context.Context, ip, login, password string) (*store.User, error) {
	if !a.Cfg().Auth.Local.Enabled {
		return nil, Errf(403, "password login is disabled")
	}
	if !a.limiter.Allow("login:"+ip, 30) || !a.limiter.Allow("login:u:"+strings.ToLower(login), 20) {
		return nil, ErrRateLimited
	}
	bad := Errf(401, "wrong username or password")
	u, err := a.DB.UserByName(ctx, login)
	if err != nil {
		u, err = a.DB.UserByEmail(ctx, login)
	}
	if err != nil || u.PasswordHash == "" || u.System {
		bcrypt.CompareHashAndPassword([]byte("$2a$12$C6UzMDM.H6dfI/f/IKcEeO8Xh2Qh2v1yZ0b9Zr0r2x4a8cHn7Q5mW"), []byte(password))
		return nil, bad
	}
	if bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(password)) != nil {
		return nil, bad
	}
	if u.Suspended() {
		return nil, SuspendedError(u)
	}
	a.promoteIfAdmin(ctx, u)
	return u, nil
}

func (a *App) promoteIfAdmin(ctx context.Context, u *store.User) {
	if a.Cfg().IsAdmin(u.Username) && u.Role != "admin" {
		u.Role = "admin"
		a.DB.UpdateUser(ctx, u)
	}
}

type ExternalIdentity struct {
	Provider    string
	Subject     string
	Username    string
	DisplayName string
	Email       string
	AvatarURL   string
	DefaultRole string
}

func (a *App) LoginExternal(ctx context.Context, id ExternalIdentity, linkTo *store.User) (*store.User, error) {
	if id.Subject == "" {
		return nil, Errf(400, "provider returned no user id")
	}
	if linkTo != nil {
		if u, err := a.DB.UserByIdentity(ctx, id.Provider, id.Subject); err == nil && u.ID != linkTo.ID {
			return nil, Errf(409, "that %s account is already linked to another user", id.Provider)
		}
		return linkTo, a.DB.LinkIdentity(ctx, id.Provider, id.Subject, linkTo.ID)
	}
	if u, err := a.DB.UserByIdentity(ctx, id.Provider, id.Subject); err == nil {
		if u.Suspended() {
			return nil, SuspendedError(u)
		}
		if u.AvatarURL == "" && id.AvatarURL != "" {
			u.AvatarURL = id.AvatarURL
			a.DB.UpdateUser(ctx, u)
		}
		a.promoteIfAdmin(ctx, u)
		return u, nil
	}
	cfg := a.Cfg()
	if cfg.Auth.Local.Registration != "open" && len(cfg.Auth.Providers) == 0 {
		return nil, Errf(403, "registration is closed")
	}
	base := regexp.MustCompile(`[^A-Za-z0-9_.-]+`).ReplaceAllString(id.Username, "")
	base = strings.Trim(base, ".-")
	if len(base) < 3 {
		base = id.Provider + "_" + base
	}
	if len(base) > 28 {
		base = base[:28]
	}
	name := base
	for i := 2; !ValidUsername(name) || a.DB.UsernameTaken(ctx, name); i++ {
		name = fmt.Sprintf("%s%d", base, i)
		if i > 500 {
			return nil, Errf(500, "could not allocate a username")
		}
	}
	email := id.Email
	if email != "" {
		if _, err := a.DB.UserByEmail(ctx, email); err == nil {
			email = ""
		}
	}
	u := &store.User{Username: name, DisplayName: id.DisplayName, Email: email, AvatarURL: id.AvatarURL,
		Role: a.newUserRole(ctx, name, id.DefaultRole), EmailNotifications: true}
	if err := a.DB.CreateUser(ctx, u); err != nil {
		return nil, err
	}
	if err := a.DB.LinkIdentity(ctx, id.Provider, id.Subject, u.ID); err != nil {
		return nil, err
	}
	a.Log.Info("user registered via oauth", "username", name, "provider", id.Provider)
	return u, nil
}

func (a *App) NewSession(ctx context.Context, u *store.User, ip, ua string) (string, time.Time, error) {
	tok := RandomToken(32)
	ttl := a.Cfg().Auth.SessionTTL.D()
	if ttl <= 0 {
		ttl = 30 * 24 * time.Hour
	}
	exp := time.Now().Add(ttl)
	if len(ua) > 300 {
		ua = ua[:300]
	}
	return tok, exp, a.DB.CreateSession(ctx, tok, u.ID, exp.Unix(), ip, ua)
}

type ProfileUpdate struct {
	DisplayName        string
	Email              string
	Bio                string
	Locale             string
	AvatarURL          string
	EmailNotifications bool
	CurrentPassword    string
	NewPassword        string
}

func (a *App) UpdateProfile(ctx context.Context, act *Actor, p ProfileUpdate) error {
	if err := RequireLogin(act); err != nil {
		return err
	}
	u := act.User
	errs := map[string]string{}
	if len([]rune(p.DisplayName)) > 64 {
		errs["display_name"] = "Display name is too long"
	}
	if len(p.Bio) > 2000 {
		errs["bio"] = "Bio is too long"
	}
	p.Email = strings.TrimSpace(p.Email)
	if p.Email != "" && p.Email != u.Email {
		if _, err := mail.ParseAddress(p.Email); err != nil {
			errs["email"] = "Invalid email"
		} else if o, err := a.DB.UserByEmail(ctx, p.Email); err == nil && o.ID != u.ID {
			errs["email"] = "That email is in use"
		}
	}
	if p.NewPassword != "" {
		if u.PasswordHash != "" && bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(p.CurrentPassword)) != nil {
			errs["current_password"] = "Current password is wrong"
		}
		if len(p.NewPassword) < a.Cfg().Auth.Local.MinPassword {
			errs["new_password"] = "New password is too short"
		}
	}
	p.AvatarURL = strings.TrimSpace(p.AvatarURL)
	if p.AvatarURL != "" && !strings.HasPrefix(p.AvatarURL, "https://") && !strings.HasPrefix(p.AvatarURL, "http://") && !strings.HasPrefix(p.AvatarURL, "/") {
		errs["avatar_url"] = "Profile picture must be an http(s) URL or a path"
	}
	if len(p.AvatarURL) > 1000 {
		errs["avatar_url"] = "Profile picture URL is too long"
	}
	if len(errs) > 0 {
		return Invalid(errs)
	}
	u.AvatarURL = p.AvatarURL
	u.DisplayName = strings.TrimSpace(p.DisplayName)
	u.Email = p.Email
	u.Bio = p.Bio
	u.Locale = p.Locale
	u.EmailNotifications = p.EmailNotifications
	if p.NewPassword != "" {
		h, err := HashPassword(p.NewPassword)
		if err != nil {
			return err
		}
		u.PasswordHash = h
	}
	return a.DB.UpdateUser(ctx, u)
}

func (a *App) CreateAPIToken(ctx context.Context, act *Actor, name string) (string, *store.APIToken, error) {
	if err := RequireLogin(act); err != nil {
		return "", nil, err
	}
	if !a.Can(act, "api.token", "") {
		return "", nil, ErrForbidden
	}
	name = strings.TrimSpace(name)
	if name == "" {
		name = "token"
	}
	tok := "bgy_" + RandomToken(30)
	t, err := a.DB.CreateAPIToken(ctx, act.User.ID, name, tok)
	return tok, t, err
}

type UserAdmin struct {
	Role         *string
	Banned       *bool
	ShadowBanned *bool
	Trusted      *bool
}

func (a *App) AdminUpdateUser(ctx context.Context, act *Actor, username string, ch UserAdmin) (*store.User, error) {
	if err := RequireLogin(act); err != nil {
		return nil, err
	}
	u, err := a.DB.UserByName(ctx, username)
	if err != nil || u.System {
		return nil, ErrNotFound
	}
	if ch.Role != nil {
		if !a.Can(act, "user.manage", "") {
			return nil, ErrForbidden
		}
		if _, ok := a.Cfg().Roles[*ch.Role]; !ok {
			return nil, Errf(422, "unknown role %q", *ch.Role)
		}
		u.Role = *ch.Role
	}
	if ch.Banned != nil || ch.ShadowBanned != nil || ch.Trusted != nil {
		if !a.Can(act, "moderation", "") {
			return nil, ErrForbidden
		}
		if a.Can(&Actor{User: u}, "moderation", "") && !a.Can(act, "user.manage", "") {
			return nil, ErrForbidden
		}
		if ch.Banned != nil {
			u.Banned = *ch.Banned
		}
		if ch.ShadowBanned != nil {
			u.ShadowBanned = *ch.ShadowBanned
		}
		if ch.Trusted != nil {
			u.Trusted = *ch.Trusted
		}
	}
	if u.ID == act.ID() && ch.Role != nil && *ch.Role != "admin" && a.Role(act.User) == "admin" {
		if n, _ := a.DB.CountAdmins(ctx); n <= 1 {
			return nil, Errf(409, "you are the last admin")
		}
	}
	if ch.Banned != nil && *ch.Banned {
		u.BannedUntil = 0
	}
	if ch.Banned != nil && !*ch.Banned {
		u.BannedUntil, u.BanReason = 0, ""
	}
	if err := a.DB.UpdateUser(ctx, u); err != nil {
		return nil, err
	}
	if u.Suspended() {
		a.DB.DeleteUserSessions(ctx, u.ID)
	}
	logIt := func(action, reason string) {
		a.DB.AddModLog(ctx, &store.ModLogEntry{ActorID: act.ID(), TargetID: u.ID, Action: action, Reason: reason})
	}
	if ch.Role != nil {
		logIt("role", "set to "+*ch.Role)
	}
	if ch.Banned != nil {
		logIt(map[bool]string{true: "ban", false: "unban"}[*ch.Banned], "")
	}
	if ch.ShadowBanned != nil {
		logIt(map[bool]string{true: "shadowban", false: "unshadowban"}[*ch.ShadowBanned], "")
	}
	if ch.Trusted != nil {
		logIt(map[bool]string{true: "trust", false: "untrust"}[*ch.Trusted], "")
	}
	a.Log.Info("user updated by staff", "user", u.Username, "by", act.User.Username, "role", u.Role, "banned", u.Banned, "shadow", u.ShadowBanned)
	return u, nil
}

type ModerationQueue struct {
	Issues   []*store.Issue
	Comments []PendingComment
	Triage   []*store.Issue
}

type PendingComment struct {
	*store.Comment
	IssueKey string
}

func (a *App) Queue(ctx context.Context, act *Actor) (*ModerationQueue, error) {
	if !a.Can(act, "moderation", "") && !a.Can(act, "triage", "") {
		return nil, ErrForbidden
	}
	q := &ModerationQueue{}
	if a.Can(act, "moderation", "") {
		q.Issues = a.Search(ctx, act, "is:pending sort:created-asc", 1, 100).Issues
		pending, _ := a.DB.PendingComments(ctx, 100)
		ids := make([]int64, len(pending))
		for i, c := range pending {
			ids[i] = c.IssueID
		}
		issues, _ := a.DB.IssuesByIDs(ctx, ids)
		for _, c := range pending {
			if i := issues[c.IssueID]; i != nil {
				q.Comments = append(q.Comments, PendingComment{Comment: c, IssueKey: i.Key})
			}
		}
	}
	var states []string
	for _, wf := range a.Cfg().Workflows {
		states = append(states, wf.Initial)
	}
	q.Triage = a.Search(ctx, act, "status:"+strings.Join(uniq(states), ",")+" -is:pending sort:created-asc", 1, 100).Issues
	return q, nil
}

func uniq(s []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, v := range s {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}

func (a *App) Moderate(ctx context.Context, act *Actor, kind string, id int64, approve bool) error {
	if !a.Can(act, "moderation", "") {
		return ErrForbidden
	}
	state := "rejected"
	if approve {
		state = "approved"
	}
	var authorID int64
	switch kind {
	case "issue":
		iss, err := a.DB.IssueByID(ctx, id)
		if err != nil {
			return ErrNotFound
		}
		iss.Moderation = state
		if err := a.DB.SaveIssue(ctx, a.DB, iss); err != nil {
			return err
		}
		authorID = iss.ReporterID
		if approve {
			a.DB.LoadRelations(ctx, []*store.Issue{iss})
			a.emit(Event{Type: "issue.created", Issue: iss, Actor: iss.Reporter, Text: iss.Description})
		}
	case "comment":
		c, err := a.DB.CommentByID(ctx, id)
		if err != nil {
			return ErrNotFound
		}
		c.Moderation = state
		if err := a.DB.SaveComment(ctx, a.DB, c); err != nil {
			return err
		}
		authorID = c.AuthorID
		iss, err := a.DB.IssueByID(ctx, c.IssueID)
		if err == nil {
			iss.CommentCount = a.DB.CountComments(ctx, a.DB, iss.ID)
			a.DB.Exec(ctx, `UPDATE issues SET comment_count = ? WHERE id = ?`, iss.CommentCount, iss.ID)
			a.DB.UpdateFTS(ctx, a.DB, iss.ID)
			if approve {
				a.DB.LoadRelations(ctx, []*store.Issue{iss})
				author, _ := a.DB.UserByID(ctx, c.AuthorID)
				a.emit(Event{Type: "issue.commented", Issue: iss, Actor: author, Comment: c, Text: c.Body})
			}
		}
	default:
		return Errf(422, "unknown kind")
	}
	if approve && authorID != 0 {
		a.DB.Exec(ctx, `UPDATE users SET trusted = TRUE WHERE id = ?`, authorID)
	}
	return nil
}
