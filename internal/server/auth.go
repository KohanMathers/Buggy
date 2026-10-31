package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/oauth2"

	"github.com/kohanmathers/buggy/internal/app"
	"github.com/kohanmathers/buggy/internal/config"
)

func (s *Server) loginForm(w http.ResponseWriter, r *http.Request) {
	if actorFrom(r).LoggedIn() {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	p := s.newPage(w, r, "Log in", nil)
	p.Form = r.URL.Query()
	s.render(w, r, 200, "login", p)
}

func (s *Server) loginSubmit(w http.ResponseWriter, r *http.Request) {
	act := actorFrom(r)
	u, err := s.app.Login(r.Context(), act.IP, strings.TrimSpace(r.FormValue("login")), r.FormValue("password"))
	if err != nil {
		if wantsJSON(r) {
			errJSON(w, err)
			return
		}
		p := s.newPage(w, r, "Log in", nil)
		p.Errors = map[string]string{"_": err.Error()}
		p.Form = r.Form
		s.render(w, r, app.Status(err), "login", p)
		return
	}
	tok, exp, err := s.app.NewSession(r.Context(), u, act.IP, r.UserAgent())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.setCookie(w, r, sessionCookie, tok, int(time.Until(exp).Seconds()), true)
	next := r.FormValue("next")
	if !safeRedirect(next) {
		next = "/"
	}
	http.Redirect(w, r, next, http.StatusSeeOther)
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		s.app.DB.DeleteSession(r.Context(), c.Value)
	}
	s.setCookie(w, r, sessionCookie, "", -1, true)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) registerForm(w http.ResponseWriter, r *http.Request) {
	cfg := s.app.Cfg()
	if !cfg.Auth.Local.Enabled || cfg.Auth.Local.Registration != "open" {
		s.fail(w, r, app.Errf(403, "Registration is closed. Sign in with one of the providers on the login page instead."))
		return
	}
	p := s.newPage(w, r, "Create account", nil)
	p.Form = r.URL.Query()
	s.render(w, r, 200, "register", p)
}

func (s *Server) registerSubmit(w http.ResponseWriter, r *http.Request) {
	act := actorFrom(r)
	if r.FormValue("password") != r.FormValue("password2") {
		p := s.newPage(w, r, "Create account", nil)
		p.Errors = map[string]string{"password2": "Passwords don't match"}
		p.Form = r.Form
		s.render(w, r, 422, "register", p)
		return
	}
	u, err := s.app.Register(r.Context(), act, r.FormValue("username"), r.FormValue("email"), r.FormValue("password"), captchaToken(r))
	if err != nil {
		var ae *app.Error
		if errors.As(err, &ae) && ae.Status < 500 {
			p := s.newPage(w, r, "Create account", nil)
			p.Errors = ae.Fields
			if p.Errors == nil {
				p.Errors = map[string]string{"_": ae.Msg}
			}
			p.Form = r.Form
			s.render(w, r, ae.Status, "register", p)
			return
		}
		s.fail(w, r, err)
		return
	}
	tok, exp, err := s.app.NewSession(r.Context(), u, act.IP, r.UserAgent())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.setCookie(w, r, sessionCookie, tok, int(time.Until(exp).Seconds()), true)
	next := r.FormValue("next")
	if !safeRedirect(next) {
		next = "/"
	}
	codes, err := s.app.IssueRecoveryCodes(r.Context(), u)
	if err != nil {
		s.app.Log.Error("issue recovery codes", "err", err)
		http.Redirect(w, r, next, http.StatusSeeOther)
		return
	}
	r = r.WithContext(context.WithValue(r.Context(), actorKey, &app.Actor{User: u, IP: act.IP}))
	s.renderRecoveryCodes(w, r, codes, next)
}

func (s *Server) forgotForm(w http.ResponseWriter, r *http.Request) {
	if !s.app.Cfg().Auth.Local.Enabled {
		s.fail(w, r, app.Errf(403, "Password login is disabled. Sign in with one of the providers on the login page."))
		return
	}
	p := s.newPage(w, r, "Reset password", nil)
	p.Form = r.URL.Query()
	s.render(w, r, 200, "forgot", p)
}

func (s *Server) forgotSubmit(w http.ResponseWriter, r *http.Request) {
	act := actorFrom(r)
	retry := func(status int, errs map[string]string) {
		p := s.newPage(w, r, "Reset password", nil)
		p.Errors, p.Form = errs, r.Form
		s.render(w, r, status, "forgot", p)
	}
	if r.FormValue("password") != r.FormValue("password2") {
		retry(422, map[string]string{"password2": "Passwords don't match"})
		return
	}
	u, left, err := s.app.ResetPasswordWithCode(r.Context(), act.IP, r.FormValue("login"), r.FormValue("code"), r.FormValue("password"))
	if err != nil {
		var ae *app.Error
		if errors.As(err, &ae) && ae.Status < 500 {
			errs := ae.Fields
			if errs == nil {
				errs = map[string]string{"_": ae.Msg}
			}
			retry(ae.Status, errs)
			return
		}
		s.fail(w, r, err)
		return
	}
	tok, exp, err := s.app.NewSession(r.Context(), u, act.IP, r.UserAgent())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.setCookie(w, r, sessionCookie, tok, int(time.Until(exp).Seconds()), true)
	msg := fmt.Sprintf("Your password was changed and you're signed in. You have %d recovery codes left.", left)
	if left <= 2 {
		msg += " Generate new ones below so you don't run out."
	}
	s.flash(w, r, "ok", msg)
	http.Redirect(w, r, "/settings#recovery", http.StatusSeeOther)
}

