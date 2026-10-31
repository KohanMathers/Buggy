package store

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"unicode"
)

type Issue struct {
	ID            int64  `json:"-"`
	ProjectID     int64  `json:"-"`
	Number        int64  `json:"number"`
	Key           string `json:"key"`
	Title         string `json:"title"`
	Description   string `json:"description"`
	State         string `json:"state"`
	Resolution    string `json:"resolution,omitempty"`
	ReporterID    int64  `json:"-"`
	AssigneeID    int64  `json:"-"`
	Private       bool   `json:"private"`
	Moderation    string `json:"moderation,omitempty"`
	Votes         int64  `json:"votes"`
	Watchers      int64  `json:"watchers"`
	CommentCount  int64  `json:"comment_count"`
	Confirmations int64  `json:"confirmations"`
	Fingerprint   string `json:"fingerprint,omitempty"`
	DuplicateOf   int64  `json:"-"`
	CreatedAt     int64  `json:"created_at"`
	UpdatedAt     int64  `json:"updated_at"`
	ResolvedAt    int64  `json:"resolved_at,omitempty"`
	ActivityAt    int64  `json:"activity_at"`

	Labels      []string            `json:"labels"`
	Fields      map[string][]string `json:"fields"`
	Affects     []*Version          `json:"affects_versions"`
	FixVersions []*Version          `json:"fix_versions"`
	Reporter    *User               `json:"reporter,omitempty"`
	Assignee    *User               `json:"assignee,omitempty"`
}

func (i *Issue) ProjectKey() string {
	k, _, _ := strings.Cut(i.Key, "-")
	return k
}

func (i *Issue) Field(id string) string {
	if v := i.Fields[id]; len(v) > 0 {
		return v[0]
	}
	return ""
}

func (i *Issue) HasLabel(l string) bool {
	for _, x := range i.Labels {
		if x == l {
			return true
		}
	}
	return false
}

const issueCols = `i.id, i.project_id, i.number, i.key, i.title, i.description, i.state, i.resolution, i.reporter_id,
i.assignee_id, i.private, i.moderation, i.votes, i.watchers, i.comment_count, i.confirmations, i.fingerprint,
i.duplicate_of, i.created_at, i.updated_at, i.resolved_at, i.activity_at`

func scanIssue(s scanner) (*Issue, error) {
	i := &Issue{}
	err := s.Scan(&i.ID, &i.ProjectID, &i.Number, &i.Key, &i.Title, &i.Description, &i.State, &i.Resolution,
		&i.ReporterID, &i.AssigneeID, &i.Private, &i.Moderation, &i.Votes, &i.Watchers, &i.CommentCount,
		&i.Confirmations, &i.Fingerprint, &i.DuplicateOf, &i.CreatedAt, &i.UpdatedAt, &i.ResolvedAt, &i.ActivityAt)
	return i, err
}

func scanIssues(rows *sql.Rows) ([]*Issue, error) {
	defer rows.Close()
	var out []*Issue
	for rows.Next() {
		i, err := scanIssue(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, i)
	}
	return out, rows.Err()
}

