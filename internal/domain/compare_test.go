package domain

import (
	"errors"
	"math"
	"strings"
	"testing"
)

func TestEqualBody(t *testing.T) {
	exact := CompareOptions{}
	sem := CompareOptions{JSONBody: true}
	tol := func(t float64) CompareOptions { return CompareOptions{JSONBody: true, FloatTolerance: t} }
	tests := []struct {
		name      string
		want, got string
		opt       CompareOptions
		equal     bool
		whyHas    string // substring of why when not equal
	}{
		{"identical bytes", `{"a":1}`, `{"a":1}`, exact, true, ""},
		{"spacing and key order, exact", `{"id": 1, "total": 100}`, `{"total":100,"id":1}`, exact, false, "line 1"},
		{"spacing and key order, json", `{"id": 1, "total": 100}`, `{"total":100,"id":1}`, sem, true, ""},
		{"newlines are whitespace in json", "{\n  \"a\": [1, 2]\n}\n", `{"a":[1,2]}`, sem, true, ""},
		{"array order matters", `{"a":[1,2]}`, `{"a":[2,1]}`, sem, false, "$.a[0]: want 1 got 2"},
		{"array length", `[1,2,3]`, `[1,2]`, sem, false, "$: want 3 element(s) got 2"},
		{"1 vs 1.0 by decimal value", `{"n":1}`, `{"n":1.0}`, sem, true, ""},
		{"1 vs 1e0 by decimal value", `[1]`, `[1e0]`, sem, true, ""},
		{"big integers exact, no float64 rounding", `[9007199254740993]`, `[9007199254740992]`, sem, false, "$[0]: want 9007199254740993 got 9007199254740992"},
		{"number vs string", `{"n":1}`, `{"n":"1"}`, sem, false, `$.n: want number 1 got string "1"`},
		{"missing key", `{"a":1,"b":2}`, `{"a":1}`, sem, false, "$.b: missing in got"},
		{"extra key", `{"a":1}`, `{"a":1,"c":3}`, sem, false, "$.c: not in want, present in got"},
		{"nested path", `{"items":[{"price":1},{"price":2},{"price":1.5}]}`, `{"items":[{"price":1},{"price":2},{"price":1.50001}]}`, sem, false, "$.items[2].price: want 1.5 got 1.50001"},
		{"odd key quoted in path", `{"a b":1}`, `{"a b":2}`, sem, false, `$["a b"]: want 1 got 2`},
		{"0.1+0.2 within 1e-9", `{"x":0.3}`, `{"x":0.30000000000000004}`, tol(1e-9), true, ""},
		{"0.1+0.2 outside 1e-18", `{"x":0.3}`, `{"x":0.30000000000000004}`, tol(1e-18), false, "$.x: want 0.3 got 0.30000000000000004 (outside tolerance 1e-18)"},
		{"relative tolerance on large values", `[1000000]`, `[1000001]`, tol(1e-5), true, ""},
		{"absolute tolerance near zero", `[0]`, `[0.0000001]`, tol(1e-6), true, ""},
		{"tolerance does not touch strings", `["0.3"]`, `["0.30000000000000004"]`, tol(1e-3), false, "$[0]"},
		{"non-JSON falls back to exact, equal", "hello", "hello", sem, true, ""},
		{"non-JSON falls back to exact, differs", "hello", "hello ", sem, false, "compared exactly: want is not usable JSON"},
		{"one side JSON only", `{"a":1}`, `not json`, sem, false, "got is not usable JSON"},
		{"duplicate key falls back", `{"a":1,"a":2}`, `{"a":2}`, sem, false, "duplicate key at $.a"},
		{"duplicate key in got falls back", `{"a":2}`, `{"a": 1, "a": 2}`, sem, false, "duplicate key"},
		{"duplicate key, same bytes still equal", `{"a":1,"a":1}`, `{"a":1,"a":1}`, sem, true, ""},
		{"trailing data is not JSON", `{"a":1}`, `{"a":1}{"a":1}`, sem, false, "more than one JSON value"},
		{"empty bodies equal", "", "", sem, true, ""},
		{"invalid utf8 falls back", "\"\xff\"", "\"\xfe\"", sem, false, "invalid UTF-8"},
		{"NaN line is not a number", "NaN", "NaN ", CompareOptions{FloatTolerance: 1}, false, "line 1"},
		{"Infinity is not a number", "Infinity", "1e400", CompareOptions{FloatTolerance: 1}, false, "line 1"},
		{"numeric line within tolerance", "total\n0.3", "total\n0.30000000000000004", CompareOptions{FloatTolerance: 1e-9}, true, ""},
		{"numeric line outside tolerance", "total\n0.3", "total\n0.31", CompareOptions{FloatTolerance: 1e-9}, false, "line 2: want 0.3 got 0.31 (outside tolerance 1e-09)"},
		{"partly numeric line stays exact", "x=0.3", "x=0.30000000000000004", CompareOptions{FloatTolerance: 1}, false, "line 1"},
		{"line count differs", "a\nb", "a", exact, false, "want 2 line(s) got 1"},
		{"huge exponent compared as written", `[1e999999999]`, `[1e999999998]`, tol(1), false, "$[0]"},
		{"invalid tolerance does not loosen", "0.3", "0.4", CompareOptions{FloatTolerance: math.Inf(1)}, false, "line 1"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			eq, why := EqualBody(tc.want, tc.got, tc.opt)
			if eq != tc.equal {
				t.Fatalf("EqualBody(%q, %q) = %v (%s), want %v", tc.want, tc.got, eq, why, tc.equal)
			}
			if eq && why != "" {
				t.Fatalf("equal but why = %q", why)
			}
			if !eq && !strings.Contains(why, tc.whyHas) {
				t.Fatalf("why = %q, want it to contain %q", why, tc.whyHas)
			}
		})
	}
}