type oidcDiscovery struct {
	AuthURL     string `json:"authorization_endpoint"`
	TokenURL    string `json:"token_endpoint"`
	UserInfoURL string `json:"userinfo_endpoint"`
	fetched     time.Time
}

type oauthCache struct {
	mu   sync.Mutex
	disc map[string]*oidcDiscovery
}

func newOAuthCache() *oauthCache { return &oauthCache{disc: map[string]*oidcDiscovery{}} }

func (s *Server) providerEndpoints(ctx context.Context, p config.Provider) (config.Provider, error) {
	set := func(dst *string, v string) {
		if *dst == "" {
			*dst = v
		}
	}
	issuer := p.Issuer
	switch p.Type {
	case "github":
		set(&p.AuthURL, "https://github.com/login/oauth/authorize")
		set(&p.TokenURL, "https://github.com/login/oauth/access_token")
		set(&p.UserInfoURL, "https://api.github.com/user")
		if len(p.Scopes) == 0 {
			p.Scopes = []string{"read:user", "user:email"}
		}
		set(&p.IDField, "id")
		set(&p.UsernameField, "login")
		set(&p.NameField, "name")
		set(&p.AvatarField, "avatar_url")
		set(&p.Name, "GitHub")
	case "discord":
		set(&p.AuthURL, "https://discord.com/oauth2/authorize")
		set(&p.TokenURL, "https://discord.com/api/oauth2/token")
		set(&p.UserInfoURL, "https://discord.com/api/users/@me")
		if len(p.Scopes) == 0 {
			p.Scopes = []string{"identify", "email"}
		}
		set(&p.IDField, "id")
		set(&p.UsernameField, "username")
		set(&p.NameField, "global_name")
		set(&p.Name, "Discord")
	case "google":
		set(&issuer, "https://accounts.google.com")
		set(&p.Name, "Google")
	case "microsoft":
		set(&issuer, "https://login.microsoftonline.com/common/v2.0")
		set(&p.Name, "Microsoft")
	case "gitlab":
		set(&issuer, "https://gitlab.com")
		set(&p.Name, "GitLab")
	}
	if (p.Type == "oidc" || p.Type == "google" || p.Type == "microsoft" || p.Type == "gitlab") && (p.AuthURL == "" || p.TokenURL == "" || p.UserInfoURL == "") {
		d, err := s.discover(ctx, issuer)
		if err != nil {
			return p, err
		}
		set(&p.AuthURL, d.AuthURL)
		set(&p.TokenURL, d.TokenURL)
		set(&p.UserInfoURL, d.UserInfoURL)
		if len(p.Scopes) == 0 {
			p.Scopes = []string{"openid", "profile", "email"}
		}
	}
	set(&p.IDField, "sub")
	set(&p.UsernameField, "preferred_username")
	set(&p.NameField, "name")
	set(&p.EmailField, "email")
	set(&p.AvatarField, "picture")
	set(&p.Name, p.ID)
	return p, nil
}

func (s *Server) discover(ctx context.Context, issuer string) (*oidcDiscovery, error) {
	s.oauth.mu.Lock()
	d := s.oauth.disc[issuer]
	s.oauth.mu.Unlock()
	if d != nil && time.Since(d.fetched) < time.Hour {
		return d, nil
	}
	req, _ := http.NewRequestWithContext(ctx, "GET", strings.TrimRight(issuer, "/")+"/.well-known/openid-configuration", nil)
	resp, err := s.app.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("oidc discovery: %w", err)
	}
	defer resp.Body.Close()
	d = &oidcDiscovery{}
	if err := json.NewDecoder(resp.Body).Decode(d); err != nil || d.AuthURL == "" {
		return nil, fmt.Errorf("oidc discovery: invalid document from %s", issuer)
	}
	d.fetched = time.Now()
	s.oauth.mu.Lock()
	s.oauth.disc[issuer] = d
	s.oauth.mu.Unlock()
	return d, nil
}

func (s *Server) oauthConfig(ctx context.Context, id string) (*oauth2.Config, config.Provider, error) {
	for _, p := range s.app.Cfg().Auth.Providers {
		if p.ID == id {
			full, err := s.providerEndpoints(ctx, p)
			if err != nil {
				return nil, p, err
			}
			return &oauth2.Config{
				ClientID:     full.ClientID,
				ClientSecret: full.ClientSecret,
				Endpoint:     oauth2.Endpoint{AuthURL: full.AuthURL, TokenURL: full.TokenURL},
				RedirectURL:  s.app.Cfg().Instance.URL + "/auth/" + id + "/callback",
				Scopes:       full.Scopes,
			}, full, nil
		}
	}
	return nil, config.Provider{}, app.ErrNotFound
}

