package store

import (
	"database/sql"
	"fmt"
	"math/rand"
	"net/url"
	"strings"
	"testing"
)

func TestSchemaDSN(t testing.TB, dsn string) string {
	t.Helper()
	schema := fmt.Sprintf("buggy_test_%d", rand.Int63())
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("CREATE SCHEMA " + schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Exec("DROP SCHEMA " + schema + " CASCADE"); db.Close() })
	sep := "?"
	if strings.Contains(dsn, "?") {
		sep = "&"
	}
	return dsn + sep + "search_path=" + url.QueryEscape(schema)
}