func TestEqualBodyOversizeFallsBackToExact(t *testing.T) {
	big := `{"a":"` + strings.Repeat("x", MaxJSONCompareBytes) + `"}`
	spaced := `{"a": "` + strings.Repeat("x", MaxJSONCompareBytes) + `"}`
	eq, why := EqualBody(big, spaced, CompareOptions{JSONBody: true})
	if eq || !strings.Contains(why, "not parsed as JSON") {
		t.Fatalf("oversize body: equal=%v why=%q, want exact fallback with a note", eq, why)
	}
	if eq, _ := EqualBody(big, big, CompareOptions{JSONBody: true}); !eq {
		t.Fatal("identical oversize bodies must still be equal")
	}
}

func TestEqualBodyDeepNestingFallsBack(t *testing.T) {
	deep := strings.Repeat("[", maxJSONDepth+1) + strings.Repeat("]", maxJSONDepth+1)
	eq, why := EqualBody(deep, deep+" ", CompareOptions{JSONBody: true})
	if eq || !strings.Contains(why, "nested deeper") {
		t.Fatalf("deep nesting: equal=%v why=%q", eq, why)
	}
}

func TestCompareOptionsValidate(t *testing.T) {
	for _, v := range []float64{0, 1e-9, 5} {
		if err := (CompareOptions{FloatTolerance: v}).Validate(); err != nil {
			t.Fatalf("%v: %v", v, err)
		}
	}
	for _, v := range []float64{-1e-9, math.NaN(), math.Inf(1), math.Inf(-1)} {
		if err := (CompareOptions{FloatTolerance: v}).Validate(); !errors.Is(err, ErrInvalidCompareOptions) {
			t.Fatalf("%v: err = %v, want ErrInvalidCompareOptions", v, err)
		}
	}
}

func TestCompareOptionsDescribe(t *testing.T) {
	if got := (CompareOptions{}).Describe(); got != "exact" {
		t.Fatalf("zero options = %q, want exact", got)
	}
	got := CompareOptions{JSONBody: true, FloatTolerance: 1e-9}.Describe()
	for _, want := range []string{"JSON semantic", "exact decimal value", "float tolerance 1e-09", "absolute or relative"} {
		if !strings.Contains(got, want) {
			t.Fatalf("Describe() = %q, missing %q", got, want)
		}
	}
}

func TestIsJSONNumber(t *testing.T) {
	for s, want := range map[string]bool{
		"0": true, "-0": true, "1.5": true, "1e10": true, "1E-3": true, "-12.5e+2": true,
		"": false, "-": false, "01": false, "+1": false, ".5": false, "1.": false, "1e": false,
		"NaN": false, "Inf": false, "Infinity": false, "-Infinity": false, "0x10": false, " 1": false, "1 ": false,
	} {
		if got := isJSONNumber(s); got != want {
			t.Errorf("isJSONNumber(%q) = %v, want %v", s, got, want)
		}
	}
}

