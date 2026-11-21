package main

import (
	"strings"
	"testing"
)

func TestSubjectFolderIsOneSafeSegment(t *testing.T) {
	cases := map[string]string{
		"github.com/x/y":   "github.com_x_y",
		"../../etc/passwd": "etc_passwd",
		"a/b\\c":           "a_b_c",
		"":                 "module",
		"..":               "module",
		"ok-name.v2":       "ok-name.v2",
	}
	for in, want := range cases {
		got := subjectFolder(in)
		if got != want {
			t.Fatalf("subjectFolder(%q)=%q want %q", in, got, want)
		}
		if strings.ContainsAny(got, "/\\") || got == "." || got == ".." {
			t.Fatalf("subjectFolder(%q)=%q is not a single safe segment", in, got)
		}
	}
}
