package domain

import (
	"sort"
	"strconv"
	"strings"
)

// Row is one table row, as column name to value text. Values are text so every database type
// compares the same way.
type Row map[string]string

// Table is a snapshot of one table: its rows, keyed for comparison.
type Table struct {
	Name string
	Rows []Row
}

// RowChange is one difference between the before and after snapshots of a table.
type RowChange struct {
	Table string `json:"table"`
	// Op is "insert", "delete" or "update".
	Op     string `json:"op"`
	Key    string `json:"key"`
	Before string `json:"before,omitempty"`
	After  string `json:"after,omitempty"`
}

// DiffTable compares two snapshots of the same table. Rows are matched by key: the primary key
// columns when the caller knows them, otherwise the whole row. The result is sorted, so a
// recording is deterministic.
func DiffTable(name string, before, after Table, keyCols []string) []RowChange {
	b := indexRows(before.Rows, keyCols)
	a := indexRows(after.Rows, keyCols)
	var out []RowChange
	for key, brow := range b {
		arow, present := a[key]
		switch {
		case !present:
			out = append(out, RowChange{Table: name, Op: "delete", Key: key, Before: formatRow(brow)})
		case formatRow(brow) != formatRow(arow):
			out = append(out, RowChange{Table: name, Op: "update", Key: key, Before: formatRow(brow), After: formatRow(arow)})
		}
	}
	for key, arow := range a {
		if _, present := b[key]; !present {
			out = append(out, RowChange{Table: name, Op: "insert", Key: key, After: formatRow(arow)})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Op != out[j].Op {
			return out[i].Op < out[j].Op
		}
		return out[i].Key < out[j].Key
	})
	return out
}

// indexRows maps each row to its key. A key that appears twice is kept as a composite of both
// rows, so duplicates are not silently dropped.
func indexRows(rows []Row, keyCols []string) map[string]Row {
	out := map[string]Row{}
	for i, r := range rows {
		key := rowKey(r, keyCols)
		if _, dup := out[key]; dup {
			key = key + "#" + formatRow(r) + "#" + strconv.Itoa(i)
		}
		out[key] = r
	}
	return out
}

func rowKey(r Row, keyCols []string) string {
	if len(keyCols) == 0 {
		return formatRow(r)
	}
	parts := make([]string, 0, len(keyCols))
	for _, c := range keyCols {
		parts = append(parts, c+"="+r[c])
	}
	return strings.Join(parts, ",")
}

// formatRow renders a row with sorted column names, so two equal rows always print the same.
func formatRow(r Row) string {
	cols := make([]string, 0, len(r))
	for c := range r {
		cols = append(cols, c)
	}
	sort.Strings(cols)
	parts := make([]string, 0, len(cols))
	for _, c := range cols {
		parts = append(parts, c+"="+r[c])
	}
	return strings.Join(parts, " ")
}

// WithoutColumns drops the named columns from a formatted row ("a=1 b=2" becomes "b=2" without
// "a"). It is used to describe an insert without its generated key.
func WithoutColumns(formatted string, cols []string) string {
	if len(cols) == 0 {
		return formatted
	}
	drop := map[string]bool{}
	for _, c := range cols {
		drop[c] = true
	}
	var kept []string
	for _, field := range strings.Fields(formatted) {
		name, _, _ := strings.Cut(field, "=")
		if !drop[name] {
			kept = append(kept, field)
		}
	}
	return strings.Join(kept, " ")
}
