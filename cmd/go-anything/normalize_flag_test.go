package main

import (
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/go-anything/go-anything/internal/domain"
)

// TestSplitNormalizeSpecs pins the -normalize comma contract: a piece that names a built-in normalizer
// is its own spec, and any other piece is re-joined to the spec before it with its comma, so a custom
// regex that contains commas is read as one spec and the command lines that worked before the
// comma-separated list arrived keep working.
func TestSplitNormalizeSpecs(t *testing.T) {
	tests := []struct {
		name   string
		values []string
		want   []string
	}{
		{"a list of built-ins", []string{"counts,host_urls"}, []string{"counts", "host_urls"}},
		{"a custom regex with commas stays one spec", []string{`thousands=^[0-9]{1,3}(,[0-9]{3})*$`}, []string{`thousands=^[0-9]{1,3}(,[0-9]{3})*$`}},
		{"a single built-in", []string{"timestamps"}, []string{"timestamps"}},
		{"two pairs in one regex stay one spec", []string{"re=a,b=c"}, []string{"re=a,b=c"}},
		{"built-ins then a comma regex", []string{"timestamps,uuids,re=a,b"}, []string{"timestamps", "uuids", "re=a,b"}},
		{"spaces around a built-in name", []string{"counts, host_urls"}, []string{"counts", "host_urls"}},
		{"repeated flags stay separate values", []string{"counts", "uuids"}, []string{"counts", "uuids"}},
		{"a non-built-in first piece starts as-is", []string{"mine=a,b"}, []string{"mine=a,b"}},
	}
	for _, tc := range tests {
		if got := splitNormalizeSpecs(tc.values); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: splitNormalizeSpecs(%q) = %q, want %q", tc.name, tc.values, got, tc.want)
		}
	}
}

// TestAppReplayAcceptsCommaSpecs replays with the -normalize spellings that must keep working: a
// name=regex holding commas (the pre-list usage a plain split broke) and a comma-separated list of
// built-ins (the list usage itself). None may error.
func TestAppReplayAcceptsCommaSpecs(t *testing.T) {
	srv := goRewrite(t, `{"a":1}`)
	recPath := writeCases(t, domain.AppCase{Name: "c", Kind: "http", Method: "GET", Path: "/", Status: 200, ResponseBody: `{"a":1}`})
	for _, spec := range []string{`thousands=^[0-9]{1,3}(,[0-9]{3})*$`, "re=a,b=c", "counts,host_urls"} {
		err := run([]string{"app", "replay", recPath, "-base", srv.URL, "-normalize", spec}, strings.NewReader(""), io.Discard, io.Discard)
		if err != nil {
			t.Fatalf("app replay -normalize %s must be accepted: %v", spec, err)
		}
	}
}
