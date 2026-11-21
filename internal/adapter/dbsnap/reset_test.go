package dbsnap

import (
	"path/filepath"
	"testing"
)

func TestExecScriptRefusedOnReadOnly(t *testing.T) {
	s, err := Open(makeDB(t))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.ExecScript("DELETE FROM orders"); err == nil {
		t.Fatal("a read-only snapshotter must refuse scripts")
	}
	if tbl, _ := s.Snapshot("orders"); len(tbl.Rows) != 2 {
		t.Fatal("refused script must not have changed any row")
	}
}

func TestExecScriptResetsSQLiteWithSeveralStatements(t *testing.T) {
	path := makeDB(t)
	w, err := OpenSQLiteWritable(path)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	if err := w.ExecScript("DELETE FROM orders; INSERT INTO orders (id, status) VALUES (7, 'seed');"); err != nil {
		t.Fatal(err)
	}
	r, _ := Open(path)
	defer r.Close()
	tbl, _ := r.Snapshot("orders")
	if len(tbl.Rows) != 1 || tbl.Rows[0]["status"] != "seed" {
		t.Fatalf("reset script must leave only the seed row: %+v", tbl.Rows)
	}
}

func TestOpenSQLiteWritableNeverCreatesFile(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "nope.db")
	if _, err := OpenSQLiteWritable(missing); err == nil {
		t.Fatal("a missing database must be an error, not a new empty one")
	}
}

func TestDumpReadsEveryWatchedTable(t *testing.T) {
	path := makeDB(t)
	s, _ := Open(path)
	defer s.Close()
	got, err := s.Dump([]string{"orders"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got["orders"].Rows) != 2 {
		t.Fatalf("dump must hold the table's rows: %+v", got)
	}
}
