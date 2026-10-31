package app

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/kohanmathers/buggy/internal/store"
)

const RecoveryCodeCount = 10

const recoveryAlphabet = "abcdefghjkmnpqrstuvwxyz23456789"

func newRecoveryCode() string {
	var sb strings.Builder
	limit := 256 - 256%len(recoveryAlphabet)
	buf := make([]byte, 1)
	for n := 0; n < 16; {
		rand.Read(buf)
		if int(buf[0]) >= limit {
			continue
		}
		if n > 0 && n%4 == 0 {
			sb.WriteByte('-')
		}
		sb.WriteByte(recoveryAlphabet[int(buf[0])%len(recoveryAlphabet)])
		n++
	}
	return sb.String()
}

func normalizeRecoveryCode(code string) string {
	var sb strings.Builder
	for _, r := range strings.ToLower(code) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			sb.WriteRune(r)
		}
	}
	return sb.String()
}

func hashRecoveryCode(userID int64, code string) string {
	h := sha256.Sum256([]byte(fmt.Sprintf("buggy-recovery:%d:%s", userID, normalizeRecoveryCode(code))))
	return hex.EncodeToString(h[:])
}

func (a *App) GenerateRecoveryCodes(ctx context.Context, act *Actor) ([]string, error) {
	if err := RequireLogin(act); err != nil {
		return nil, err
	}
	return a.IssueRecoveryCodes(ctx, act.User)
}

func (a *App) IssueRecoveryCodes(ctx context.Context, u *store.User) ([]string, error) {
	codes := make([]string, RecoveryCodeCount)
	hashes := make([]string, RecoveryCodeCount)
	for i := range codes {
		codes[i] = newRecoveryCode()
		hashes[i] = hashRecoveryCode(u.ID, codes[i])
	}
	if err := a.DB.ReplaceRecoveryCodes(ctx, u.ID, hashes); err != nil {
		return nil, err
	}
	a.Log.Info("recovery codes generated", "user", u.Username)
	return codes, nil
}

func (a *App) RecoveryStatus(ctx context.Context, userID int64) (unused, total int) {
	return a.DB.RecoveryCodeCounts(ctx, userID)
}

func (a *App) ResetPasswordWithCode(ctx context.Context, ip, login, code, newPassword string) (*store.User, int, error) {
	cfg := a.Cfg()
	if !cfg.Auth.Local.Enabled {
		return nil, 0, Errf(403, "password login is disabled on this server")
	}
	login = strings.TrimSpace(login)
	if !a.limiter.Allow("recover:ip:"+ip, 10) || !a.limiter.Allow("recover:u:"+strings.ToLower(login), 5) {
		return nil, 0, ErrRateLimited
	}
	if len(newPassword) < cfg.Auth.Local.MinPassword {
		return nil, 0, Invalid(map[string]string{"password": fmt.Sprintf("Password must be at least %d characters", cfg.Auth.Local.MinPassword)})
	}
	bad := Invalid(map[string]string{"_": "That username and recovery code don't match. Check for typos, and remember each code works only once."})
	u, err := a.DB.UserByName(ctx, login)
	if err != nil {
		u, err = a.DB.UserByEmail(ctx, login)
	}
	if err != nil || u.System || normalizeRecoveryCode(code) == "" {
		return nil, 0, bad
	}
	ok, err := a.DB.ConsumeRecoveryCode(ctx, u.ID, hashRecoveryCode(u.ID, code))
	if err != nil {
		return nil, 0, err
	}
	if !ok {
		a.Log.Warn("failed password recovery attempt", "user", u.Username, "ip", ip)
		return nil, 0, bad
	}
	if u.Suspended() {
		return nil, 0, Errf(403, "your account is suspended")
	}
	hash, err := HashPassword(newPassword)
	if err != nil {
		return nil, 0, err
	}
	u.PasswordHash = hash
	if err := a.DB.UpdateUser(ctx, u); err != nil {
		return nil, 0, err
	}
	a.DB.DeleteUserSessions(ctx, u.ID)
	unused, _ := a.DB.RecoveryCodeCounts(ctx, u.ID)
	a.Log.Info("password reset with recovery code", "user", u.Username, "ip", ip, "codes_left", unused)
	return u, unused, nil
}
