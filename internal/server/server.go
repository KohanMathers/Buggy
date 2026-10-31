// Package server exposes buggy over HTTP.
package server

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kohanmathers/buggy/internal/app"
	"github.com/kohanmathers/buggy/internal/store"
)

type ctxKey int

const (
	actorKey ctxKey = iota
	csrfKey
	bearerKey
)

type Server struct {
	app     *app.App
	embedFS fs.FS
	secret  []byte
	Version string

	tmplMu      sync.Mutex
	tmplCache   map[string]*tmplEntry
	assetHashes map[string]string
	metrics     *httpMetrics
	oauth       *oauthCache
}

func New(a *app.App, embedFS fs.FS, secret []byte, version string) *Server {
	s := &Server{app: a, embedFS: embedFS, secret: secret, Version: version,
		tmplCache: map[string]*tmplEntry{}, assetHashes: map[string]string{}, metrics: newHTTPMetrics(), oauth: newOAuthCache()}
	a.Conf.OnReload(func(_ *configT) { s.clearTemplates() })
	return s
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	s.routes(mux)
	s.apiRoutes(mux)
	var h http.Handler = mux
	h = s.withActor(h)
	h = s.withSecurityHeaders(h)
	h = s.withRecover(h)
	h = s.withMetrics(h)
	return h
}

func actorFrom(r *http.Request) *app.Actor {
	if a, ok := r.Context().Value(actorKey).(*app.Actor); ok {
		return a
	}
	return &app.Actor{}
}

