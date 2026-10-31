package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"
)

type User struct {
	ID                 int64  `json:"id"`
	Username           string `json:"username"`
	DisplayName        string `json:"display_name"`
	Email              string `json:"-"`
	PasswordHash       string `json:"-"`
	Role               string `json:"role"`
	AvatarURL          string `json:"avatar_url"`
	Bio                string `json:"bio"`
	Locale             string `json:"-"`
	EmailNotifications bool   `json:"-"`
	Banned             bool   `json:"banned,omitempty"`
	ShadowBanned       bool   `json:"-"`
	Trusted            bool   `json:"-"`
	System             bool   `json:"system,omitempty"`
	CreatedAt          int64  `json:"created_at"`
	LastSeenAt         int64  `json:"-"`
	BanReason          string `json:"-"`
	BannedUntil        int64  `json:"-"`
	MutedUntil         int64  `json:"-"`
	MuteReason         string `json:"-"`
}

const Forever int64 = 253402300799

func (u *User) Suspended() bool {
	if u == nil || !u.Banned {
		return false
	}
	return u.BannedUntil == 0 || u.BannedUntil > time.Now().Unix()
}

func (u *User) Locked() bool { return u.Suspended() && u.BannedUntil != 0 }

func (u *User) Muted() bool { return u != nil && u.MutedUntil > time.Now().Unix() }

func (u *User) Name() string {
	if u == nil {
		return "unknown"
	}
	if u.DisplayName != "" {
		return u.DisplayName
	}
	return u.Username
}

const userCols = `id, username, display_name, email, password_hash, role, avatar_url, bio, locale,
email_notifications, banned, shadow_banned, trusted, system, created_at, last_seen_at,
ban_reason, banned_until, muted_until, mute_reason`

type scanner interface{ Scan(dest ...any) error }

func scanUser(s scanner) (*User, error) {
	u := &User{}
	err := s.Scan(&u.ID, &u.Username, &u.DisplayName, &u.Email, &u.PasswordHash, &u.Role, &u.AvatarURL,
		&u.Bio, &u.Locale, &u.EmailNotifications, &u.Banned, &u.ShadowBanned, &u.Trusted, &u.System,
		&u.CreatedAt, &u.LastSeenAt, &u.BanReason, &u.BannedUntil, &u.MutedUntil, &u.MuteReason)
	return u, err
}

