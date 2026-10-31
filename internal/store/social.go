package store

import (
	"context"
	"database/sql"
)

type Comment struct {
	ID         int64  `json:"id"`
	IssueID    int64  `json:"-"`
	AuthorID   int64  `json:"-"`
	Body       string `json:"body"`
	Restricted bool   `json:"restricted"`
	Moderation string `json:"moderation,omitempty"`
	Deleted    bool   `json:"deleted,omitempty"`
	CreatedAt  int64  `json:"created_at"`
	UpdatedAt  int64  `json:"updated_at"`
	Author     *User  `json:"author,omitempty"`
}

const commentCols = `id, issue_id, author_id, body, restricted, moderation, deleted, created_at, updated_at`

func scanComment(s scanner) (*Comment, error) {
	c := &Comment{}
	err := s.Scan(&c.ID, &c.IssueID, &c.AuthorID, &c.Body, &c.Restricted, &c.Moderation, &c.Deleted, &c.CreatedAt, &c.UpdatedAt)
	return c, err
}

func (d *DB) CreateComment(ctx context.Context, q Q, c *Comment) error {
	if c.CreatedAt == 0 {
		c.CreatedAt = now()
	}
	c.UpdatedAt = c.CreatedAt
	if c.Moderation == "" {
		c.Moderation = "approved"
	}
	id, err := insertID(ctx, q, `INSERT INTO comments (issue_id, author_id, body, restricted, moderation, deleted, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, FALSE, ?, ?)`, c.IssueID, c.AuthorID, c.Body, c.Restricted, c.Moderation, c.CreatedAt, c.UpdatedAt)
	c.ID = id
	return err
}

func (d *DB) SaveComment(ctx context.Context, q Q, c *Comment) error {
	_, err := q.Exec(ctx, `UPDATE comments SET body = ?, restricted = ?, moderation = ?, deleted = ?, updated_at = ? WHERE id = ?`,
		c.Body, c.Restricted, c.Moderation, c.Deleted, c.UpdatedAt, c.ID)
	return err
}

func (d *DB) CommentByID(ctx context.Context, id int64) (*Comment, error) {
	c, err := scanComment(d.QueryRow(ctx, `SELECT `+commentCols+` FROM comments WHERE id = ?`, id))
	return c, notFound(err)
}

func (d *DB) Comments(ctx context.Context, issueID int64) ([]*Comment, error) {
	rows, err := d.Query(ctx, `SELECT `+commentCols+` FROM comments WHERE issue_id = ? ORDER BY created_at, id`, issueID)
	if err != nil {
		return nil, err
	}
	return collectComments(ctx, d, rows)
}

