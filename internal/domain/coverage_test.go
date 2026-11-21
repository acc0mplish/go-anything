package domain

import (
	"math"
	"strings"
	"testing"
)

func TestParseCoverProfile(t *testing.T) {
	in := strings.Join([]string{
		"mode: set",
		"x/a.go:3.1,4.2 2 1",
		"x/a.go:6.1,7.2 3 0",
		"x/b.go:2.1,2.9 1 5",
	}, "\n")
	c, err := ParseCoverProfile(strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	if c.Statements != 6 || c.Covered != 3 {
		t.Fatalf("want 3/6 covered, got %+v", c)
	}
	if math.Abs(c.Percent-50.0) > 1e-9 {
		t.Fatalf("want 50%%, got %v", c.Percent)
	}
}

func TestParseCoverProfileRejects(t *testing.T) {
	cases := map[string]string{
		"no mode line": "x/a.go:1.1,2.2 1 1",
		"empty":        "",
		"malformed":    "mode: set\nbroken",
	}
	for name, in := range cases {
		if _, err := ParseCoverProfile(strings.NewReader(in)); err == nil {
			t.Fatalf("%s: want error", name)
		}
	}
}
