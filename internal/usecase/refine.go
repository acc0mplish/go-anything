package usecase

import (
	"fmt"
	"net/url"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/go-anything/go-anything/internal/domain"
)

// ProbeCases builds the requests used to look for differences between the original and the rewrite.
// For a command-line unit each value is tried as an argument and as stdin. For an HTTP unit each value
// is tried as the last path segment, with and without a trailing slash, and in up to two raw
// percent-encoded forms (see rawPathVariants). A value adds at most four HTTP requests.
func ProbeCases(kind, unit, prefix string, values []string) []domain.AppCase {
	var out []domain.AppCase
	for i, v := range values {
		if kind == "cli" {
			out = append(out,
				domain.AppCase{Name: fmt.Sprintf("probe-arg-%d", i), Kind: "cli", Argv: []string{unit, v}},
				domain.AppCase{Name: fmt.Sprintf("probe-stdin-%d", i), Kind: "cli", Argv: []string{unit}, Stdin: v + "\n"},
			)
			continue
		}
		// Two path shapes per value: the value alone, and the value with a trailing slash. The second
		// catches rewrites that are more lenient about URL shape than the original.
		plain := url.PathEscape(v)
		out = append(out,
			domain.AppCase{Name: fmt.Sprintf("probe-%d", i), Kind: "http", Method: "GET", Path: prefix + plain},
			domain.AppCase{Name: fmt.Sprintf("probe-slash-%d", i), Kind: "http", Method: "GET", Path: prefix + plain + "/"},
		)
		// Raw forms are added on the plain shape only, so the expansion per value stays fixed.
		for _, rv := range rawPathVariants(v) {
			out = append(out, domain.AppCase{Name: fmt.Sprintf("probe-%s-%d", rv.label, i), Kind: "http", Method: "GET", Path: prefix + rv.escaped})
		}
	}
	return out
}

// rawPathVariant is a path segment written in a raw percent-encoded form that differs from url.PathEscape.
type rawPathVariant struct {
	label   string // used in the case name, e.g. "hex"
	escaped string // the segment as it goes on the wire
}

// rawPathVariants returns the raw encodings of a path value that the probe sends besides the plain one:
// "hex" writes every ASCII letter or digit as %XX, and "hex-first" writes only the first character as %XX.
// A form is returned only when it differs from the plain escaping and from the form before it. A value
// with no ASCII letter or digit returns nothing, because its raw encodings would repeat the plain one.
func rawPathVariants(v string) []rawPathVariant {
	if !hasASCIIAlnum(v) {
		return nil
	}
	plain := url.PathEscape(v)

	// url.PathEscape works byte by byte, so escaping each byte on its own gives the same result for the rest.
	var hex strings.Builder
	for i := 0; i < len(v); i++ {
		if isASCIIAlnum(v[i]) {
			fmt.Fprintf(&hex, "%%%02X", v[i])
		} else {
			hex.WriteString(url.PathEscape(v[i : i+1]))
		}
	}

	_, size := utf8.DecodeRuneInString(v)
	var first strings.Builder
	for i := 0; i < size; i++ {
		fmt.Fprintf(&first, "%%%02X", v[i])
	}
	first.WriteString(url.PathEscape(v[size:]))

	var out []rawPathVariant
	for _, c := range []rawPathVariant{{"hex", hex.String()}, {"hex-first", first.String()}} {
		if c.escaped == plain || (len(out) > 0 && out[0].escaped == c.escaped) {
			continue
		}
		out = append(out, c)
	}
	return out
}

// hasASCIIAlnum reports whether v holds an ASCII letter or digit.
func hasASCIIAlnum(v string) bool {
	for i := 0; i < len(v); i++ {
		if isASCIIAlnum(v[i]) {
			return true
		}
	}
	return false
}