func (d *DB) CreateUser(ctx context.Context, u *User) error {
	if u.CreatedAt == 0 {
		u.CreatedAt = now()
	}
	id, err := insertID(ctx, d, `INSERT INTO users (username, username_lower, display_name, email, password_hash, role,
		avatar_url, bio, locale, email_notifications, banned, shadow_banned, trusted, system, created_at, last_seen_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		u.Username, strings.ToLower(u.Username), u.DisplayName, u.Email, u.PasswordHash, u.Role, u.AvatarURL,
		u.Bio, u.Locale, u.EmailNotifications, u.Banned, u.ShadowBanned, u.Trusted, u.System, u.CreatedAt, u.LastSeenAt)
	u.ID = id
	return err
}

func (d *DB) UpdateUser(ctx context.Context, u *User) error {
	_, err := d.Exec(ctx, `UPDATE users SET username = ?, username_lower = ?, display_name = ?, email = ?, password_hash = ?,
		role = ?, avatar_url = ?, bio = ?, locale = ?, email_notifications = ?, banned = ?, shadow_banned = ?, trusted = ?,
		ban_reason = ?, banned_until = ?, muted_until = ?, mute_reason = ?
		WHERE id = ?`,
		u.Username, strings.ToLower(u.Username), u.DisplayName, u.Email, u.PasswordHash, u.Role, u.AvatarURL, u.Bio,
		u.Locale, u.EmailNotifications, u.Banned, u.ShadowBanned, u.Trusted,
		u.BanReason, u.BannedUntil, u.MutedUntil, u.MuteReason, u.ID)
	return err
}

func (d *DB) TouchUser(ctx context.Context, id int64) {
	d.Exec(ctx, `UPDATE users SET last_seen_at = ? WHERE id = ?`, now(), id)
}

func (d *DB) UserByID(ctx context.Context, id int64) (*User, error) {
	u, err := scanUser(d.QueryRow(ctx, `SELECT `+userCols+` FROM users WHERE id = ?`, id))
	return u, notFound(err)
}

func (d *DB) UserByName(ctx context.Context, name string) (*User, error) {
	u, err := scanUser(d.QueryRow(ctx, `SELECT `+userCols+` FROM users WHERE username_lower = ?`, strings.ToLower(name)))
	return u, notFound(err)
}

func (d *DB) UserByEmail(ctx context.Context, email string) (*User, error) {
	if email == "" {
		return nil, ErrNotFound
	}
	u, err := scanUser(d.QueryRow(ctx, `SELECT `+userCols+` FROM users WHERE lower(email) = ? AND system = FALSE ORDER BY id LIMIT 1`, strings.ToLower(email)))
	return u, notFound(err)
}

func (d *DB) UsersByIDs(ctx context.Context, ids []int64) (map[int64]*User, error) {
	out := map[int64]*User{}
	uniq := map[int64]bool{}
	var list []int64
	for _, id := range ids {
		if id != 0 && !uniq[id] {
			uniq[id] = true
			list = append(list, id)
		}
	}
	if len(list) == 0 {
		return out, nil
	}
	rows, err := d.Query(ctx, `SELECT `+userCols+` FROM users WHERE id IN (`+placeholders(len(list))+`)`, int64Args(list)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out[u.ID] = u
	}
	return out, rows.Err()
}

func (d *DB) ListUsers(ctx context.Context, q string, limit, offset int) ([]*User, int, error) {
	where := `system = FALSE`
	var args []any
	if q != "" {
		where += ` AND (username_lower LIKE ? OR lower(display_name) LIKE ? OR lower(email) LIKE ?)`
		like := "%" + strings.ToLower(q) + "%"
		args = append(args, like, like, like)
	}
	var total int
	if err := d.QueryRow(ctx, `SELECT COUNT(*) FROM users WHERE `+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := d.Query(ctx, `SELECT `+userCols+` FROM users WHERE `+where+` ORDER BY id DESC LIMIT ? OFFSET ?`,
		append(args, limit, offset)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []*User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, u)
	}
	return out, total, rows.Err()
}

func (d *DB) SearchUsernames(ctx context.Context, prefix string, limit int) ([]*User, error) {
	rows, err := d.Query(ctx, `SELECT `+userCols+` FROM users WHERE username_lower LIKE ? AND system = FALSE AND banned = FALSE ORDER BY last_seen_at DESC LIMIT ?`,
		strings.ToLower(prefix)+"%", limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

func (d *DB) CountUsers(ctx context.Context) (int, error) {
	var n int
	err := d.QueryRow(ctx, `SELECT COUNT(*) FROM users WHERE system = FALSE`).Scan(&n)
	return n, err
}

func (d *DB) CountAdmins(ctx context.Context) (int, error) {
	var n int
	err := d.QueryRow(ctx, `SELECT COUNT(*) FROM users WHERE role = 'admin' AND system = FALSE`).Scan(&n)
	return n, err
}

func (d *DB) UsernameTaken(ctx context.Context, name string) bool {
	var n int
	d.QueryRow(ctx, `SELECT COUNT(*) FROM users WHERE username_lower = ?`, strings.ToLower(name)).Scan(&n)
	return n > 0
}

func (d *DB) UserByIdentity(ctx context.Context, provider, subject string) (*User, error) {
	u, err := scanUser(d.QueryRow(ctx, `SELECT `+prefixCols("u.", userCols)+` FROM users u JOIN identities i ON i.user_id = u.id
		WHERE i.provider = ? AND i.subject = ?`, provider, subject))
	return u, notFound(err)
}

func (d *DB) LinkIdentity(ctx context.Context, provider, subject string, userID int64) error {
	_, err := d.Exec(ctx, `INSERT INTO identities (provider, subject, user_id, created_at) VALUES (?, ?, ?, ?)
		ON CONFLICT (provider, subject) DO UPDATE SET user_id = excluded.user_id`, provider, subject, userID, now())
	return err
}

func (d *DB) UserIdentities(ctx context.Context, userID int64) ([]string, error) {
	rows, err := d.Query(ctx, `SELECT provider FROM identities WHERE user_id = ? ORDER BY provider`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var p string
		rows.Scan(&p)
		out = append(out, p)
	}
	return out, rows.Err()
}

func prefixCols(prefix, cols string) string {
	parts := strings.Split(cols, ",")
	for i, p := range parts {
		parts[i] = prefix + strings.TrimSpace(p)
	}
	return strings.Join(parts, ", ")
}

func HashToken(tok string) string {
	h := sha256.Sum256([]byte(tok))
	return hex.EncodeToString(h[:])
}

func (d *DB) CreateSession(ctx context.Context, token string, userID, expires int64, ip, ua string) error {
	_, err := d.Exec(ctx, `INSERT INTO sessions (token_hash, user_id, created_at, expires_at, ip, user_agent) VALUES (?, ?, ?, ?, ?, ?)`,
		HashToken(token), userID, now(), expires, ip, ua)
	return err
}

func (d *DB) SessionUser(ctx context.Context, token string) (*User, error) {
	u, err := scanUser(d.QueryRow(ctx, `SELECT `+prefixCols("u.", userCols)+` FROM sessions s JOIN users u ON u.id = s.user_id
		WHERE s.token_hash = ? AND s.expires_at > ?`, HashToken(token), now()))
	return u, notFound(err)
}

func (d *DB) DeleteSession(ctx context.Context, token string) error {
	_, err := d.Exec(ctx, `DELETE FROM sessions WHERE token_hash = ?`, HashToken(token))
	return err
}

func (d *DB) DeleteUserSessions(ctx context.Context, userID int64) error {
	_, err := d.Exec(ctx, `DELETE FROM sessions WHERE user_id = ?`, userID)
	return err
}

func (d *DB) PurgeExpiredSessions(ctx context.Context) {
	d.Exec(ctx, `DELETE FROM sessions WHERE expires_at < ?`, now())
}

type APIToken struct {
	ID         int64  `json:"id"`
	UserID     int64  `json:"-"`
	Name       string `json:"name"`
	Prefix     string `json:"prefix"`
	CreatedAt  int64  `json:"created_at"`
	LastUsedAt int64  `json:"last_used_at"`
}

func (d *DB) CreateAPIToken(ctx context.Context, userID int64, name, token string) (*APIToken, error) {
	t := &APIToken{UserID: userID, Name: name, Prefix: token[:12], CreatedAt: now()}
	id, err := insertID(ctx, d, `INSERT INTO api_tokens (user_id, name, token_hash, prefix, created_at) VALUES (?, ?, ?, ?, ?)`,
		userID, name, HashToken(token), t.Prefix, t.CreatedAt)
	t.ID = id
	return t, err
}

func (d *DB) APITokenUser(ctx context.Context, token string) (*User, error) {
	h := HashToken(token)
	u, err := scanUser(d.QueryRow(ctx, `SELECT `+prefixCols("u.", userCols)+` FROM api_tokens t JOIN users u ON u.id = t.user_id
		WHERE t.token_hash = ?`, h))
	if err == nil {
		d.Exec(ctx, `UPDATE api_tokens SET last_used_at = ? WHERE token_hash = ?`, now(), h)
	}
	return u, notFound(err)
}

func (d *DB) ListAPITokens(ctx context.Context, userID int64) ([]*APIToken, error) {
	rows, err := d.Query(ctx, `SELECT id, user_id, name, prefix, created_at, last_used_at FROM api_tokens WHERE user_id = ? ORDER BY id DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*APIToken
	for rows.Next() {
		t := &APIToken{}
		if err := rows.Scan(&t.ID, &t.UserID, &t.Name, &t.Prefix, &t.CreatedAt, &t.LastUsedAt); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (d *DB) DeleteAPIToken(ctx context.Context, userID, id int64) error {
	_, err := d.Exec(ctx, `DELETE FROM api_tokens WHERE id = ? AND user_id = ?`, id, userID)
	return err
}

func (d *DB) DeleteUserAPITokens(ctx context.Context, userID int64) (int64, error) {
	res, err := d.Exec(ctx, `DELETE FROM api_tokens WHERE user_id = ?`, userID)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (d *DB) CountUserSessions(ctx context.Context, userID int64) int {
	return d.Count(ctx, `SELECT COUNT(*) FROM sessions WHERE user_id = ? AND expires_at > ?`, userID, now())
}

type ModLogEntry struct {
	ID        int64  `json:"id"`
	ActorID   int64  `json:"-"`
	TargetID  int64  `json:"-"`
	Action    string `json:"action"`
	Reason    string `json:"reason"`
	Until     int64  `json:"until,omitempty"`
	CreatedAt int64  `json:"created_at"`
	Actor     *User  `json:"actor,omitempty"`
}

func (d *DB) AddModLog(ctx context.Context, e *ModLogEntry) error {
	e.CreatedAt = now()
	id, err := insertID(ctx, d, `INSERT INTO mod_log (actor_id, target_id, action, reason, until_at, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		e.ActorID, e.TargetID, e.Action, e.Reason, e.Until, e.CreatedAt)
	e.ID = id
	return err
}

func (d *DB) ModLog(ctx context.Context, targetID int64, limit int) ([]*ModLogEntry, error) {
	rows, err := d.Query(ctx, `SELECT id, actor_id, target_id, action, reason, until_at, created_at FROM mod_log
		WHERE target_id = ? ORDER BY created_at DESC, id DESC LIMIT ?`, targetID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*ModLogEntry
	var ids []int64
	for rows.Next() {
		e := &ModLogEntry{}
		if err := rows.Scan(&e.ID, &e.ActorID, &e.TargetID, &e.Action, &e.Reason, &e.Until, &e.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
		ids = append(ids, e.ActorID)
	}
	rows.Close()
	users, _ := d.UsersByIDs(ctx, ids)
	for _, e := range out {
		e.Actor = users[e.ActorID]
	}
	return out, nil
}
