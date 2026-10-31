// Package backup creates portable, database-independent backups.
package backup

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/kohanmathers/buggy/internal/storage"
	"github.com/kohanmathers/buggy/internal/store"
)

type Stats struct {
	Rows  int
	Files int
}

type manifest struct {
	Format  int      `json:"format"`
	Driver  string   `json:"driver"`
	Schema  int      `json:"schema"`
	Created string   `json:"created"`
	Tables  []string `json:"tables"`
}

func addFile(tw *tar.Writer, name string, data []byte) error {
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(data)), ModTime: time.Now()}); err != nil {
		return err
	}
	_, err := tw.Write(data)
	return err
}

func Create(ctx context.Context, db *store.DB, files storage.Storage, configFiles []string, path string) (Stats, error) {
	var st Stats
	f, err := os.Create(path)
	if err != nil {
		return st, err
	}
	defer f.Close()
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)

	m := manifest{Format: 1, Driver: db.Dialect(), Schema: db.SchemaVersion(ctx), Created: time.Now().UTC().Format(time.RFC3339), Tables: store.Tables}
	mb, _ := json.MarshalIndent(m, "", "  ")
	if err := addFile(tw, "manifest.json", mb); err != nil {
		return st, err
	}
	for _, t := range store.Tables {
		var buf bytes.Buffer
		n, err := dumpTable(ctx, db, t, &buf)
		if err != nil {
			return st, fmt.Errorf("dump %s: %w", t, err)
		}
		st.Rows += n
		if err := addFile(tw, "db/"+t+".jsonl", buf.Bytes()); err != nil {
			return st, err
		}
	}
	for _, cf := range configFiles {
		if b, err := os.ReadFile(cf); err == nil {
			addFile(tw, "config/"+filepath.Base(cf), b)
		}
	}
	rows, err := db.Query(ctx, `SELECT storage_key FROM attachments WHERE deleted = FALSE`)
	if err != nil {
		return st, err
	}
	var keys []string
	for rows.Next() {
		var k string
		rows.Scan(&k)
		keys = append(keys, k)
	}
	rows.Close()
	for _, k := range keys {
		rc, err := files.Open(ctx, k)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: attachment %s missing: %v\n", k, err)
			continue
		}
		data, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			return st, err
		}
		if err := addFile(tw, "attachments/"+k, data); err != nil {
			return st, err
		}
		st.Files++
	}
	if err := tw.Close(); err != nil {
		return st, err
	}
	if err := gz.Close(); err != nil {
		return st, err
	}
	return st, f.Close()
}

func dumpTable(ctx context.Context, db *store.DB, table string, w io.Writer) (int, error) {
	rows, err := db.Query(ctx, "SELECT * FROM "+table)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	cols, _ := rows.Columns()
	types, _ := rows.ColumnTypes()
	enc := json.NewEncoder(w)
	n := 0
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return n, err
		}
		obj := make(map[string]any, len(cols))
		for i, c := range cols {
			v := vals[i]
			if b, ok := v.([]byte); ok {
				v = string(b)
			}
			if strings.Contains(strings.ToUpper(types[i].DatabaseTypeName()), "BOOL") {
				switch t := v.(type) {
				case int64:
					v = t != 0
				}
			}
			obj[c] = v
		}
		if err := enc.Encode(obj); err != nil {
			return n, err
		}
		n++
	}
	return n, rows.Err()
}

func Restore(ctx context.Context, db *store.DB, files storage.Storage, path string) (Stats, error) {
	var st Stats
	if n := db.Count(ctx, `SELECT COUNT(*) FROM issues`); n > 0 {
		return st, fmt.Errorf("target database already has %d issues; restore needs an empty database", n)
	}
	if n := db.Count(ctx, `SELECT COUNT(*) FROM users WHERE system = FALSE`); n > 0 {
		return st, fmt.Errorf("target database already has %d users; restore needs an empty database", n)
	}
	f, err := os.Open(path)
	if err != nil {
		return st, err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return st, err
	}
	tr := tar.NewReader(gz)
	tables := map[string][]byte{}
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return st, err
		}
		switch {
		case strings.HasPrefix(h.Name, "db/"):
			b, err := io.ReadAll(tr)
			if err != nil {
				return st, err
			}
			tables[strings.TrimSuffix(strings.TrimPrefix(h.Name, "db/"), ".jsonl")] = b
		case strings.HasPrefix(h.Name, "attachments/"):
			key := strings.TrimPrefix(h.Name, "attachments/")
			if strings.Contains(key, "..") {
				continue
			}
			if err := files.Put(ctx, key, tr, h.Size, ""); err != nil {
				return st, fmt.Errorf("restore attachment %s: %w", key, err)
			}
			st.Files++
		}
	}
	if len(tables) == 0 {
		return st, errors.New("archive contains no database tables")
	}
	err = db.InTx(ctx, func(tx *store.Tx) error {
		for i := len(store.Tables) - 1; i >= 0; i-- {
			if _, err := tx.Exec(ctx, "DELETE FROM "+store.Tables[i]); err != nil {
				return err
			}
		}
		for _, t := range store.Tables {
			data := tables[t]
			sc := bufio.NewScanner(bytes.NewReader(data))
			sc.Buffer(make([]byte, 1<<20), 64<<20)
			for sc.Scan() {
				dec := json.NewDecoder(bytes.NewReader(sc.Bytes()))
				dec.UseNumber()
				var row map[string]any
				if err := dec.Decode(&row); err != nil {
					return fmt.Errorf("%s: %w", t, err)
				}
				cols := make([]string, 0, len(row))
				args := make([]any, 0, len(row))
				for c, v := range row {
					if n, ok := v.(json.Number); ok {
						if iv, err := n.Int64(); err == nil {
							v = iv
						} else if fv, err := n.Float64(); err == nil && !math.IsNaN(fv) {
							v = fv
						}
					}
					cols = append(cols, c)
					args = append(args, v)
				}
				q := "INSERT INTO " + t + " (" + strings.Join(cols, ", ") + ") VALUES (" + strings.TrimSuffix(strings.Repeat("?, ", len(cols)), ", ") + ")"
				if _, err := tx.Exec(ctx, q, args...); err != nil {
					return fmt.Errorf("%s: %w", t, err)
				}
				st.Rows++
			}
			if err := sc.Err(); err != nil {
				return err
			}
		}
		if db.Dialect() == "postgres" {
			for _, t := range store.Tables {
				if hasID(t) {
					if _, err := tx.Exec(ctx, fmt.Sprintf(`SELECT setval(pg_get_serial_sequence('%s', 'id'), COALESCE((SELECT MAX(id) FROM %s), 0) + 1, false)`, t, t)); err != nil {
						return err
					}
				}
			}
		}
		return nil
	})
	if err != nil {
		return st, err
	}
	_, err = db.ReindexAll(ctx)
	return st, err
}

func hasID(t string) bool {
	switch t {
	case "kv", "identities", "sessions", "issue_labels", "issue_versions", "field_values", "votes", "watchers", "automation_runs":
		return false
	}
	return true
}
