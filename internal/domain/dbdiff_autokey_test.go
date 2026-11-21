package domain

import "testing"

// An auto-increment key changes from run to run. Two runs that insert the same content must
// compare equal even though the generated id differs.
func TestInsertWithDifferentGeneratedIDIsSameContent(t *testing.T) {
	recorded := []RowChange{{Table: "orders", Op: "insert", Key: "id=1", After: "status=new"}}
	replayed := []RowChange{{Table: "orders", Op: "insert", Key: "id=2", After: "status=new"}}
	if !SameInsertContent(recorded, replayed) {
		t.Fatal("an insert with the same content but a different generated key must match")
	}
}

func TestInsertWithDifferentContentDoesNotMatch(t *testing.T) {
	recorded := []RowChange{{Table: "orders", Op: "insert", Key: "id=1", After: "status=new"}}
	replayed := []RowChange{{Table: "orders", Op: "insert", Key: "id=3", After: "status=paid"}}
	if SameInsertContent(recorded, replayed) {
		t.Fatal("a different status must not match, whatever the id is")
	}
}

func TestDifferentNumberOfInsertsDoesNotMatch(t *testing.T) {
	one := []RowChange{{Op: "insert", After: "x"}}
	two := []RowChange{{Op: "insert", After: "x"}, {Op: "insert", After: "x"}}
	if SameInsertContent(one, two) {
		t.Fatal("a missing or extra insert must not match")
	}
}

func TestWithoutColumnsDropsGeneratedKey(t *testing.T) {
	got := WithoutColumns("id=7 status=new total=10", []string{"id"})
	if got != "status=new total=10" {
		t.Fatalf("key column must be dropped, got %q", got)
	}
	if WithoutColumns("a=1 b=2", nil) != "a=1 b=2" {
		t.Fatal("with no key columns the row must be unchanged")
	}
}