func TestReportShowsComparison(t *testing.T) {
	r := Report{Subject: "s", Kind: "app-http", Total: 1, Matched: 1,
		Run: &ReportRun{Compare: CompareOptions{JSONBody: true, FloatTolerance: 1e-9}.Describe()}}
	r.Finalize()
	if md := r.Markdown(); !strings.Contains(md, "Compare: JSON semantic") || !strings.Contains(md, "exact decimal value") {
		t.Fatalf("markdown does not say which comparison was used:\n%s", md)
	}
	html, err := r.HTML()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(html), "Compare JSON semantic") || !strings.Contains(string(html), "float tolerance 1e-09") {
		t.Fatalf("html meta line does not say which comparison was used")
	}
	r.Run.Compare = CompareOptions{}.Describe()
	if md := r.Markdown(); !strings.Contains(md, "Compare: exact") {
		t.Fatalf("markdown does not say exact:\n%s", md)
	}
}

func TestEqualBodyNormalizesTimestampsAndUUIDs(t *testing.T) {
	want := `{"id": 7, "created": "2026-11-22T01:23:45Z", "ref": "01234567-89ab-cdef-0123-456789abcdef"}`
	got := `{"id":7,"created":"2027-01-01T00:00:00+09:00","ref":"ffffffff-ffff-ffff-ffff-ffffffffffff"}`
	opt := CompareOptions{JSONBody: true, Normalize: []string{"timestamps", "uuids"}}
	if eq, why := EqualBody(want, got, opt); !eq {
		t.Fatalf("a differing clock reading and UUID must compare as equal under the normalizers: %s", why)
	}
	noNorm := CompareOptions{JSONBody: true}
	if eq, _ := EqualBody(want, got, noNorm); eq {
		t.Fatal("without the normalizers the same answers must differ")
	}
}

func TestEqualBodyNormalizesTextAnswersToo(t *testing.T) {
	want := "saved at 2026-11-22 01:23:45\n"
	got := "saved at 2026-12-01 09:00:00\n"
	if eq, why := EqualBody(want, got, CompareOptions{Normalize: []string{"timestamps"}}); !eq {
		t.Fatalf("plain text answers get the same mask: %s", why)
	}
	if eq, _ := EqualBody(want, got, CompareOptions{}); eq {
		t.Fatal("without the normalizer the lines must differ")
	}
}

func TestEqualBodyCustomNormalizer(t *testing.T) {
	opt := CompareOptions{Normalize: []string{"port=:\\d+"}}
	if eq, why := EqualBody("listen on :8080", "listen on :9090", opt); !eq {
		t.Fatalf("a custom name=regex masks its matches: %s", why)
	}
	if eq, _ := EqualBody("listen on :8080", "listen on 8080", opt); eq {
		t.Fatal("text outside the regex must still match exactly")
	}
}

func TestEqualBodyDoesNotMaskEpochNumbers(t *testing.T) {
	// Deliberate: an epoch-looking integer is data, not a clock (see CompareOptions.Normalize).
	if eq, _ := EqualBody(`{"ms": 1795277025000}`, `{"ms": 1000000000000}`, CompareOptions{JSONBody: true, Normalize: []string{"timestamps"}}); eq {
		t.Fatal("bare epoch numbers must not be masked")
	}
}

func TestValidateRejectsBadNormalizers(t *testing.T) {
	for spec, want := range map[string]string{
		"epoch":     "unknown normalizer",
		"=d+":       "must be non-empty",
		"a b=[0-9]": "must be non-empty",
		"bad=[":     "normalizer bad",
	} {
		opt := CompareOptions{Normalize: []string{spec}}
		if err := opt.Validate(); err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("%q: want an error naming %q, got %v", spec, want, err)
		}
	}
}

func TestCompareAnswerNormalizesStderr(t *testing.T) {
	want := Answer{Kind: "cli", ExitCode: 0, Stderr: "done at 2026-11-22T01:23:45Z\n"}
	got := Answer{Kind: "cli", ExitCode: 0, Stderr: "done at 2026-11-23T01:23:45Z\n"}
	if diffs := CompareAnswer(want, got, nil, CompareOptions{Normalize: []string{"timestamps"}}); len(diffs) != 0 {
		t.Fatalf("stderr gets the same mask: %v", diffs)
	}
}