func (d *DB) CreateIssue(ctx context.Context, q Q, iss *Issue, projectKey string) error {
	t := now()
	if iss.CreatedAt == 0 {
		iss.CreatedAt = t
	}
	if iss.UpdatedAt == 0 {
		iss.UpdatedAt = iss.CreatedAt
	}
	if iss.ActivityAt == 0 {
		iss.ActivityAt = iss.UpdatedAt
	}
	if iss.Moderation == "" {
		iss.Moderation = "approved"
	}
	if iss.Number == 0 {
		if err := q.QueryRow(ctx, `UPDATE projects SET next_number = next_number + 1 WHERE id = ? RETURNING next_number - 1`,
			iss.ProjectID).Scan(&iss.Number); err != nil {
			return fmt.Errorf("allocate number: %w", err)
		}
	} else {
		if _, err := q.Exec(ctx, `UPDATE projects SET next_number = CASE WHEN next_number > ? THEN next_number ELSE ? END WHERE id = ?`,
			iss.Number, iss.Number+1, iss.ProjectID); err != nil {
			return err
		}
	}
	iss.Key = fmt.Sprintf("%s-%d", strings.ToUpper(projectKey), iss.Number)
	id, err := insertID(ctx, q, `INSERT INTO issues (project_id, number, key, title, description, state, resolution, reporter_id,
		assignee_id, private, moderation, votes, watchers, comment_count, confirmations, fingerprint, duplicate_of,
		created_at, updated_at, resolved_at, activity_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		iss.ProjectID, iss.Number, iss.Key, iss.Title, iss.Description, iss.State, iss.Resolution, iss.ReporterID,
		iss.AssigneeID, iss.Private, iss.Moderation, iss.Votes, iss.Watchers, iss.CommentCount, iss.Confirmations,
		iss.Fingerprint, iss.DuplicateOf, iss.CreatedAt, iss.UpdatedAt, iss.ResolvedAt, iss.ActivityAt)
	iss.ID = id
	return err
}

func (d *DB) SaveIssue(ctx context.Context, q Q, i *Issue) error {
	_, err := q.Exec(ctx, `UPDATE issues SET title = ?, description = ?, state = ?, resolution = ?, assignee_id = ?,
		private = ?, moderation = ?, fingerprint = ?, duplicate_of = ?, updated_at = ?, resolved_at = ?, activity_at = ?,
		votes = ?, watchers = ?, comment_count = ?, confirmations = ?
		WHERE id = ?`,
		i.Title, i.Description, i.State, i.Resolution, i.AssigneeID, i.Private, i.Moderation, i.Fingerprint,
		i.DuplicateOf, i.UpdatedAt, i.ResolvedAt, i.ActivityAt, i.Votes, i.Watchers, i.CommentCount, i.Confirmations, i.ID)
	return err
}

func (d *DB) IssueByKey(ctx context.Context, key string) (*Issue, error) {
	i, err := scanIssue(d.QueryRow(ctx, `SELECT `+issueCols+` FROM issues i WHERE i.key = ?`, strings.ToUpper(key)))
	return i, notFound(err)
}

func (d *DB) IssueByID(ctx context.Context, id int64) (*Issue, error) {
	i, err := scanIssue(d.QueryRow(ctx, `SELECT `+issueCols+` FROM issues i WHERE i.id = ?`, id))
	return i, notFound(err)
}

func (d *DB) IssuesByIDs(ctx context.Context, ids []int64) (map[int64]*Issue, error) {
	out := map[int64]*Issue{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := d.Query(ctx, `SELECT `+issueCols+` FROM issues i WHERE i.id IN (`+placeholders(len(ids))+`)`, int64Args(ids)...)
	if err != nil {
		return nil, err
	}
	list, err := scanIssues(rows)
	for _, i := range list {
		out[i.ID] = i
	}
	return out, err
}

func (d *DB) IssuesByFingerprint(ctx context.Context, projectID int64, fp string, exclude int64, limit int) ([]*Issue, error) {
	if fp == "" {
		return nil, nil
	}
	rows, err := d.Query(ctx, `SELECT `+issueCols+` FROM issues i WHERE i.project_id = ? AND i.fingerprint = ? AND i.id <> ?
		AND i.moderation = 'approved' ORDER BY i.duplicate_of = 0 DESC, i.votes DESC LIMIT ?`, projectID, fp, exclude, limit)
	if err != nil {
		return nil, err
	}
	return scanIssues(rows)
}

func (d *DB) DeleteIssue(ctx context.Context, id int64) error {
	return d.InTx(ctx, func(tx *Tx) error {
		if d.driver == "sqlite" {
			if _, err := tx.Exec(ctx, `DELETE FROM issue_fts WHERE rowid = ?`, id); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE issues SET duplicate_of = 0 WHERE duplicate_of = ?`, id); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `DELETE FROM issues WHERE id = ?`, id)
		return err
	})
}

func (d *DB) SetLabels(ctx context.Context, q Q, issueID int64, labels []string) error {
	if _, err := q.Exec(ctx, `DELETE FROM issue_labels WHERE issue_id = ?`, issueID); err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, l := range labels {
		if l == "" || seen[l] {
			continue
		}
		seen[l] = true
		if _, err := q.Exec(ctx, `INSERT INTO issue_labels (issue_id, label) VALUES (?, ?)`, issueID, l); err != nil {
			return err
		}
	}
	return nil
}

func (d *DB) SetIssueVersions(ctx context.Context, q Q, issueID int64, kind string, versionIDs []int64) error {
	if _, err := q.Exec(ctx, `DELETE FROM issue_versions WHERE issue_id = ? AND kind = ?`, issueID, kind); err != nil {
		return err
	}
	seen := map[int64]bool{}
	for _, v := range versionIDs {
		if seen[v] {
			continue
		}
		seen[v] = true
		if _, err := q.Exec(ctx, `INSERT INTO issue_versions (issue_id, version_id, kind) VALUES (?, ?, ?)`, issueID, v, kind); err != nil {
			return err
		}
	}
	return nil
}

