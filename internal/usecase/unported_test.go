package usecase

import (
	"testing"

	"github.com/go-anything/go-anything/internal/domain"
)

func TestUnportedUnitsRanksAndFilters(t *testing.T) {
	units := []Unit{
		{ImportPath: "m/small", Lines: 10, Exported: 1},
		{ImportPath: "m/big", Lines: 500, Exported: 3, Recorded: true},
		{ImportPath: "m/large", Lines: 300, Exported: 9},
		{ImportPath: "m/tie-b", Lines: 50, Exported: 2},
		{ImportPath: "m/tie-a", Lines: 50, Exported: 2},
	}
	got := UnportedUnits(units)
	for _, u := range got {
		if u.ImportPath == "m/big" {
			t.Fatal("recorded unit must not be listed as unported")
		}
	}
	want := []string{"m/large", "m/tie-a", "m/tie-b", "m/small"}
	if len(got) != len(want) {
		t.Fatalf("want %d unported, got %d", len(want), len(got))
	}
	for i, w := range want {
		if got[i].ImportPath != w {
			t.Fatalf("position %d: want %s, got %s", i, w, got[i].ImportPath)
		}
	}
}

func TestMarkRecordedMatchesByImportPath(t *testing.T) {
	units := []Unit{{ImportPath: "m/a"}, {ImportPath: "m/b"}}
	recs := []domain.Recording{{Subject: "m/a"}, {Subject: "go run foo.go"}}
	got := MarkRecorded(units, recs)
	if !got[0].Recorded || got[1].Recorded {
		t.Fatalf("only m/a is covered: %+v", got)
	}
}