func collectComments(ctx context.Context, d *DB, rows *sql.Rows) ([]*Comment, error) {
	defer rows.Close()
	var out []*Comment
	var uids []int64
	for rows.Next() {
		c, err := scanComment(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
		uids = append(uids, c.AuthorID)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	users, err := d.UsersByIDs(ctx, uids)
	if err != nil {
		return nil, err
	}
	for _, c := range out {
		c.Author = users[c.AuthorID]
	}
	return out, nil
}

func (d *DB) CountComments(ctx context.Context, q Q, issueID int64) int64 {
	var n int64
	q.QueryRow(ctx, `SELECT COUNT(*) FROM comments WHERE issue_id = ? AND deleted = FALSE AND moderation = 'approved' AND restricted = FALSE`, issueID).Scan(&n)
	return n
}

type Attachment struct {
	ID          int64  `json:"id"`
	IssueID     int64  `json:"-"`
	UploaderID  int64  `json:"-"`
	Filename    string `json:"filename"`
	ContentType string `json:"content_type"`
	Size        int64  `json:"size"`
	StorageKey  string `json:"-"`
	Deleted     bool   `json:"-"`
	CreatedAt   int64  `json:"created_at"`
	Uploader    *User  `json:"uploader,omitempty"`
}

const attachmentCols = `id, issue_id, uploader_id, filename, content_type, size, storage_key, deleted, created_at`

func scanAttachment(s scanner) (*Attachment, error) {
	a := &Attachment{}
	err := s.Scan(&a.ID, &a.IssueID, &a.UploaderID, &a.Filename, &a.ContentType, &a.Size, &a.StorageKey, &a.Deleted, &a.CreatedAt)
	return a, err
}

func (d *DB) CreateAttachment(ctx context.Context, q Q, a *Attachment) error {
	if a.CreatedAt == 0 {
		a.CreatedAt = now()
	}
	id, err := insertID(ctx, q, `INSERT INTO attachments (issue_id, uploader_id, filename, content_type, size, storage_key, deleted, created_at)
		VALUES (?, ?, ?, ?, ?, ?, FALSE, ?)`, a.IssueID, a.UploaderID, a.Filename, a.ContentType, a.Size, a.StorageKey, a.CreatedAt)
	a.ID = id
	return err
}

func (d *DB) Attachments(ctx context.Context, issueID int64) ([]*Attachment, error) {
	rows, err := d.Query(ctx, `SELECT `+attachmentCols+` FROM attachments WHERE issue_id = ? AND deleted = FALSE ORDER BY id`, issueID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Attachment
	var uids []int64
	for rows.Next() {
		a, err := scanAttachment(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
		uids = append(uids, a.UploaderID)
	}
	rows.Close()
	users, _ := d.UsersByIDs(ctx, uids)
	for _, a := range out {
		a.Uploader = users[a.UploaderID]
	}
	return out, rows.Err()
}

func (d *DB) AttachmentByID(ctx context.Context, id int64) (*Attachment, error) {
	a, err := scanAttachment(d.QueryRow(ctx, `SELECT `+attachmentCols+` FROM attachments WHERE id = ?`, id))
	return a, notFound(err)
}

func (d *DB) DeleteAttachment(ctx context.Context, id int64) error {
	_, err := d.Exec(ctx, `UPDATE attachments SET deleted = TRUE WHERE id = ?`, id)
	return err
}

type HistoryEntry struct {
	ID        int64  `json:"id"`
	IssueID   int64  `json:"-"`
	ActorID   int64  `json:"-"`
	Field     string `json:"field"`
	Old       string `json:"old"`
	New       string `json:"new"`
	CreatedAt int64  `json:"created_at"`
	Actor     *User  `json:"actor,omitempty"`
}

func (d *DB) AddHistory(ctx context.Context, q Q, issueID, actorID int64, field, old, new string) error {
	if old == new {
		return nil
	}
	_, err := q.Exec(ctx, `INSERT INTO history (issue_id, actor_id, field, old_value, new_value, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		issueID, actorID, field, old, new, now())
	return err
}

func (d *DB) History(ctx context.Context, issueID int64) ([]*HistoryEntry, error) {
	rows, err := d.Query(ctx, `SELECT id, issue_id, actor_id, field, old_value, new_value, created_at FROM history WHERE issue_id = ? ORDER BY created_at, id`, issueID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*HistoryEntry
	var uids []int64
	for rows.Next() {
		h := &HistoryEntry{}
		if err := rows.Scan(&h.ID, &h.IssueID, &h.ActorID, &h.Field, &h.Old, &h.New, &h.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, h)
		uids = append(uids, h.ActorID)
	}
	rows.Close()
	users, _ := d.UsersByIDs(ctx, uids)
	for _, h := range out {
		h.Actor = users[h.ActorID]
	}
	return out, nil
}

func (d *DB) SetVote(ctx context.Context, q Q, issueID, userID int64, on bool) (bool, error) {
	var res sql.Result
	var err error
	if on {
		res, err = q.Exec(ctx, `INSERT INTO votes (issue_id, user_id, created_at) VALUES (?, ?, ?) ON CONFLICT DO NOTHING`, issueID, userID, now())
	} else {
		res, err = q.Exec(ctx, `DELETE FROM votes WHERE issue_id = ? AND user_id = ?`, issueID, userID)
	}
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	if n > 0 {
		delta := 1
		if !on {
			delta = -1
		}
		_, err = q.Exec(ctx, `UPDATE issues SET votes = votes + ? WHERE id = ?`, delta, issueID)
	}
	return n > 0, err
}

func (d *DB) SetWatch(ctx context.Context, q Q, issueID, userID int64, on bool) (bool, error) {
	var res sql.Result
	var err error
	if on {
		res, err = q.Exec(ctx, `INSERT INTO watchers (issue_id, user_id) VALUES (?, ?) ON CONFLICT DO NOTHING`, issueID, userID)
	} else {
		res, err = q.Exec(ctx, `DELETE FROM watchers WHERE issue_id = ? AND user_id = ?`, issueID, userID)
	}
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	if n > 0 {
		delta := 1
		if !on {
			delta = -1
		}
		_, err = q.Exec(ctx, `UPDATE issues SET watchers = watchers + ? WHERE id = ?`, delta, issueID)
	}
	return n > 0, err
}

func (d *DB) HasVoted(ctx context.Context, issueID, userID int64) bool {
	var n int
	d.QueryRow(ctx, `SELECT COUNT(*) FROM votes WHERE issue_id = ? AND user_id = ?`, issueID, userID).Scan(&n)
	return n > 0
}

func (d *DB) IsWatching(ctx context.Context, issueID, userID int64) bool {
	var n int
	d.QueryRow(ctx, `SELECT COUNT(*) FROM watchers WHERE issue_id = ? AND user_id = ?`, issueID, userID).Scan(&n)
	return n > 0
}

func (d *DB) Watchers(ctx context.Context, issueID int64) ([]int64, error) {
	return d.ids(ctx, `SELECT user_id FROM watchers WHERE issue_id = ?`, issueID)
}

func (d *DB) Voters(ctx context.Context, issueID int64) ([]int64, error) {
	return d.ids(ctx, `SELECT user_id FROM votes WHERE issue_id = ?`, issueID)
}

func (d *DB) ids(ctx context.Context, q string, args ...any) ([]int64, error) {
	rows, err := d.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		rows.Scan(&id)
		out = append(out, id)
	}
	return out, rows.Err()
}

func (d *DB) MoveSocial(ctx context.Context, q Q, from, to int64) (int64, error) {
	res, err := q.Exec(ctx, `INSERT INTO votes (issue_id, user_id, created_at) SELECT CAST(? AS BIGINT), user_id, created_at FROM votes WHERE issue_id = ? ON CONFLICT DO NOTHING`, to, from)
	if err != nil {
		return 0, err
	}
	moved, _ := res.RowsAffected()
	if _, err := q.Exec(ctx, `INSERT INTO watchers (issue_id, user_id) SELECT CAST(? AS BIGINT), user_id FROM watchers WHERE issue_id = ? ON CONFLICT DO NOTHING`, to, from); err != nil {
		return 0, err
	}
	if _, err := q.Exec(ctx, `INSERT INTO confirmations (issue_id, user_id, version_id, env, note, created_at)
		SELECT CAST(? AS BIGINT), user_id, version_id, env, note, created_at FROM confirmations WHERE issue_id = ?`, to, from); err != nil {
		return 0, err
	}
	_, err = q.Exec(ctx, `UPDATE issues SET
		votes = votes + ?,
		watchers = (SELECT COUNT(*) FROM watchers WHERE issue_id = ?),
		confirmations = (SELECT COUNT(*) FROM confirmations WHERE issue_id = ?)
		WHERE id = ?`, moved, to, to, to)
	return moved, err
}

type Link struct {
	ID        int64  `json:"id"`
	SourceID  int64  `json:"-"`
	TargetID  int64  `json:"-"`
	Kind      string `json:"kind"`
	CreatedBy int64  `json:"-"`
	CreatedAt int64  `json:"created_at"`
	Other     *Issue `json:"issue"`
	Outgoing  bool   `json:"outgoing"`
}

func (d *DB) AddLink(ctx context.Context, q Q, source, target int64, kind string, by int64) error {
	_, err := q.Exec(ctx, `INSERT INTO links (source_id, target_id, kind, created_by, created_at) VALUES (?, ?, ?, ?, ?) ON CONFLICT DO NOTHING`,
		source, target, kind, by, now())
	return err
}

func (d *DB) RemoveLink(ctx context.Context, id int64) (*Link, error) {
	l := &Link{}
	err := d.QueryRow(ctx, `SELECT id, source_id, target_id, kind FROM links WHERE id = ?`, id).Scan(&l.ID, &l.SourceID, &l.TargetID, &l.Kind)
	if err != nil {
		return nil, notFound(err)
	}
	_, err = d.Exec(ctx, `DELETE FROM links WHERE id = ?`, id)
	return l, err
}

func (d *DB) Links(ctx context.Context, issueID int64) ([]*Link, error) {
	rows, err := d.Query(ctx, `SELECT id, source_id, target_id, kind, created_by, created_at FROM links WHERE source_id = ? OR target_id = ? ORDER BY id`, issueID, issueID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Link
	var other []int64
	for rows.Next() {
		l := &Link{}
		if err := rows.Scan(&l.ID, &l.SourceID, &l.TargetID, &l.Kind, &l.CreatedBy, &l.CreatedAt); err != nil {
			return nil, err
		}
		l.Outgoing = l.SourceID == issueID
		if l.Outgoing {
			other = append(other, l.TargetID)
		} else {
			other = append(other, l.SourceID)
		}
		out = append(out, l)
	}
	rows.Close()
	issues, err := d.IssuesByIDs(ctx, other)
	if err != nil {
		return nil, err
	}
	var res []*Link
	for _, l := range out {
		if l.Outgoing {
			l.Other = issues[l.TargetID]
		} else {
			l.Other = issues[l.SourceID]
		}
		if l.Other != nil {
			res = append(res, l)
		}
	}
	return res, nil
}

type Confirmation struct {
	ID        int64  `json:"id"`
	IssueID   int64  `json:"-"`
	UserID    int64  `json:"-"`
	VersionID int64  `json:"-"`
	Env       string `json:"env"`
	Note      string `json:"note"`
	CreatedAt int64  `json:"created_at"`
	User      *User  `json:"user,omitempty"`
	Version   string `json:"version,omitempty"`
}

func (d *DB) AddConfirmation(ctx context.Context, q Q, c *Confirmation) error {
	c.CreatedAt = now()
	id, err := insertID(ctx, q, `INSERT INTO confirmations (issue_id, user_id, version_id, env, note, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		c.IssueID, c.UserID, c.VersionID, c.Env, c.Note, c.CreatedAt)
	if err != nil {
		return err
	}
	c.ID = id
	_, err = q.Exec(ctx, `UPDATE issues SET confirmations = confirmations + 1 WHERE id = ?`, c.IssueID)
	return err
}

func (d *DB) Confirmations(ctx context.Context, issueID int64) ([]*Confirmation, error) {
	rows, err := d.Query(ctx, `SELECT c.id, c.issue_id, c.user_id, c.version_id, c.env, c.note, c.created_at, COALESCE(v.name, '')
		FROM confirmations c LEFT JOIN versions v ON v.id = c.version_id WHERE c.issue_id = ? ORDER BY c.created_at DESC`, issueID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Confirmation
	var uids []int64
	for rows.Next() {
		c := &Confirmation{}
		if err := rows.Scan(&c.ID, &c.IssueID, &c.UserID, &c.VersionID, &c.Env, &c.Note, &c.CreatedAt, &c.Version); err != nil {
			return nil, err
		}
		out = append(out, c)
		uids = append(uids, c.UserID)
	}
	rows.Close()
	users, _ := d.UsersByIDs(ctx, uids)
	for _, c := range out {
		c.User = users[c.UserID]
	}
	return out, nil
}

type Commit struct {
	ID        int64  `json:"id"`
	IssueID   int64  `json:"-"`
	Repo      string `json:"repo"`
	SHA       string `json:"sha"`
	Message   string `json:"message"`
	URL       string `json:"url"`
	Author    string `json:"author"`
	CreatedAt int64  `json:"created_at"`
}

func (d *DB) AddCommit(ctx context.Context, c *Commit) (bool, error) {
	if c.CreatedAt == 0 {
		c.CreatedAt = now()
	}
	res, err := d.Exec(ctx, `INSERT INTO commits (issue_id, repo, sha, message, url, author, created_at) VALUES (?, ?, ?, ?, ?, ?, ?) ON CONFLICT DO NOTHING`,
		c.IssueID, c.Repo, c.SHA, c.Message, c.URL, c.Author, c.CreatedAt)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

func (d *DB) Commits(ctx context.Context, issueID int64) ([]*Commit, error) {
	rows, err := d.Query(ctx, `SELECT id, issue_id, repo, sha, message, url, author, created_at FROM commits WHERE issue_id = ? ORDER BY created_at`, issueID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Commit
	for rows.Next() {
		c := &Commit{}
		if err := rows.Scan(&c.ID, &c.IssueID, &c.Repo, &c.SHA, &c.Message, &c.URL, &c.Author, &c.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

type Notification struct {
	ID        int64  `json:"id"`
	UserID    int64  `json:"-"`
	IssueID   int64  `json:"-"`
	ActorID   int64  `json:"-"`
	Kind      string `json:"kind"`
	Title     string `json:"title"`
	Body      string `json:"body"`
	URL       string `json:"url"`
	Read      bool   `json:"read"`
	CreatedAt int64  `json:"created_at"`
	Actor     *User  `json:"actor,omitempty"`
}

func (d *DB) CreateNotification(ctx context.Context, n *Notification) error {
	n.CreatedAt = now()
	id, err := insertID(ctx, d, `INSERT INTO notifications (user_id, issue_id, actor_id, kind, title, body, url, read, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, FALSE, ?)`, n.UserID, n.IssueID, n.ActorID, n.Kind, n.Title, n.Body, n.URL, n.CreatedAt)
	n.ID = id
	return err
}

func (d *DB) Notifications(ctx context.Context, userID int64, unreadOnly bool, limit int) ([]*Notification, error) {
	q := `SELECT id, user_id, issue_id, actor_id, kind, title, body, url, read, created_at FROM notifications WHERE user_id = ?`
	if unreadOnly {
		q += ` AND read = FALSE`
	}
	rows, err := d.Query(ctx, q+` ORDER BY created_at DESC, id DESC LIMIT ?`, userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Notification
	var uids []int64
	for rows.Next() {
		n := &Notification{}
		if err := rows.Scan(&n.ID, &n.UserID, &n.IssueID, &n.ActorID, &n.Kind, &n.Title, &n.Body, &n.URL, &n.Read, &n.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, n)
		uids = append(uids, n.ActorID)
	}
	rows.Close()
	users, _ := d.UsersByIDs(ctx, uids)
	for _, n := range out {
		n.Actor = users[n.ActorID]
	}
	return out, nil
}

func (d *DB) UnreadCount(ctx context.Context, userID int64) int {
	var n int
	d.QueryRow(ctx, `SELECT COUNT(*) FROM notifications WHERE user_id = ? AND read = FALSE`, userID).Scan(&n)
	return n
}

func (d *DB) MarkRead(ctx context.Context, userID, id int64) error {
	var err error
	if id == 0 {
		_, err = d.Exec(ctx, `UPDATE notifications SET read = TRUE WHERE user_id = ?`, userID)
	} else {
		_, err = d.Exec(ctx, `UPDATE notifications SET read = TRUE WHERE user_id = ? AND id = ?`, userID, id)
	}
	return err
}

func (d *DB) MarkIssueRead(ctx context.Context, userID, issueID int64) {
	d.Exec(ctx, `UPDATE notifications SET read = TRUE WHERE user_id = ? AND issue_id = ? AND read = FALSE`, userID, issueID)
}

type SavedSearch struct {
	ID        int64  `json:"id"`
	UserID    int64  `json:"-"`
	Name      string `json:"name"`
	Query     string `json:"query"`
	Public    bool   `json:"public"`
	CreatedAt int64  `json:"created_at"`
	Owner     string `json:"owner,omitempty"`
}

func (d *DB) CreateSavedSearch(ctx context.Context, s *SavedSearch) error {
	s.CreatedAt = now()
	id, err := insertID(ctx, d, `INSERT INTO saved_searches (user_id, name, query, public, created_at) VALUES (?, ?, ?, ?, ?)`,
		s.UserID, s.Name, s.Query, s.Public, s.CreatedAt)
	s.ID = id
	return err
}

func (d *DB) SavedSearches(ctx context.Context, userID int64) ([]*SavedSearch, error) {
	rows, err := d.Query(ctx, `SELECT s.id, s.user_id, s.name, s.query, s.public, s.created_at, u.username FROM saved_searches s
		JOIN users u ON u.id = s.user_id WHERE s.user_id = ? OR s.public = TRUE ORDER BY s.public DESC, s.name`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*SavedSearch
	for rows.Next() {
		s := &SavedSearch{}
		if err := rows.Scan(&s.ID, &s.UserID, &s.Name, &s.Query, &s.Public, &s.CreatedAt, &s.Owner); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func (d *DB) DeleteSavedSearch(ctx context.Context, id, userID int64, anyUser bool) error {
	if anyUser {
		_, err := d.Exec(ctx, `DELETE FROM saved_searches WHERE id = ?`, id)
		return err
	}
	_, err := d.Exec(ctx, `DELETE FROM saved_searches WHERE id = ? AND user_id = ?`, id, userID)
	return err
}

func (d *DB) AutomationRan(ctx context.Context, automation string, issueID, updatedAt int64) bool {
	var n int
	d.QueryRow(ctx, `SELECT COUNT(*) FROM automation_runs WHERE automation = ? AND issue_id = ? AND issue_updated_at >= ?`,
		automation, issueID, updatedAt).Scan(&n)
	return n > 0
}

func (d *DB) RecordAutomation(ctx context.Context, automation string, issueID, updatedAt int64) {
	d.Exec(ctx, `INSERT INTO automation_runs (automation, issue_id, ran_at, issue_updated_at) VALUES (?, ?, ?, ?)
		ON CONFLICT (automation, issue_id) DO UPDATE SET ran_at = excluded.ran_at, issue_updated_at = excluded.issue_updated_at`,
		automation, issueID, now(), updatedAt)
}