// TestBuiltinNormalizerMasksPinsTheReplacement fixes, as exact strings, what each new built-in writes
// into an answer: every mask must keep a JSON answer valid JSON (the key and the quotes survive).
func TestBuiltinNormalizerMasksPinsTheReplacement(t *testing.T) {
	cases := []struct {
		name string
		in   string
		norm string
		out  string
	}{
		{
			"host_urls masks scheme and host, keeps path and query",
			`{"url": "http://10.0.0.7:8080/orders?page=2"}`,
			"host_urls",
			`{"url": "<host_urls>/orders?page=2"}`,
		},
		{
			"host_urls catches slashes escaped by PHP json_encode",
			`{"link": "https:\/\/api.example.com\/v1"}`,
			"host_urls",
			`{"link": "<host_urls>\/v1"}`,
		},
		{
			"counts quotes the mask so the JSON stays valid; a fixed setting is not touched",
			`{"total": 42, "error_count":3, "per_page": 20}`,
			"counts",
			`{"total": "<counts>", "error_count":"<counts>", "per_page": 20}`,
		},
		{
			"tokens re-emits the key with a quoted mask",
			`{"access_token": "abc123", "refresh_token":"r-2", "user": "kim"}`,
			"tokens",
			`{"access_token":"<tokens>", "refresh_token":"<tokens>", "user": "kim"}`,
		},
		{
			"session_ids re-emits the key with its original case",
			`{"SessionId": "s-1", "session_id": "s-2", "id": "s-3"}`,
			"session_ids",
			`{"SessionId":"<session_ids>", "session_id":"<session_ids>", "id": "s-3"}`,
		},
		{
			"oauth keeps separator and parameter name, in URLs and HTML-escaped text",
			`/next?state=xyz&nonce=abc&x=1 and a&amp;state=efg in HTML`,
			"oauth",
			`/next?state=<oauth>&nonce=<oauth>&x=1 and a&amp;state=<oauth> in HTML`,
		},
	}
	for _, c := range cases {
		got := normalizeForCompare(c.in, mustNormalizers(t, []string{c.norm}))
		if got != c.out {
			t.Errorf("%s:\n  got  %s\n  want %s", c.name, got, c.out)
		}
	}
}

// mustNormalizers parses specs or fails the test.
func mustNormalizers(t *testing.T, specs []string) []normalizer {
	t.Helper()
	ns, err := parseNormalizers(specs)
	if err != nil {
		t.Fatalf("built-in specs must parse: %v", err)
	}
	return ns
}

// TestCountsMaskKeepsJSONComparisonSemantic pins the interplay the counts mask exists for: after masking,
// two answers whose counters differ still compare as equal JSON, and the comparison is still semantic
// (key order and whitespace ignored), not a downgraded text comparison.
func TestCountsMaskKeepsJSONComparisonSemantic(t *testing.T) {
	want := `{"total": 5, "items": ["a", "b"]}`
	got := `{"items":["a","b"],"total":999}`
	opt := CompareOptions{JSONBody: true, Normalize: []string{"counts"}}
	if eq, why := EqualBody(want, got, opt); !eq {
		t.Fatalf("differing counters must compare as equal under counts: %s", why)
	}
	// The equal-but-not-identical bytes prove the comparison was JSON semantics: an exact comparison
	// would fail because key order and whitespace differ.
	if eq, _ := EqualBody(want, got, CompareOptions{JSONBody: true}); eq {
		t.Fatal("the unmasked answers must differ (key order and counter), or this test proves nothing")
	}
	// A masked answer must still be parseable JSON: masking must not break the structure.
	gotMasked := normalizeForCompare(want, mustNormalizers(t, []string{"counts"}))
	if _, err := parseStrictJSON(gotMasked); err != nil {
		t.Fatalf("the counts mask must keep the answer valid JSON: %v (masked: %s)", err, gotMasked)
	}
	if _, err := parseStrictJSON(normalizeForCompare(got, mustNormalizers(t, []string{"counts"}))); err != nil {
		t.Fatalf("the counts mask must keep the answer valid JSON: %v", err)
	}
}

// TestUnknownNormalizerErrorListsBuiltins: the error must name every built-in, generated from the map
// so a new built-in cannot be missing from the message.
func TestUnknownNormalizerErrorListsBuiltins(t *testing.T) {
	_, err := parseNormalizers([]string{"epoch"})
	if err == nil {
		t.Fatal("an unknown normalizer must be rejected")
	}
	for _, name := range []string{"counts", "host_urls", "oauth", "session_ids", "timestamps", "tokens", "uuids"} {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("the unknown-normalizer error must list the built-in %s: %v", name, err)
		}
	}
	if !strings.Contains(err.Error(), "name=regex") {
		t.Errorf("the error must still say how to ask for a custom mask: %v", err)
	}
}
