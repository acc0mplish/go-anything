package domain

import "sort"

// SameInsertContent reports whether two sets of row insertions describe the same content.
//
// An insert's key is often a generated number (an auto-increment id) that differs from run to run
// even when the program behaves identically. So inserts are compared by their content only:
// the same number of inserts, each with the same contents, in any order. Updates and deletes are
// not handled here; they name a specific existing row and are compared by key.
func SameInsertContent(recorded, replayed []RowChange) bool {
	a := insertContents(recorded)
	b := insertContents(replayed)
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// insertContents returns the content of each insert, sorted so order of arrival does not matter.
func insertContents(changes []RowChange) []string {
	var out []string
	for _, c := range changes {
		if c.Op == "insert" {
			out = append(out, c.Table+"|"+c.After)
		}
	}
	sort.Strings(out)
	return out
}
