package dbsnap

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/go-anything/go-anything/internal/domain"
)

// makeDB creates a small SQLite file with one table, using the same driver the adapter uses.
func makeDB(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "app.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	stmts := []string{
		"CREATE TABLE orders (id INTEGER PRIMARY KEY, status TEXT)",
		"INSERT INTO orders (id, status) VALUES (1, 'new'), (2, 'paid')",
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

func TestSnapshotReadsRows(t *testing.T) {
	s, err := Open(makeDB(t))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	tbl, err := s.Snapshot("orders")
	if err != nil {
		t.Fatal(err)
	}
	if len(tbl.Rows) != 2 || tbl.Rows[0]["status"] != "new" {
		t.Fatalf("rows not read: %+v", tbl.Rows)
	}
}

func TestOpenIsReadOnly(t *testing.T) {
	path := makeDB(t)
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	// Any write through the snapshotter must fail: the tool must never change program data.
	if _, err := s.db.Exec("INSERT INTO orders (id, status) VALUES (9, 'x')"); err == nil {
		t.Fatal("a write through the read-only handle must be refused")
	}
}

func TestRejectsUnsafeTableNames(t *testing.T) {
	s, err := Open(makeDB(t))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, name := range []string{"orders; DROP TABLE orders", "orders--", "a b", "", "1abc", "x.y"} {
		if _, err := s.Snapshot(name); err == nil {
			t.Fatalf("unsafe table name %q must be refused", name)
		}
	}
}

func TestOpenMissingFileFails(t *testing.T) {
	if _, err := Open(filepath.Join(t.TempDir(), "nope.db")); err == nil {
		t.Fatal("a missing database must be an error, not an empty one")
	}
}

func TestPrimaryKeyFound(t *testing.T) {
	s, err := Open(makeDB(t))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	pk, err := s.PrimaryKey("orders")
	if err != nil {
		t.Fatal(err)
	}
	if len(pk) != 1 || pk[0] != "id" {
		t.Fatalf("primary key wrong: %v", pk)
	}
}

func TestSnapshotDiffDetectsRealChange(t *testing.T) {
	path := makeDB(t)
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	before, _ := s.Snapshot("orders")

	// The program under test changes data between the two snapshots.
	w, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Exec("UPDATE orders SET status='shipped' WHERE id=1"); err != nil {
		t.Fatal(err)
	}
	w.Close()

	after, _ := s.Snapshot("orders")
	changes := domain.DiffTable("orders", before, after, []string{"id"})
	if len(changes) != 1 || changes[0].Op != "update" {
		t.Fatalf("the status change must be one update: %+v", changes)
	}
}
