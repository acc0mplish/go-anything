package dbsnap

import (
	"fmt"

	"github.com/go-anything/go-anything/internal/domain"
)

// Watcher turns row changes in chosen tables into effects. Mark takes a snapshot; Drain compares
// the current state with the last mark and returns what changed. It satisfies the effect source
// the recording and replay use, so DB changes are compared exactly like webhook calls.
type Watcher struct {
	s      *Snapshotter
	tables []string
	keys   map[string][]string
	base   map[string]domain.Table
}

// NewWatcher watches the named tables. Each table's primary key is read once, up front.
func NewWatcher(s *Snapshotter, tables []string) (*Watcher, error) {
	w := &Watcher{s: s, tables: tables, keys: map[string][]string{}, base: map[string]domain.Table{}}
	for _, t := range tables {
		pk, err := s.PrimaryKey(t)
		if err != nil {
			return nil, err
		}
		w.keys[t] = pk
	}
	return w, w.mark()
}

func (w *Watcher) mark() error {
	for _, t := range w.tables {
		snap, err := w.s.Snapshot(t)
		if err != nil {
			return err
		}
		w.base[t] = snap
	}
	return nil
}

// Drain returns the row changes since the last Drain (or since NewWatcher), then re-marks, so
// each case sees only its own changes. A failed read is returned as an error, never as "no change".
func (w *Watcher) Drain() []domain.Effect {
	var out []domain.Effect
	for _, t := range w.tables {
		after, err := w.s.Snapshot(t)
		if err != nil {
			// The Drain interface has no error return. Surface the failure as an effect so it
			// shows in the comparison instead of looking like "nothing changed".
			out = append(out, domain.Effect{Kind: domain.EffectDB, Table: t, Op: "error", Key: "snapshot", Body: err.Error()})
			continue
		}
		for _, c := range domain.DiffTable(t, w.base[t], after, w.keys[t]) {
			out = append(out, domain.Effect{Kind: domain.EffectDB, Table: c.Table, Op: c.Op, Key: c.Key, Body: changeBody(c, w.keys[t])})
		}
		w.base[t] = after
	}
	return out
}

// changeBody describes a change. For an insert the primary key columns are left out: they are
// usually generated, so the same behavior produces different values on each run.
func changeBody(c domain.RowChange, pk []string) string {
	switch c.Op {
	case "update":
		return fmt.Sprintf("%s -> %s", c.Before, c.After)
	case "insert":
		return domain.WithoutColumns(c.After, pk)
	case "delete":
		return c.Before
	}
	return ""
}
