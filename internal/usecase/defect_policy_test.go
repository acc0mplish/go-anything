package usecase

import (
	"strings"
	"testing"
)

func TestParseDefectPolicyEmptyIsDefect(t *testing.T) {
	got, err := ParseDefectPolicy("")
	if err != nil {
		t.Fatalf("the empty policy must parse, got error: %v", err)
	}
	if got != PolicyDefect {
		t.Fatalf("the empty policy must default to %q, got %q", PolicyDefect, got)
	}
}

func TestParseDefectPolicyAcceptsBothNames(t *testing.T) {
	cases := map[string]DefectPolicy{
		"defect":   PolicyDefect,
		"expected": PolicyExpected,
	}
	for name, want := range cases {
		got, err := ParseDefectPolicy(name)
		if err != nil {
			t.Fatalf("%q must parse, got error: %v", name, err)
		}
		if got != want {
			t.Fatalf("%q must parse to %q, got %q", name, want, got)
		}
	}
}

func TestParseDefectPolicyRejectsUnknownListingValidNames(t *testing.T) {
	for _, bad := range []string{"crash", "Expected", "DEFECT", " expected"} {
		got, err := ParseDefectPolicy(bad)
		if err == nil {
			t.Fatalf("%q must be rejected, got policy %q", bad, got)
		}
		msg := err.Error()
		if !strings.Contains(msg, `"defect"`) || !strings.Contains(msg, `"expected"`) {
			t.Fatalf("the error for %q must list both valid names, got: %s", bad, msg)
		}
	}
}

func TestRecordsCrashOnlyForExpected(t *testing.T) {
	if PolicyDefect.RecordsCrash() {
		t.Fatal("the defect policy must not record a crash")
	}
	if !PolicyExpected.RecordsCrash() {
		t.Fatal("the expected policy must record a crash")
	}
	if DefectPolicy("bogus").RecordsCrash() {
		t.Fatal("an unrecognised policy must not record a crash")
	}
}
