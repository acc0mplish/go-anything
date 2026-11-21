package dbsnap

import (
	"database/sql"
	"testing"

	"github.com/go-anything/go-anything/internal/domain"
)

func TestWatcherReportsOnlyChangesSinceLastDrain(t *testing.T) {
	path := makeDB(t)
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	w, err := NewWatcher(s, []string{"orders"})
	if err != nil {
		t.Fatal(err)
	}
	w2, _ := sql.Open("sqlite", path)
	defer w2.Close()

	if _, err := w2.Exec("INSERT INTO orders (id, status) VALUES (3, 'new')"); err != nil {
		t.Fatal(err)
	}
	first := w.Drain()
	if len(first) != 1 || first[0].Kind != domain.EffectDB || first[0].Op != "insert" {
		t.Fatalf("the insert must be the one effect: %+v", first)
	}
	if second := w.Drain(); len(second) != 0 {
		t.Fatalf("a second drain with no new change must be empty: %+v", second)
	}
}

func TestWatcherSurfacesFailureAsEffect(t *testing.T) {
	path := makeDB(t)
	s, _ := Open(path)
	w, err := NewWatcher(s, []string{"orders"})
	if err != nil {
		t.Fatal(err)
	}
	// Close the database under the watcher: the next read must become a visible effect, not silence.
	s.Close()
	got := w.Drain()
	if len(got) != 1 || got[0].Op != "error" {
		t.Fatalf("a failed read must surface as an error effect: %+v", got)
	}
}

func TestWatcherRejectsUnknownTable(t *testing.T) {
	s, _ := Open(makeDB(t))
	defer s.Close()
	if _, err := NewWatcher(s, []string{"missing_table"}); err == nil {
		t.Fatal("watching a table that does not exist must fail up front")
	}
}
