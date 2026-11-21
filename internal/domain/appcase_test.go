package domain

import (
	"errors"
	"testing"
)

func TestAppRecordingValidate(t *testing.T) {
	ok := AppRecording{Cases: []AppCase{
		{Name: "a", Kind: "http", Method: "GET", Path: "/x"},
		{Name: "b", Kind: "cli", Argv: []string{"tool"}},
	}}
	if err := ok.Validate(); err != nil {
		t.Fatalf("valid recording rejected: %v", err)
	}
	cases := map[string]AppRecording{
		"empty":        {},
		"no name":      {Cases: []AppCase{{Kind: "cli", Argv: []string{"x"}}}},
		"duplicate":    {Cases: []AppCase{{Name: "a", Kind: "cli", Argv: []string{"x"}}, {Name: "a", Kind: "cli", Argv: []string{"x"}}}},
		"http no path": {Cases: []AppCase{{Name: "a", Kind: "http", Method: "GET"}}},
		"cli no argv":  {Cases: []AppCase{{Name: "a", Kind: "cli"}}},
		"bad kind":     {Cases: []AppCase{{Name: "a", Kind: "grpc"}}},
	}
	for name, r := range cases {
		if err := r.Validate(); err == nil {
			t.Fatalf("%s: want error", name)
		}
	}
	if err := (AppRecording{Cases: []AppCase{{Name: "a", Kind: "grpc"}}}).Validate(); !errors.Is(err, ErrInvalidAppCase) {
		t.Fatalf("bad kind must wrap ErrInvalidAppCase, got %v", err)
	}
}
