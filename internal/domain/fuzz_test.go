package domain

import (
	"strings"
	"testing"
)

// Each fuzz target checks one property: the parser never panics on any input, and an error
// is returned instead of a silently wrong result.

func FuzzParseCoverProfile(f *testing.F) {
	f.Add("mode: set\nx/a.go:3.1,4.2 2 1\n")
	f.Add("")
	f.Add("mode: set\nbroken")
	f.Fuzz(func(t *testing.T, in string) {
		c, err := ParseCoverProfile(strings.NewReader(in))
		if err == nil && (c.Covered > c.Statements || c.Percent < 0 || c.Percent > 100+1e-9) {
			t.Fatalf("impossible coverage accepted: %+v", c)
		}
	})
}

func FuzzParseTop(f *testing.F) {
	f.Add("      flat  flat%   sum%        cum   cum%\n     2.70s 98.54% 98.91%      2.71s 98.91%  x.burn\n")
	f.Add("")
	f.Fuzz(func(t *testing.T, in string) {
		_, _ = ParseTop(strings.NewReader(in))
	})
}

func FuzzParseGoTestJSON(f *testing.F) {
	f.Add(`{"Action":"pass","Package":"p","Test":"TestA"}`)
	f.Add("not json")
	f.Fuzz(func(t *testing.T, in string) {
		_, _ = ParseGoTestJSON(strings.NewReader(in))
	})
}

func FuzzGoVersionAtLeast(f *testing.F) {
	f.Add("go1.27.4", "1.27.4")
	f.Add("", "")
	f.Fuzz(func(t *testing.T, installed, required string) {
		GoVersionAtLeast(installed, required)
	})
}

func FuzzScanUnsafe(f *testing.F) {
	f.Add("package p\nimport \"unsafe\"\nvar _ = unsafe.Sizeof(1)\n")
	f.Fuzz(func(t *testing.T, src string) {
		_ = ScanUnsafe("f.go", src)
	})
}

func FuzzScanUnimplemented(f *testing.F) {
	f.Add("package p\nfunc A() { panic(\"not implemented\") }\n")
	f.Fuzz(func(t *testing.T, src string) {
		_ = ScanUnimplemented("f.go", src)
	})
}

func FuzzPortPlan(f *testing.F) {
	f.Add("a", "b", "unsafe", 3)
	f.Fuzz(func(t *testing.T, p1, p2, imp string, n int) {
		a := Analysis{Module: "m", Packages: []Package{
			{ImportPath: p1, Imports: []string{p2, imp}, Lines: n, Tests: n % 3},
			{ImportPath: p2, Imports: []string{p1}},
		}}
		rows := PortPlan(a)
		if len(rows) != 2 {
			t.Fatalf("every package needs one row: %d", len(rows))
		}
		for _, r := range rows {
			if r.Readiness < 0 || r.Readiness > 100 || r.Wave < 0 {
				t.Fatalf("readiness or wave out of range: %+v", r)
			}
		}
	})
}
