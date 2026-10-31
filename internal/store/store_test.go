package store

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestSQLiteBasics(t *testing.T) { testBasics(t, "sqlite", filepath.Join(t.TempDir(), "t.db")) }

func TestPostgresBasics(t *testing.T) {
	dsn := os.Getenv("BUGGY_TEST_PG_DSN")
	if dsn == "" {
		t.Skip("set BUGGY_TEST_PG_DSN to run against Postgres")
	}
	testBasics(t, "postgres", TestSchemaDSN(t, dsn))
}

func testBasics(t *testing.T, driver, dsn string) {
	ctx := context.Background()
	db, err := Open(driver, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	u := &User{Username: "Alice", Role: "user", EmailNotifications: true}
	if err := db.CreateUser(ctx, u); err != nil {
		t.Fatal(err)
	}
	p, err := db.EnsureProject(ctx, "app")
	if err != nil {
		t.Fatal(err)
	}
	v, err := db.UpsertVersion(ctx, p.ID, Version{Name: "1.0", SortOrder: -1})
	if err != nil {
		t.Fatal(err)
	}
	for _, title := range []string{"Game crashes when opening map", "Sound stutters in menu"} {
		iss := &Issue{ProjectID: p.ID, Title: title, State: "open", ReporterID: u.ID}
		err := db.InTx(ctx, func(tx *Tx) error {
			if err := db.CreateIssue(ctx, tx, iss, "APP"); err != nil {
				return err
			}
			db.SetLabels(ctx, tx, iss.ID, []string{"crash"})
			db.SetIssueVersions(ctx, tx, iss.ID, "affects", []int64{v.ID})
			db.SetFieldValues(ctx, tx, iss.ID, []string{"platform"}, []FieldValue{{Field: "platform", Value: "windows"}})
			return db.UpdateFTS(ctx, tx, iss.ID)
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	list, total, err := db.SearchIssues(ctx, IssueQuery{Text: Terms("crash map")})
	if err != nil || total != 1 || list[0].Key != "APP-1" {
		t.Fatalf("search: %v %d %+v", err, total, list)
	}
	list, _, err = db.SearchIssues(ctx, IssueQuery{Text: Terms("crash sound"), AnyText: true})
	if err != nil || len(list) != 2 {
		t.Fatalf("any search: %v %d", err, len(list))
	}
	db.LoadRelations(ctx, list)
	if list[0].Field("platform") != "windows" || len(list[0].Affects) != 1 || list[0].Reporter.Username != "Alice" {
		t.Fatalf("relations: %+v", list[0])
	}
	if ch, _ := db.SetVote(ctx, db, list[0].ID, u.ID, true); !ch {
		t.Fatal("vote")
	}
}
