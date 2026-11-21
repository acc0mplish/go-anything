package domain

import (
	"strings"
	"testing"
)

func TestParseGoTestJSONPassAndFail(t *testing.T) {
	in := strings.Join([]string{
		`{"Action":"run","Package":"p","Test":"TestA"}`,
		`{"Action":"output","Package":"p","Test":"TestB","Output":"got 2 want 3\n"}`,
		`{"Action":"pass","Package":"p","Test":"TestA"}`,
		`{"Action":"fail","Package":"p","Test":"TestB"}`,
	}, "\n")
	cases, err := ParseGoTestJSON(strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	if len(cases) != 2 {
		t.Fatalf("want 2 cases, got %d", len(cases))
	}
	if cases[0].Output != "PASS" {
		t.Fatalf("pass must record PASS, got %+v", cases[0])
	}
	if cases[1].Error == "" || !strings.Contains(cases[1].Error, "got 2 want 3") {
		t.Fatalf("fail must carry its output as an error case: %+v", cases[1])
	}
}

func TestParseGoTestJSONRejectsGarbage(t *testing.T) {
	if _, err := ParseGoTestJSON(strings.NewReader("not json")); err == nil {
		t.Fatal("non-JSON input must be an error")
	}
}
