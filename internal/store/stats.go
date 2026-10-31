package store

import "context"

func (d *DB) Count(ctx context.Context, q string, args ...any) int {
	var n int
	d.QueryRow(ctx, q, args...).Scan(&n)
	return n
}

func (d *DB) StateCounts(ctx context.Context, projectID int64, includePrivate bool) (map[string]int, error) {
	q := `SELECT state, COUNT(*) FROM issues WHERE moderation = 'approved'`
	var args []any
	if projectID != 0 {
		q += ` AND project_id = ?`
		args = append(args, projectID)
	}
	if !includePrivate {
		q += ` AND private = FALSE`
	}
	rows, err := d.Query(ctx, q+` GROUP BY state`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var s string
		var n int
		rows.Scan(&s, &n)
		out[s] = n
	}
	return out, rows.Err()
}

type Scored struct {
	IssueID int64
	Score   int64
}

func (d *DB) Trending(ctx context.Context, since int64, projectID int64, limit int) ([]Scored, error) {
	q := `SELECT x.issue_id, SUM(x.w) AS score FROM (
		SELECT issue_id, 3 AS w FROM votes WHERE created_at > ?
		UNION ALL SELECT issue_id, 2 AS w FROM confirmations WHERE created_at > ?
		UNION ALL SELECT issue_id, 1 AS w FROM comments WHERE created_at > ? AND deleted = FALSE
	) x JOIN issues i ON i.id = x.issue_id
	WHERE i.private = FALSE AND i.moderation = 'approved' AND i.resolved_at = 0`
	args := []any{since, since, since}
	if projectID != 0 {
		q += ` AND i.project_id = ?`
		args = append(args, projectID)
	}
	q += ` GROUP BY x.issue_id ORDER BY score DESC LIMIT ?`
	args = append(args, limit)
	rows, err := d.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Scored
	for rows.Next() {
		var s Scored
		rows.Scan(&s.IssueID, &s.Score)
		out = append(out, s)
	}
	return out, rows.Err()
}

func (d *DB) PendingComments(ctx context.Context, limit int) ([]*Comment, error) {
	rows, err := d.Query(ctx, `SELECT `+commentCols+` FROM comments WHERE moderation = 'pending' AND deleted = FALSE ORDER BY created_at LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	return collectComments(ctx, d, rows)
}

func (d *DB) UserComments(ctx context.Context, userID int64, limit int) ([]*Comment, error) {
	rows, err := d.Query(ctx, `SELECT `+commentCols+` FROM comments WHERE author_id = ? AND deleted = FALSE AND moderation = 'approved' AND restricted = FALSE
		ORDER BY created_at DESC LIMIT ?`, userID, limit)
	if err != nil {
		return nil, err
	}
	return collectComments(ctx, d, rows)
}

func (d *DB) VersionCounts(ctx context.Context, projectID int64, kind string) (map[int64]int, error) {
	rows, err := d.Query(ctx, `SELECT iv.version_id, COUNT(*) FROM issue_versions iv JOIN issues i ON i.id = iv.issue_id
		WHERE i.project_id = ? AND iv.kind = ? AND i.private = FALSE AND i.moderation = 'approved' GROUP BY iv.version_id`, projectID, kind)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]int{}
	for rows.Next() {
		var id int64
		var n int
		rows.Scan(&id, &n)
		out[id] = n
	}
	return out, rows.Err()
}