func isASCIIAlnum(b byte) bool {
	return (b >= '0' && b <= '9') || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

// CaseKey identifies a request, so a probe that is already recorded is not recorded twice.
func CaseKey(c domain.AppCase) string {
	if c.Kind == "cli" {
		return "cli\x00" + strings.Join(c.Argv, "\x00") + "\x00" + c.Stdin
	}
	return "http\x00" + c.Method + " " + c.Path + "\x00" + c.Body
}

// CompareAnswers reports how two answers to the same request differ. Empty means they agree.
func CompareAnswers(want, got domain.AppCase) []AppMismatch {
	return compareApp(want, got)
}

// CompareAnswersWith is CompareAnswers with the body and stdout compared under opt (JSON semantic,
// float tolerance). The zero opt behaves exactly like CompareAnswers.
func CompareAnswersWith(want, got domain.AppCase, opt domain.CompareOptions) []AppMismatch {
	return compareAppWith(want, got, opt)
}

// WithAnswer returns the request with the given answer fields, for recording it.
func WithAnswer(request, answer domain.AppCase, name string) domain.AppCase {
	c := withAnswer(request, answer)
	c.Name = name
	return c
}

// ProbeOutcome is what one probe found once the original's and the rewrite's answers are compared.
type ProbeOutcome int

const (
	// ProbeAgree means the answers match (fields the case marks unstable are not compared).
	ProbeAgree ProbeOutcome = iota
	// ProbeOriginalDefect means the original crashed under the defect policy: report it, do not record it.
	ProbeOriginalDefect
	// ProbeDisagree means the rewrite must change: the original's answer is the behavior to match.
	ProbeDisagree
)

// JudgeProbe compares the original's answer (want) with the rewrite's (got). recordsCrash is the defect
// policy: when it is true, an original crash is behavior to match, but only its exit code (CLI) or status
// (HTTP) and stdout: the crash text is marked unstable (see CrashUnstable), because no rewrite can
// reproduce a traceback byte for byte. The returned want carries that mark, for recording.
func JudgeProbe(want, got domain.AppCase, recordsCrash bool) (ProbeOutcome, domain.AppCase) {
	return JudgeProbeWith(want, got, recordsCrash, domain.CompareOptions{})
}

// JudgeProbeWith is JudgeProbe with the body and stdout compared under opt (JSON semantic, float
// tolerance). The zero opt behaves exactly like JudgeProbe.
func JudgeProbeWith(want, got domain.AppCase, recordsCrash bool, opt domain.CompareOptions) (ProbeOutcome, domain.AppCase) {
	crashed := OriginalCrashed(want)
	// A dropped connection is a defect under every policy: no handler can return "no response" in a
	// test, so recording it as behavior to match would ask for the impossible.
	if crashed && want.Kind == "http" && want.Status == domain.NoResponse {
		if len(CompareAnswersWith(want, got, opt)) == 0 {
			return ProbeAgree, want
		}
		return ProbeOriginalDefect, want
	}
	if crashed && recordsCrash {
		want.Unstable = CrashUnstable(want)
	}
	if len(CompareAnswersWith(want, got, opt)) == 0 {
		return ProbeAgree, want
	}
	if crashed && !recordsCrash {
		return ProbeOriginalDefect, want
	}
	return ProbeDisagree, want
}

// CrashUnstable returns the case's Unstable list with the crash text field added: "stderr" for a
// command-line case, "body" for an HTTP case. The exit code, stdout and status are still compared.
func CrashUnstable(c domain.AppCase) []string {
	field := "stderr"
	if c.Kind == "http" {
		field = "body"
	}
	out := append([]string(nil), c.Unstable...)
	if !slices.Contains(out, field) {
		out = append(out, field)
	}
	return out
}

// RecordAnswer returns the request with the original's answer, named for the recording. A crash answer
// recorded under the expected policy keeps its Unstable mark, and its crash text is masked
// (MaskCrashText) before it reaches the recording on disk.
func RecordAnswer(request, answer domain.AppCase, name string) domain.AppCase {
	c := WithAnswer(request, answer, name)
	c.Unstable = append([]string(nil), answer.Unstable...)
	if slices.Contains(c.Unstable, "stderr") && OriginalCrashed(answer) {
		c.Stderr = MaskCrashText(c.Stderr)
	}
	if slices.Contains(c.Unstable, "body") && OriginalCrashed(answer) {
		c.ResponseBody = MaskCrashText(c.ResponseBody)
	}
	return c
}

// ShapeValuesPerRound is how many values of a round also get the request-shape variants.
const ShapeValuesPerRound = 3

// RequestShapeProbes varies the shape of an HTTP request rather than its value, for at most n non-empty
// values spread over the list (so a generated, seed-dependent value is among them): a query string
// appended ("?x=1"), a doubled slash before the value, a trailing dot, and the method in lower case
// ("get"). Each chosen value adds at most four requests, so a round adds at most 4*n. The doubled
// slash is skipped when the prefix is "/", where "//value" would read as a host name.
func RequestShapeProbes(prefix string, values []string, n int) []domain.AppCase {
	var pool []string
	for _, v := range values {
		if v != "" {
			pool = append(pool, v)
		}
	}
	if n <= 0 || len(pool) == 0 {
		return nil
	}
	n = min(n, len(pool))
	base := strings.TrimSuffix(prefix, "/")
	var out []domain.AppCase
	for k := 0; k < n; k++ {
		v := pool[k*len(pool)/n]
		plain := url.PathEscape(v)
		add := func(label, method, path string) {
			out = append(out, domain.AppCase{Name: fmt.Sprintf("probe-shape-%s-%d", label, k), Kind: "http", Method: method, Path: path})
		}
		add("query", "GET", prefix+plain+"?x=1")
		if base != "" {
			add("dslash", "GET", base+"//"+plain)
		}
		add("dot", "GET", prefix+plain+".")
		add("lower", "get", prefix+plain)
	}
	return out
}
