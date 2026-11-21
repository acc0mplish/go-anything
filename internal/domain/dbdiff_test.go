package domain

import "testing"

func TestDiffTableInsertDeleteUpdate(t *testing.T) {
	before := Table{Name: "orders", Rows: []Row{
		{"id": "1", "status": "new"},
		{"id": "2", "status": "paid"},
	}}
	after := Table{Name: "orders", Rows: []Row{
		{"id": "1", "status": "shipped"}, // update
		{"id": "3", "status": "new"},     // insert
		// id 2 removed                    // delete
	}}
	got := DiffTable("orders", before, after, []string{"id"})
	ops := map[string]int{}
	for _, c := range got {
		ops[c.Op]++
	}
	if ops["insert"] != 1 || ops["delete"] != 1 || ops["update"] != 1 || len(got) != 3 {
		t.Fatalf("want one insert, delete and update: %+v", got)
	}
}

func TestDiffTableNoChangeIsEmpty(t *testing.T) {
	snap := Table{Rows: []Row{{"id": "1", "v": "a"}}}
	if got := DiffTable("t", snap, snap, []string{"id"}); len(got) != 0 {
		t.Fatalf("identical snapshots must have no changes: %+v", got)
	}
}

func TestDiffTableWithoutKeyUsesWholeRow(t *testing.T) {
	before := Table{Rows: []Row{{"a": "1"}}}
	after := Table{Rows: []Row{{"a": "1"}, {"a": "2"}}}
	got := DiffTable("t", before, after, nil)
	if len(got) != 1 || got[0].Op != "insert" {
		t.Fatalf("with no key the new row must be an insert: %+v", got)
	}
}

func TestDiffTableDuplicateKeysAreNotDropped(t *testing.T) {
	// two rows share a key; both must survive the comparison, not collapse into one.
	before := Table{Rows: []Row{{"k": "x", "v": "1"}, {"k": "x", "v": "2"}}}
	after := Table{Rows: []Row{{"k": "x", "v": "1"}}}
	got := DiffTable("t", before, after, []string{"k"})
	if len(got) == 0 {
		t.Fatal("removing one of two duplicate-key rows must be a visible change")
	}
}

func TestDiffTableIsDeterministic(t *testing.T) {
	before := Table{Rows: []Row{{"id": "9"}, {"id": "1"}, {"id": "5"}}}
	after := Table{}
	a := DiffTable("t", before, after, []string{"id"})
	b := DiffTable("t", before, after, []string{"id"})
	if len(a) != len(b) {
		t.Fatal("length differs between runs")
	}
	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("order differs at %d: %+v vs %+v", i, a[i], b[i])
		}
	}
}
