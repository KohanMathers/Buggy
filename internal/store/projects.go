package store

import (
	"context"
	"strings"
)

type ProjectRow struct {
	ID         int64
	Key        string
	NextNumber int64
}

type Version struct {
	ID          int64  `json:"id"`
	ProjectID   int64  `json:"-"`
	Name        string `json:"name"`
	SortOrder   int64  `json:"sort_order"`
	Released    bool   `json:"released"`
	ReleaseDate string `json:"release_date,omitempty"`
	Archived    bool   `json:"archived"`
}

func (d *DB) EnsureProject(ctx context.Context, key string) (*ProjectRow, error) {
	key = strings.ToUpper(key)
	p := &ProjectRow{}
	err := d.QueryRow(ctx, `SELECT id, key, next_number FROM projects WHERE key = ?`, key).Scan(&p.ID, &p.Key, &p.NextNumber)
	if err == nil {
		return p, nil
	}
	if notFound(err) != ErrNotFound {
		return nil, err
	}
	id, err := insertID(ctx, d, `INSERT INTO projects (key, next_number, created_at) VALUES (?, 1, ?)`, key, now())
	if err != nil {
		return nil, err
	}
	return &ProjectRow{ID: id, Key: key, NextNumber: 1}, nil
}

func (d *DB) ProjectByKey(ctx context.Context, key string) (*ProjectRow, error) {
	p := &ProjectRow{}
	err := d.QueryRow(ctx, `SELECT id, key, next_number FROM projects WHERE key = ?`, strings.ToUpper(key)).Scan(&p.ID, &p.Key, &p.NextNumber)
	return p, notFound(err)
}

func (d *DB) ProjectKeysByID(ctx context.Context) (map[int64]string, error) {
	rows, err := d.Query(ctx, `SELECT id, key FROM projects`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]string{}
	for rows.Next() {
		var id int64
		var k string
		rows.Scan(&id, &k)
		out[id] = k
	}
	return out, rows.Err()
}

const versionCols = `id, project_id, name, sort_order, released, release_date, archived`

func scanVersion(s scanner) (*Version, error) {
	v := &Version{}
	err := s.Scan(&v.ID, &v.ProjectID, &v.Name, &v.SortOrder, &v.Released, &v.ReleaseDate, &v.Archived)
	return v, err
}

func (d *DB) Versions(ctx context.Context, projectID int64) ([]*Version, error) {
	rows, err := d.Query(ctx, `SELECT `+versionCols+` FROM versions WHERE project_id = ? ORDER BY sort_order, id`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Version
	for rows.Next() {
		v, err := scanVersion(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func (d *DB) VersionByName(ctx context.Context, projectID int64, name string) (*Version, error) {
	v, err := scanVersion(d.QueryRow(ctx, `SELECT `+versionCols+` FROM versions WHERE project_id = ? AND name = ?`, projectID, name))
	return v, notFound(err)
}

func (d *DB) VersionByID(ctx context.Context, id int64) (*Version, error) {
	v, err := scanVersion(d.QueryRow(ctx, `SELECT `+versionCols+` FROM versions WHERE id = ?`, id))
	return v, notFound(err)
}

func (d *DB) UpsertVersion(ctx context.Context, projectID int64, v Version) (*Version, error) {
	existing, err := d.VersionByName(ctx, projectID, v.Name)
	if err == nil {
		if v.SortOrder < 0 {
			v.SortOrder = existing.SortOrder
		}
		_, err = d.Exec(ctx, `UPDATE versions SET sort_order = ?, released = ?, release_date = ?, archived = ? WHERE id = ?`,
			v.SortOrder, v.Released, v.ReleaseDate, v.Archived, existing.ID)
		v.ID, v.ProjectID = existing.ID, projectID
		return &v, err
	}
	if err != ErrNotFound {
		return nil, err
	}
	if v.SortOrder < 0 {
		var max int64
		d.QueryRow(ctx, `SELECT COALESCE(MAX(sort_order), 0) FROM versions WHERE project_id = ?`, projectID).Scan(&max)
		v.SortOrder = max + 10
	}
	id, err := insertID(ctx, d, `INSERT INTO versions (project_id, name, sort_order, released, release_date, archived) VALUES (?, ?, ?, ?, ?, ?)`,
		projectID, v.Name, v.SortOrder, v.Released, v.ReleaseDate, v.Archived)
	v.ID, v.ProjectID = id, projectID
	return &v, err
}

func (d *DB) DeleteVersion(ctx context.Context, id int64) error {
	_, err := d.Exec(ctx, `DELETE FROM versions WHERE id = ?`, id)
	return err
}