func (d *DB) AddIssueVersion(ctx context.Context, q Q, issueID, versionID int64, kind string) error {
	_, err := q.Exec(ctx, `INSERT INTO issue_versions (issue_id, version_id, kind) VALUES (?, ?, ?) ON CONFLICT DO NOTHING`, issueID, versionID, kind)
	return err
}

type FieldValue struct {
	Field string
	Value string
	Num   *float64
}

func (d *DB) SetFieldValues(ctx context.Context, q Q, issueID int64, fields []string, values []FieldValue) error {
	if len(fields) > 0 {
		args := append([]any{issueID}, toAny(fields)...)
		if _, err := q.Exec(ctx, `DELETE FROM field_values WHERE issue_id = ? AND field IN (`+placeholders(len(fields))+`)`, args...); err != nil {
			return err
		}
	}
	for _, v := range values {
		if v.Value == "" {
			continue
		}
		if _, err := q.Exec(ctx, `INSERT INTO field_values (issue_id, field, value, num) VALUES (?, ?, ?, ?)`, issueID, v.Field, v.Value, v.Num); err != nil {
			return err
		}
	}
	return nil
}

func toAny(s []string) []any {
	out := make([]any, len(s))
	for i, v := range s {
		out[i] = v
	}
	return out
}

func (d *DB) LoadRelations(ctx context.Context, issues []*Issue) error {
	if len(issues) == 0 {
		return nil
	}
	byID := map[int64]*Issue{}
	ids := make([]int64, 0, len(issues))
	var userIDs []int64
	for _, i := range issues {
		byID[i.ID] = i
		ids = append(ids, i.ID)
		i.Fields = map[string][]string{}
		i.Labels = nil
		i.Affects, i.FixVersions = nil, nil
		userIDs = append(userIDs, i.ReporterID, i.AssigneeID)
	}
	in := placeholders(len(ids))
	args := int64Args(ids)

	rows, err := d.Query(ctx, `SELECT issue_id, label FROM issue_labels WHERE issue_id IN (`+in+`) ORDER BY label`, args...)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id int64
		var l string
		rows.Scan(&id, &l)
		byID[id].Labels = append(byID[id].Labels, l)
	}
	rows.Close()

	rows, err = d.Query(ctx, `SELECT issue_id, field, value FROM field_values WHERE issue_id IN (`+in+`)`, args...)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id int64
		var f, v string
		rows.Scan(&id, &f, &v)
		byID[id].Fields[f] = append(byID[id].Fields[f], v)
	}
	rows.Close()

	rows, err = d.Query(ctx, `SELECT iv.issue_id, iv.kind, `+prefixCols("v.", versionCols)+` FROM issue_versions iv
		JOIN versions v ON v.id = iv.version_id WHERE iv.issue_id IN (`+in+`) ORDER BY v.sort_order`, args...)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id int64
		var kind string
		v := &Version{}
		if err := rows.Scan(&id, &kind, &v.ID, &v.ProjectID, &v.Name, &v.SortOrder, &v.Released, &v.ReleaseDate, &v.Archived); err != nil {
			rows.Close()
			return err
		}
		if kind == "fixed" {
			byID[id].FixVersions = append(byID[id].FixVersions, v)
		} else {
			byID[id].Affects = append(byID[id].Affects, v)
		}
	}
	rows.Close()

	users, err := d.UsersByIDs(ctx, userIDs)
	if err != nil {
		return err
	}
	for _, i := range issues {
		i.Reporter = users[i.ReporterID]
		i.Assignee = users[i.AssigneeID]
	}
	return nil
}

type IssueQuery struct {
	Where   []string
	Args    []any
	Text    []string
	AnyText bool
	Order   string
	Limit   int
	Offset  int
}

func Terms(s string) []string {
	f := strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	var out []string
	for _, t := range f {
		if len([]rune(t)) >= 2 && len(out) < 32 {
			out = append(out, t)
		}
	}
	return out
}

func (d *DB) ftsClause(terms []string, or bool) (join, where, rank string, whereArgs, rankArgs []any) {
	if d.driver == "postgres" {
		parts := make([]string, len(terms))
		for i, t := range terms {
			parts[i] = t + ":*"
		}
		op := " & "
		if or {
			op = " | "
		}
		q := strings.Join(parts, op)
		return "JOIN issue_fts f ON f.issue_id = i.id",
			"f.tsv @@ to_tsquery('english', ?)",
			"ts_rank(f.tsv, to_tsquery('english', ?)) DESC",
			[]any{q}, []any{q}
	}
	parts := make([]string, len(terms))
	for i, t := range terms {
		parts[i] = `"` + t + `"*`
	}
	op := " AND "
	if or {
		op = " OR "
	}
	q := strings.Join(parts, op)
	return "JOIN issue_fts ON issue_fts.rowid = i.id", "issue_fts MATCH ?", "bm25(issue_fts, 10.0, 1.0)", []any{q}, nil
}