func (s *Server) oauthStart(w http.ResponseWriter, r *http.Request) {
	oc, _, err := s.oauthConfig(r.Context(), r.PathValue("provider"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	state := app.RandomToken(24)
	verifier := oauth2.GenerateVerifier()
	next := r.URL.Query().Get("next")
	if !safeRedirect(next) {
		next = "/"
	}
	link := "0"
	if r.URL.Query().Get("link") == "1" && actorFrom(r).LoggedIn() {
		link = "1"
	}
	s.setCookie(w, r, "buggy_oauth", state+"|"+verifier+"|"+link+"|"+next, 600, true)
	http.Redirect(w, r, oc.AuthCodeURL(state, oauth2.S256ChallengeOption(verifier)), http.StatusFound)
}

func (s *Server) oauthCallback(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := r.PathValue("provider")
	c, err := r.Cookie("buggy_oauth")
	if err != nil {
		s.fail(w, r, app.Errf(400, "login session expired, please try again"))
		return
	}
	s.setCookie(w, r, "buggy_oauth", "", -1, true)
	parts := strings.SplitN(c.Value, "|", 4)
	if len(parts) != 4 || r.URL.Query().Get("state") != parts[0] {
		s.fail(w, r, app.Errf(400, "invalid login state, please try again"))
		return
	}
	if e := r.URL.Query().Get("error"); e != "" {
		s.fail(w, r, app.Errf(400, "login was cancelled (%s)", e))
		return
	}
	oc, prov, err := s.oauthConfig(ctx, id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	tok, err := oc.Exchange(ctx, r.URL.Query().Get("code"), oauth2.VerifierOption(parts[1]))
	if err != nil {
		s.app.Log.Warn("oauth exchange failed", "provider", id, "err", err)
		s.fail(w, r, app.Errf(400, "could not complete login with %s", prov.Name))
		return
	}
	info, err := s.fetchUserInfo(ctx, oc, tok, prov)
	if err != nil {
		s.app.Log.Warn("oauth userinfo failed", "provider", id, "err", err)
		s.fail(w, r, app.Errf(400, "could not read your %s profile", prov.Name))
		return
	}
	act := actorFrom(r)
	var linkTo = act.User
	if parts[2] != "1" {
		linkTo = nil
	}
	u, err := s.app.LoginExternal(ctx, info, linkTo)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if linkTo != nil {
		s.flash(w, r, "ok", prov.Name+" account linked")
		http.Redirect(w, r, "/settings", http.StatusSeeOther)
		return
	}
	stok, exp, err := s.app.NewSession(ctx, u, act.IP, r.UserAgent())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.setCookie(w, r, sessionCookie, stok, int(time.Until(exp).Seconds()), true)
	http.Redirect(w, r, parts[3], http.StatusSeeOther)
}

func claim(m map[string]any, key string) string {
	v, ok := m[key]
	if !ok || v == nil {
		return ""
	}
	switch t := v.(type) {
	case string:
		return t
	case float64:
		return fmt.Sprintf("%.0f", t)
	}
	return fmt.Sprint(v)
}

func (s *Server) fetchUserInfo(ctx context.Context, oc *oauth2.Config, tok *oauth2.Token, p config.Provider) (app.ExternalIdentity, error) {
	cl := oc.Client(ctx, tok)
	get := func(url string, out any) error {
		req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
		req.Header.Set("Accept", "application/json")
		resp, err := cl.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode >= 300 {
			return fmt.Errorf("userinfo status %d", resp.StatusCode)
		}
		return json.NewDecoder(resp.Body).Decode(out)
	}
	var m map[string]any
	if err := get(p.UserInfoURL, &m); err != nil {
		return app.ExternalIdentity{}, err
	}
	id := app.ExternalIdentity{
		Provider:    p.ID,
		Subject:     claim(m, p.IDField),
		Username:    claim(m, p.UsernameField),
		DisplayName: claim(m, p.NameField),
		Email:       claim(m, p.EmailField),
		AvatarURL:   claim(m, p.AvatarField),
		DefaultRole: p.DefaultRole,
	}
	if id.Username == "" {
		id.Username = strings.Split(id.Email, "@")[0]
	}
	if id.Username == "" {
		id.Username = id.DisplayName
	}
	switch p.Type {
	case "discord":
		if av := claim(m, "avatar"); av != "" {
			id.AvatarURL = "https://cdn.discordapp.com/avatars/" + id.Subject + "/" + av + ".png?size=128"
		}
	case "github":
		if id.Email == "" {
			var emails []struct {
				Email    string `json:"email"`
				Primary  bool   `json:"primary"`
				Verified bool   `json:"verified"`
			}
			if get("https://api.github.com/user/emails", &emails) == nil {
				for _, e := range emails {
					if e.Primary && e.Verified {
						id.Email = e.Email
					}
				}
			}
		}
	}
	if v, ok := m["email_verified"].(bool); ok && !v {
		id.Email = ""
	}
	return id, nil
}
