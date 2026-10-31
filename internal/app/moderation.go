package app

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/kohanmathers/buggy/internal/config"
	"github.com/kohanmathers/buggy/internal/store"
)

var ModerationActions = []string{"kick", "lock", "ban", "unban", "mute", "unmute", "shadowban", "unshadowban", "trust", "untrust", "revoke_tokens"}

func fmtUntil(ts int64) string {
	return time.Unix(ts, 0).UTC().Format("2006-01-02 15:04 UTC")
}

func SuspendedError(u *store.User) *Error {
	msg := "Your account has been permanently suspended."
	if u.BannedUntil != 0 {
		msg = "Your account is locked until " + fmtUntil(u.BannedUntil) + "."
	}
	if u.BanReason != "" {
		msg += " Reason: " + u.BanReason
	}
	return &Error{Status: 403, Msg: msg}
}

func MutedError(u *store.User) *Error {
	msg := "You're muted and can't post right now."
	if u.MutedUntil != store.Forever {
		msg = "You're muted until " + fmtUntil(u.MutedUntil) + " and can't post until then."
	}
	if u.MuteReason != "" {
		msg += " Reason: " + u.MuteReason
	}
	return &Error{Status: 403, Msg: msg}
}

func (a *App) checkMuted(act *Actor) error {
	if act != nil && !act.System && act.User != nil && act.User.Muted() {
		return MutedError(act.User)
	}
	return nil
}

type ModerationRequest struct {
	Action   string
	Duration time.Duration
	Reason   string
}

func (a *App) ModerateUser(ctx context.Context, act *Actor, username string, req ModerationRequest) (*store.User, error) {
	if err := RequireLogin(act); err != nil && !act.System {
		return nil, err
	}
	if !a.Can(act, "moderation", "") {
		return nil, ErrForbidden
	}
	u, err := a.DB.UserByName(ctx, username)
	if err != nil || u.System {
		return nil, ErrNotFound
	}
	if !act.System {
		if u.ID == act.ID() {
			return nil, Errf(422, "you can't moderate your own account")
		}
		target := &Actor{User: &store.User{ID: u.ID, Username: u.Username, Role: u.Role}}
		if (a.Can(target, "moderation", "") || a.Can(target, "user.manage", "")) && !a.Can(act, "user.manage", "") {
			return nil, Errf(403, "only administrators can moderate staff accounts")
		}
		if a.roleFor(a.Cfg(), u) == "admin" && a.roleFor(a.Cfg(), act.User) != "admin" {
			return nil, Errf(403, "only administrators can moderate other administrators")
		}
	}
	req.Reason = strings.TrimSpace(req.Reason)
	if len(req.Reason) > 500 {
		return nil, Invalid(map[string]string{"reason": "Reason must be at most 500 characters"})
	}
	now := time.Now()
	entry := &store.ModLogEntry{ActorID: act.ID(), TargetID: u.ID, Action: req.Action, Reason: req.Reason}
	kick := false
	switch req.Action {
	case "kick":
		kick = true
	case "lock":
		if req.Duration <= 0 {
			return nil, Invalid(map[string]string{"duration": "Choose how long to lock the account for"})
		}
		u.Banned, u.BannedUntil, u.BanReason = true, now.Add(req.Duration).Unix(), req.Reason
		entry.Until = u.BannedUntil
		kick = true
	case "ban":
		u.Banned, u.BannedUntil, u.BanReason = true, 0, req.Reason
		kick = true
	case "unban":
		u.Banned, u.BannedUntil, u.BanReason = false, 0, ""
	case "mute":
		u.MutedUntil = store.Forever
		if req.Duration > 0 {
			u.MutedUntil = now.Add(req.Duration).Unix()
		}
		u.MuteReason = req.Reason
		entry.Until = u.MutedUntil
	case "unmute":
		u.MutedUntil, u.MuteReason = 0, ""
	case "shadowban":
		u.ShadowBanned = true
	case "unshadowban":
		u.ShadowBanned = false
	case "trust":
		u.Trusted = true
	case "untrust":
		u.Trusted = false
	case "revoke_tokens":
		n, err := a.DB.DeleteUserAPITokens(ctx, u.ID)
		if err != nil {
			return nil, err
		}
		if entry.Reason == "" {
			entry.Reason = fmt.Sprintf("%d token(s) revoked", n)
		}
	default:
		return nil, Errf(422, "unknown action %q (known: %s)", req.Action, strings.Join(ModerationActions, ", "))
	}
	if err := a.DB.UpdateUser(ctx, u); err != nil {
		return nil, err
	}
	if kick {
		if err := a.DB.DeleteUserSessions(ctx, u.ID); err != nil {
			return nil, err
		}
	}
	if err := a.DB.AddModLog(ctx, entry); err != nil {
		return nil, err
	}
	by := "system"
	if act.User != nil {
		by = act.User.Username
	}
	a.Log.Info("account moderated", "user", u.Username, "action", req.Action, "by", by, "reason", req.Reason)
	return u, nil
}

func (a *App) ModerationLog(ctx context.Context, act *Actor, userID int64) ([]*store.ModLogEntry, error) {
	if !a.Can(act, "moderation", "") {
		return nil, ErrForbidden
	}
	return a.DB.ModLog(ctx, userID, 100)
}

func ParseModDuration(s string) (time.Duration, error) {
	s = strings.TrimSpace(strings.ToLower(s))
	if s == "" || s == "permanent" || s == "forever" || s == "0" {
		return 0, nil
	}
	d, err := config.ParseDuration(s)
	if err != nil || d < 0 {
		return 0, Invalid(map[string]string{"duration": "Use a duration like 1h, 7d or 2w"})
	}
	return d, nil
}