func (d *DB) SearchIssues(ctx context.Context, q IssueQuery) ([]*Issue, int, error) {
	join := ""
	where := append([]string{}, q.Where...)
	args := append([]any{}, q.Args...)
	order := q.Order
	var rankArgs []any
	if len(q.Text) > 0 {
		j, w, rank, wa, ra := d.ftsClause(q.Text, q.AnyText)
		join = j
		where = append(where, w)
		args = append(args, wa...)
		if order == "" {
			order = rank
			rankArgs = ra
		}
	}
	if order == "" {
		order = "i.updated_at DESC"
	}
	wsql := ""
	if len(where) > 0 {
		wsql = " WHERE " + strings.Join(where, " AND ")
	}
	var total int
	if err := d.QueryRow(ctx, `SELECT COUNT(*) FROM issues i `+join+wsql, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	if q.Limit <= 0 {
		q.Limit = 50
	}
	full := append(append(append([]any{}, args...), rankArgs...), q.Limit, q.Offset)
	rows, err := d.Query(ctx, `SELECT `+issueCols+` FROM issues i `+join+wsql+` ORDER BY `+order+`, i.id DESC LIMIT ? OFFSET ?`, full...)
	if err != nil {
		return nil, 0, err
	}
	list, err := scanIssues(rows)
	return list, total, err
}

func (d *DB) UpdateFTS(ctx context.Context, q Q, issueID int64) error {
	var title, desc, fields string
	if err := q.QueryRow(ctx, `SELECT title, description FROM issues WHERE id = ?`, issueID).Scan(&title, &desc); err != nil {
		return notFound(err)
	}
	var b strings.Builder
	b.WriteString(desc)
	rows, err := q.Query(ctx, `SELECT body FROM comments WHERE issue_id = ? AND deleted = FALSE AND restricted = FALSE AND moderation = 'approved' ORDER BY id`, issueID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var c string
		rows.Scan(&c)
		b.WriteString("\n")
		b.WriteString(c)
	}
	rows.Close()
	rows, err = q.Query(ctx, `SELECT value FROM field_values WHERE issue_id = ?`, issueID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var v string
		rows.Scan(&v)
		fields += "\n" + v
	}
	rows.Close()
	body := b.String() + fields
	if len(body) > 200000 {
		body = body[:200000]
	}
	if q.Dialect() == "postgres" {
		_, err = q.Exec(ctx, `INSERT INTO issue_fts (issue_id, title, body) VALUES (?, ?, ?)
			ON CONFLICT (issue_id) DO UPDATE SET title = excluded.title, body = excluded.body`, issueID, title, body)
		return err
	}
	if _, err := q.Exec(ctx, `DELETE FROM issue_fts WHERE rowid = ?`, issueID); err != nil {
		return err
	}
	_, err = q.Exec(ctx, `INSERT INTO issue_fts (rowid, title, body) VALUES (?, ?, ?)`, issueID, title, body)
	return err
}

func (d *DB) ReindexAll(ctx context.Context) (int, error) {
	rows, err := d.Query(ctx, `SELECT id FROM issues ORDER BY id`)
	if err != nil {
		return 0, err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		rows.Scan(&id)
		ids = append(ids, id)
	}
	rows.Close()
	if d.driver == "sqlite" {
		d.Exec(ctx, `DELETE FROM issue_fts`)
	}
	for _, id := range ids {
		if err := d.UpdateFTS(ctx, d, id); err != nil {
			return 0, err
		}
	}
	return len(ids), nil
}

func (d *DB) LabelCounts(ctx context.Context, projectID int64) (map[string]int, error) {
	q := `SELECT l.label, COUNT(*) FROM issue_labels l JOIN issues i ON i.id = l.issue_id WHERE i.private = FALSE AND i.moderation = 'approved'`
	var args []any
	if projectID != 0 {
		q += ` AND i.project_id = ?`
		args = append(args, projectID)
	}
	rows, err := d.Query(ctx, q+` GROUP BY l.label`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var l string
		var n int
		rows.Scan(&l, &n)
		out[l] = n
	}
	return out, rows.Err()
}

func SortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