func (s *Server) clientIP(r *http.Request) string {
	if s.app.Cfg().Server.TrustProxy {
		if f := r.Header.Get("X-Forwarded-For"); f != "" {
			ip, _, _ := strings.Cut(f, ",")
			return strings.TrimSpace(ip)
		}
		if ip := r.Header.Get("X-Real-IP"); ip != "" {
			return ip
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

const (
	sessionCookie = "buggy_session"
	csrfCookie    = "buggy_csrf"
	flashCookie   = "buggy_flash"
)

func (s *Server) secureCookies(r *http.Request) bool {
	switch s.app.Cfg().Server.CookieSecure {
	case "true":
		return true
	case "false":
		return false
	}
	return r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" || strings.HasPrefix(s.app.Cfg().Instance.URL, "https://")
}

func (s *Server) setCookie(w http.ResponseWriter, r *http.Request, name, value string, maxAge int, httpOnly bool) {
	http.SetCookie(w, &http.Cookie{Name: name, Value: value, Path: "/", MaxAge: maxAge, HttpOnly: httpOnly,
		Secure: s.secureCookies(r), SameSite: http.SameSiteLaxMode})
}

func (s *Server) csrfToken(seed string) string {
	m := hmac.New(sha256.New, s.secret)
	m.Write([]byte("csrf:" + seed))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

func (s *Server) withActor(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		act := &app.Actor{IP: s.clientIP(r)}
		bearer := false
		if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
			tok := strings.TrimSpace(strings.TrimPrefix(h, "Bearer "))
			u, err := s.app.DB.APITokenUser(ctx, tok)
			if err != nil {
				writeJSON(w, 401, map[string]any{"error": "invalid API token"})
				return
			}
			act.User, bearer = u, true
		} else if c, err := r.Cookie(sessionCookie); err == nil && c.Value != "" {
			if u, err := s.app.DB.SessionUser(ctx, c.Value); err == nil {
				act.User = u
				if time.Since(time.Unix(u.LastSeenAt, 0)) > 5*time.Minute {
					s.app.DB.TouchUser(ctx, u.ID)
				}
			} else {
				s.setCookie(w, r, sessionCookie, "", -1, true)
			}
		}
		if act.User != nil && act.User.Suspended() {
			act.User = nil
		}
		seed := ""
		if c, err := r.Cookie(csrfCookie); err == nil && len(c.Value) >= 16 {
			seed = c.Value
		} else {
			seed = app.RandomToken(18)
			s.setCookie(w, r, csrfCookie, seed, 365*24*3600, true)
		}
		token := s.csrfToken(seed)
		if r.Body != nil && r.ContentLength != 0 {
			r.Body = http.MaxBytesReader(w, r.Body, int64(s.app.Cfg().Storage.MaxSize)*6+(2<<20))
		}
		unsafe := r.Method != "GET" && r.Method != "HEAD" && r.Method != "OPTIONS"
		if unsafe && !bearer && !strings.HasPrefix(r.URL.Path, "/hooks/") {
			got := r.Header.Get("X-CSRF-Token")
			if got == "" {
				got = r.FormValue("_csrf")
			}
			if subtle.ConstantTimeCompare([]byte(got), []byte(token)) != 1 {
				if strings.HasPrefix(r.URL.Path, "/api/") || wantsJSON(r) {
					writeJSON(w, 403, map[string]any{"error": "invalid or missing CSRF token (send X-CSRF-Token or use an API token)"})
				} else {
					http.Error(w, "Your session form expired. Go back, refresh the page and try again.", 403)
				}
				return
			}
		}
		ctx = context.WithValue(ctx, actorKey, act)
		ctx = context.WithValue(ctx, csrfKey, token)
		ctx = context.WithValue(ctx, bearerKey, bearer)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (s *Server) withSecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
		h.Set("X-Frame-Options", "SAMEORIGIN")
		next.ServeHTTP(w, r)
	})
}

func (s *Server) withRecover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				if v == http.ErrAbortHandler {
					panic(v)
				}
				s.app.Log.Error("panic", "path", r.URL.Path, "panic", v, "stack", string(debug.Stack()))
				http.Error(w, "internal error", 500)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func wantsJSON(r *http.Request) bool {
	return strings.Contains(r.Header.Get("Accept"), "application/json") || r.Header.Get("X-Requested-With") == "fetch"
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(true)
	enc.Encode(v)
}

func errJSON(w http.ResponseWriter, err error) {
	status := app.Status(err)
	body := map[string]any{"error": err.Error()}
	var ae *app.Error
	if errors.As(err, &ae) && ae.Fields != nil {
		body["fields"] = ae.Fields
	}
	if status == 500 {
		body["error"] = "internal error"
	}
	writeJSON(w, status, body)
}

func (s *Server) flash(w http.ResponseWriter, r *http.Request, kind, msg string) {
	s.setCookie(w, r, flashCookie, url.QueryEscape(kind+"|"+msg), 60, true)
}

func (s *Server) takeFlash(w http.ResponseWriter, r *http.Request) (string, string) {
	c, err := r.Cookie(flashCookie)
	if err != nil {
		return "", ""
	}
	s.setCookie(w, r, flashCookie, "", -1, true)
	v, _ := url.QueryUnescape(c.Value)
	kind, msg, _ := strings.Cut(v, "|")
	return kind, msg
}

func back(w http.ResponseWriter, r *http.Request, fallback string) {
	to := fallback
	if ref := r.FormValue("next"); ref != "" && safeRedirect(ref) {
		to = ref
	} else if ref := r.Referer(); ref != "" {
		if u, err := url.Parse(ref); err == nil && u.Host == r.Host {
			to = u.RequestURI()
		}
	}
	http.Redirect(w, r, to, http.StatusSeeOther)
}

func safeRedirect(p string) bool {
	return strings.HasPrefix(p, "/") && !strings.HasPrefix(p, "//") && !strings.HasPrefix(p, "/\\")
}

func atoi64(s string) int64 {
	n, _ := strconv.ParseInt(s, 10, 64)
	return n
}

func formBool(r *http.Request, k string) bool {
	switch r.FormValue(k) {
	case "1", "true", "on", "yes":
		return true
	}
	return false
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == '\n' }) {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func userURL(u *store.User) string {
	if u == nil {
		return "#"
	}
	return "/users/" + url.PathEscape(u.Username)
}

var _ = fmt.Sprint
