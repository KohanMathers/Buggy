// Package store is buggy's persistence layer.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	_ "modernc.org/sqlite"
)

var ErrNotFound = errors.New("not found")

type Q interface {
	Exec(ctx context.Context, q string, args ...any) (sql.Result, error)
	Query(ctx context.Context, q string, args ...any) (*sql.Rows, error)
	QueryRow(ctx context.Context, q string, args ...any) *sql.Row
	Dialect() string
}

type DB struct {
	sql    *sql.DB
	driver string
}

func Open(driver, dsn string) (*DB, error) {
	var (
		db  *sql.DB
		err error
	)
	switch driver {
	case "sqlite":
		if dir := filepath.Dir(dsn); dir != "" {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return nil, err
			}
		}
		full := dsn
		if !strings.HasPrefix(full, "file:") {
			full = "file:" + full
		}
		sep := "?"
		if strings.Contains(full, "?") {
			sep = "&"
		}
		full += sep + "_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_pragma=synchronous(NORMAL)&_txlock=immediate"
		db, err = sql.Open("sqlite", full)
		if err == nil {
			db.SetMaxOpenConns(8)
		}
	case "postgres":
		db, err = sql.Open("pgx", dsn)
		if err == nil {
			db.SetMaxOpenConns(20)
			db.SetConnMaxIdleTime(5 * time.Minute)
		}
	default:
		return nil, fmt.Errorf("unsupported database driver %q", driver)
	}
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("connect %s: %w", driver, err)
	}
	d := &DB{sql: db, driver: driver}
	if err := d.migrate(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return d, nil
}

func (d *DB) Close() error                   { return d.sql.Close() }
func (d *DB) Dialect() string                { return d.driver }
func (d *DB) Raw() *sql.DB                   { return d.sql }
func (d *DB) Ping(ctx context.Context) error { return d.sql.PingContext(ctx) }

func rebind(driver, q string) string {
	if driver != "postgres" || !strings.Contains(q, "?") {
		return q
	}
	var b strings.Builder
	b.Grow(len(q) + 16)
	n := 0
	inStr := false
	for i := 0; i < len(q); i++ {
		c := q[i]
		if c == '\'' {
			inStr = !inStr
		}
		if c == '?' && !inStr {
			n++
			b.WriteByte('$')
			b.WriteString(strconv.Itoa(n))
			continue
		}
		b.WriteByte(c)
	}
	return b.String()
}

func (d *DB) Exec(ctx context.Context, q string, args ...any) (sql.Result, error) {
	return d.sql.ExecContext(ctx, rebind(d.driver, q), args...)
}
func (d *DB) Query(ctx context.Context, q string, args ...any) (*sql.Rows, error) {
	return d.sql.QueryContext(ctx, rebind(d.driver, q), args...)
}
func (d *DB) QueryRow(ctx context.Context, q string, args ...any) *sql.Row {
	return d.sql.QueryRowContext(ctx, rebind(d.driver, q), args...)
}

type Tx struct {
	tx     *sql.Tx
	driver string
}

func (t *Tx) Dialect() string { return t.driver }
func (t *Tx) Exec(ctx context.Context, q string, args ...any) (sql.Result, error) {
	return t.tx.ExecContext(ctx, rebind(t.driver, q), args...)
}
func (t *Tx) Query(ctx context.Context, q string, args ...any) (*sql.Rows, error) {
	return t.tx.QueryContext(ctx, rebind(t.driver, q), args...)
}
func (t *Tx) QueryRow(ctx context.Context, q string, args ...any) *sql.Row {
	return t.tx.QueryRowContext(ctx, rebind(t.driver, q), args...)
}

func (d *DB) InTx(ctx context.Context, fn func(tx *Tx) error) error {
	stx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	tx := &Tx{tx: stx, driver: d.driver}
	if err := fn(tx); err != nil {
		stx.Rollback()
		return err
	}
	return stx.Commit()
}

func insertID(ctx context.Context, q Q, query string, args ...any) (int64, error) {
	var id int64
	err := q.QueryRow(ctx, query+" RETURNING id", args...).Scan(&id)
	return id, err
}

func now() int64 { return time.Now().Unix() }

func placeholders(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.TrimSuffix(strings.Repeat("?, ", n), ", ")
}

func int64Args(ids []int64) []any {
	out := make([]any, len(ids))
	for i, v := range ids {
		out[i] = v
	}
	return out
}

func notFound(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

func (d *DB) GetKV(ctx context.Context, k string) (string, error) {
	var v string
	err := d.QueryRow(ctx, `SELECT v FROM kv WHERE k = ?`, k).Scan(&v)
	return v, notFound(err)
}

func (d *DB) SetKV(ctx context.Context, k, v string) error {
	_, err := d.Exec(ctx, `INSERT INTO kv (k, v) VALUES (?, ?) ON CONFLICT (k) DO UPDATE SET v = excluded.v`, k, v)
	return err
}
