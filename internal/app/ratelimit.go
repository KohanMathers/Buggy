package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

type RateLimiter struct {
	mu   sync.Mutex
	hits map[string][]time.Time
}

func NewRateLimiter() *RateLimiter { return &RateLimiter{hits: map[string][]time.Time{}} }

func (r *RateLimiter) Allow(key string, limit int) bool {
	if limit <= 0 {
		return true
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	cut := now.Add(-time.Hour)
	h := r.hits[key]
	i := 0
	for i < len(h) && h[i].Before(cut) {
		i++
	}
	h = h[i:]
	if len(h) >= limit {
		r.hits[key] = h
		return false
	}
	r.hits[key] = append(h, now)
	return true
}

func (r *RateLimiter) Sweep() {
	r.mu.Lock()
	defer r.mu.Unlock()
	cut := time.Now().Add(-time.Hour)
	for k, h := range r.hits {
		if len(h) == 0 || h[len(h)-1].Before(cut) {
			delete(r.hits, k)
		}
	}
}

func (a *App) limit(act *Actor, action string) error {
	if act.System || a.Can(act, "moderation", "") {
		return nil
	}
	n := a.Cfg().Spam.RateLimits[action]
	key := action + ":ip:" + act.IP
	if act.LoggedIn() {
		key = action + ":u:" + act.User.Username
	}
	if !a.limiter.Allow(key, n) {
		return ErrRateLimited
	}
	return nil
}

func (a *App) spamCheck(ctx context.Context, act *Actor, action, captcha string) error {
	if act.System {
		return nil
	}
	if err := RequireLogin(act); err != nil {
		return err
	}
	if err := a.checkMuted(act); err != nil {
		return err
	}
	cfg := a.Cfg()
	if age := cfg.Spam.MinAccountAge.D(); age > 0 && !act.User.Trusted && !a.Can(act, "triage", "") {
		if time.Since(time.Unix(act.User.CreatedAt, 0)) < age {
			return Errf(403, "new accounts must wait %s before posting", age)
		}
	}
	if err := a.limit(act, action); err != nil {
		return err
	}
	if cfg.Spam.Captcha.Enabled(action) && !act.User.Trusted && !a.Can(act, "triage", "") {
		if err := a.VerifyCaptcha(ctx, captcha, act.IP); err != nil {
			return err
		}
	}
	return nil
}

func (a *App) needsModeration(act *Actor, pk string, texts ...string) bool {
	if act.System || a.Can(act, "moderation", pk) || a.Can(act, "triage", pk) {
		return false
	}
	cfg := a.Cfg()
	if cfg.Spam.FirstPostModeration && act.User != nil && !act.User.Trusted {
		return true
	}
	for _, t := range texts {
		lt := strings.ToLower(t)
		for _, w := range cfg.Spam.BlockedWords {
			if w != "" && strings.Contains(lt, strings.ToLower(w)) {
				return true
			}
		}
	}
	return false
}

func (a *App) VerifyCaptcha(ctx context.Context, token, ip string) error {
	c := a.Cfg().Spam.Captcha
	if c.Provider == "" {
		return nil
	}
	if token == "" {
		return ErrCaptchaError
	}
	endpoint := map[string]string{
		"turnstile": "https://challenges.cloudflare.com/turnstile/v0/siteverify",
		"hcaptcha":  "https://api.hcaptcha.com/siteverify",
		"recaptcha": "https://www.google.com/recaptcha/api/siteverify",
	}[c.Provider]
	form := url.Values{"secret": {c.Secret}, "response": {token}, "remoteip": {ip}}
	req, _ := http.NewRequestWithContext(ctx, "POST", endpoint, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := a.HTTP.Do(req)
	if err != nil {
		return ErrCaptchaError
	}
	defer resp.Body.Close()
	var out struct {
		Success bool `json:"success"`
	}
	if json.NewDecoder(resp.Body).Decode(&out) != nil || !out.Success {
		return ErrCaptchaError
	}
	return nil
}
