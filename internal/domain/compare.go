package domain

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/big"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

// CompareOptions says how a recorded answer (an HTTP body, CLI stdout, a unit output) is compared with
// the rewrite's answer. The zero value is exact comparison: the bytes must be equal.
type CompareOptions struct {
	// JSONBody compares two answers semantically when BOTH parse as one JSON value: object key order and
	// insignificant whitespace are ignored, arrays stay ordered, strings compare by value, and numbers
	// compare by exact decimal value (1, 1.0 and 1e0 are equal; no float64 rounding is involved). When
	// either side is not JSON, is larger than MaxJSONCompareBytes, or has a duplicate object key, the
	// answers are compared exactly instead and the mismatch detail says why.
	JSONBody bool `json:"json_body,omitempty"`
	// FloatTolerance, when > 0, lets two numbers differ by at most this much: they are equal when
	// |want-got| <= tol (absolute) OR |want-got| <= tol*max(|want|,|got|) (relative). It applies to the
	// numbers inside JSON answers (with JSONBody) and to lines that are a number as a whole (in any answer
	// compared as text). 0 means numbers must be equal exactly. It must be finite and >= 0.
	FloatTolerance float64 `json:"float_tolerance,omitempty"`
	// Normalize names value normalizers applied to BOTH sides before comparison: a built-in name
	// ("timestamps", "uuids", "host_urls", "counts", "tokens", "session_ids", "oauth") or a custom
	// "name=regex" whose matches are replaced by "<name>" on both sides. Volatile values that differ
	// between the original and the rewrite (a clock reading, a fresh UUID, a session token) then compare
	// as equal. Every built-in mask is written so the answer stays valid JSON: a mask inside a string
	// value stays inside the quotes, and the built-ins that match a whole string field re-emit the key
	// with a quoted "<mask>" value. A custom regex that eats structural bytes (quotes, braces) is the
	// operator's responsibility. One built-in deliberately masks numbers: "counts" replaces the integer
	// under a count-like key with the quoted string "<counts>", so a volatile counter on one side only
	// compares as equal, not as a number. Every other built-in leaves numbers alone: an epoch-looking
	// integer is data, not a clock (a date written as 2026-11-22T01:23:45Z is masked; the same moment as
	// 1795277025000 is not).
	Normalize []string `json:"normalize,omitempty"`
}

// MaxJSONCompareBytes is the largest answer that is parsed for semantic JSON comparison. A larger
// answer is compared exactly, so a huge body cannot make verification slow or use much memory.
const MaxJSONCompareBytes = 8 << 20

// Limits that keep number and nesting handling cheap. A number beyond them is compared as written.
const (
	maxJSONDepth     = 1000
	maxNumberDigits  = 1000
	maxNumberExpAbs  = 1000
	compareValueClip = 120
)

// ErrInvalidCompareOptions is returned for a tolerance that is negative, NaN or infinite.
var ErrInvalidCompareOptions = errors.New("domain: invalid compare options")

// Validate rejects a tolerance that is not a finite number >= 0 and a normalizer spec that is neither
// a built-in name nor a compiling "name=regex".
func (o CompareOptions) Validate() error {
	t := o.FloatTolerance
	if math.IsNaN(t) || math.IsInf(t, 0) || t < 0 {
		return fmt.Errorf("%w: float tolerance must be a finite number >= 0, got %v", ErrInvalidCompareOptions, t)
	}
	if _, err := parseNormalizers(o.Normalize); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidCompareOptions, err)
	}
	return nil
}

// Describe says in one phrase which comparison was used, for reports.
func (o CompareOptions) Describe() string {
	var s string
	if o.JSONBody {
		s = "JSON semantic (key order and whitespace ignored, arrays ordered, numbers by exact decimal value; non-JSON answers exact)"
	} else {
		s = "exact"
	}
	if o.FloatTolerance > 0 {
		s += fmt.Sprintf(", float tolerance %g (absolute or relative)", o.FloatTolerance)
	}
	if len(o.Normalize) > 0 {
		s += ", normalized " + strings.Join(o.Normalize, ", ")
	}
	return s
}

// normalizer replaces volatile values with a fixed mask on both sides of a comparison. Repl is the
// replacement text for each match; built-ins set it so masking keeps a JSON answer valid (the key and
// the surrounding quotes survive), a custom regex masks each match as "<name>".
type normalizer struct {
	name string
	re   *regexp.Regexp
	repl string
}

// builtinNormalizers are the named masks every command can ask for by name.
var builtinNormalizers = map[string]*regexp.Regexp{
	// A date, or a date with a time and optional zone: 2026-11-22, 2026-11-22T01:23:45Z,
	// 2026-11-22 01:23:45.123+09:00. Deliberately no bare-epoch form (see CompareOptions.Normalize).
	"timestamps": regexp.MustCompile(`\d{4}-\d{2}-\d{2}([T ]\d{2}:\d{2}(:\d{2}(\.\d{1,9})?)?(Z|[+-]\d{2}:?\d{2})?)?`),
	// A canonical UUID, case-insensitive: 01234567-89ab-cdef-0123-456789abcdef.
	"uuids": regexp.MustCompile(`(?i)[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`),
	// Scheme and host (with optional port) of an http(s) URL; the path and query stay compared.
	// The \?/ form also catches a slash escaped as \/ (PHP json_encode), inside a JSON string value or in
	// plain text such as a redirect page.
	"host_urls": regexp.MustCompile(`https?:\\?/\\?/[A-Za-z0-9._~-]+(:\d+)?`),
	// The integer under a count-like key: "total", "count", "unread", "unread_count", "*_count". The key
	// ($1) and separator ($2) survive and the value becomes the quoted string "<counts>", so a JSON answer
	// stays valid; a fixed setting such as "per_page" is not matched.
	"counts": regexp.MustCompile(`"(total|count|unread|unread_count|[A-Za-z_]+_count)"(\s*:\s*)\d+`),
	// A whole JSON string field whose name ends in token, api_key/api-key or secret: the value is replaced
	// by "<tokens>" and the key ($1) is re-emitted with quotes, keeping the JSON valid.
	"tokens": regexp.MustCompile(`"([a-z_]*(?:token|api_?key|secret))"\s*:\s*"[^"]*"`),
	// A whole JSON string field named session_id, sessionid or sessionId (case-insensitive key, preserved).
	"session_ids": regexp.MustCompile(`"((?i:session_?id))"\s*:\s*"[^"]*"`),
	// The per-run query parameters of an OAuth flow (state, nonce): the separator ($1) and the parameter
	// name ($2) survive. Also matches HTML-escaped &amp; in a redirect page.
	"oauth": regexp.MustCompile(`(&amp;|[?&])(state|nonce)=[^&"'<\\\s]+`),
}

// builtinReplacement is the replacement text for the built-ins whose mask must rebuild part of the
// match to keep a JSON answer valid. A built-in missing here masks each match as "<name>".
var builtinReplacement = map[string]string{
	"counts":      `"${1}"${2}"<counts>"`,
	"tokens":      `"${1}":"<tokens>"`,
	"session_ids": `"${1}":"<session_ids>"`,
	"oauth":       `${1}${2}=<oauth>`,
}

// IsBuiltinNormalizer reports whether name is one of the built-in normalizer names. The -normalize
// flag parser uses it to tell a comma-separated list of built-ins apart from a custom regex that
// merely contains a comma.
func IsBuiltinNormalizer(name string) bool {
	_, ok := builtinNormalizers[name]
	return ok
}

// builtinNames lists the built-in normalizer names in fixed order, for error messages.
func builtinNames() string {
	names := make([]string, 0, len(builtinNormalizers))
	for n := range builtinNormalizers {
		names = append(names, n)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

// parseNormalizers turns specs into normalizers, rejecting anything unknown or uncompilable.
func parseNormalizers(specs []string) ([]normalizer, error) {
	out := make([]normalizer, 0, len(specs))
	for _, spec := range specs {
		if re, ok := builtinNormalizers[spec]; ok {
			repl, ok := builtinReplacement[spec]
			if !ok {
				repl = "<" + spec + ">"
			}
			out = append(out, normalizer{name: spec, re: re, repl: repl})
			continue
		}
		name, pattern, ok := strings.Cut(spec, "=")
		if !ok {
			return nil, fmt.Errorf("unknown normalizer %q: use one of the built-ins (%s) or name=regex", spec, builtinNames())
		}
		if name == "" || strings.ContainsAny(name, "<>= \t\n") {
			return nil, fmt.Errorf("normalizer name %q must be non-empty and free of <, >, = and whitespace", name)
		}
		re, err := regexp.Compile(pattern)
		if err != nil {
			return nil, fmt.Errorf("normalizer %s: %w", name, err)
		}
		out = append(out, normalizer{name: name, re: re, repl: "<" + name + ">"})
	}
	return out, nil
}

// normalizeForCompare masks volatile values on one side of a comparison.
func normalizeForCompare(s string, ns []normalizer) string {
	for _, n := range ns {
		s = n.re.ReplaceAllString(s, n.repl)
	}
	return s
}

// EqualBody compares a recorded answer with the rewrite's answer under opt. When they differ, why names
// the first difference: a JSON path such as "$.items[2].price: want 1.5 got 1.50001", or a line number
// for text. When semantic JSON comparison was asked for but could not be used, why also says so.
func EqualBody(want, got string, opt CompareOptions) (equal bool, why string) {
	if opt.Validate() != nil {
		// Invalid options never loosen the comparison.
		opt = CompareOptions{}
	}
	if ns, err := parseNormalizers(opt.Normalize); err == nil && len(ns) > 0 {
		// The mask replaces the volatile value on both sides, so a differing clock reading, token or
		// session id compares as equal while everything around it still must match. Masking runs before
		// the JSON parse; the built-ins rebuild enough of the match (key, quotes, separators) that a JSON
		// answer stays valid JSON. The one exception that touches a number is "counts": it replaces the
		// integer with the quoted string "<counts>" on both sides, so the comparison still sees two
		// strings, never a number loosened into a tolerance.
		want, got = normalizeForCompare(want, ns), normalizeForCompare(got, ns)
	}
	if opt.JSONBody {
		note, ok := "", true
		var wv, gv any
		switch {
		case len(want) > MaxJSONCompareBytes || len(got) > MaxJSONCompareBytes:
			note, ok = fmt.Sprintf("larger than %d bytes, not parsed as JSON", MaxJSONCompareBytes), false
		default:
			var werr, gerr error
			wv, werr = parseStrictJSON(want)
			gv, gerr = parseStrictJSON(got)
			switch {
			case werr != nil:
				note, ok = "want is not usable JSON: "+werr.Error(), false
			case gerr != nil:
				note, ok = "got is not usable JSON: "+gerr.Error(), false
			}
		}
		if ok {
			if path, detail, same := diffJSON("$", wv, gv, opt.FloatTolerance); !same {
				return false, path + ": " + detail
			}
			return true, ""
		}
		eq, w := equalText(want, got, opt.FloatTolerance)
		if eq {
			return true, ""
		}
		return false, w + " (compared exactly: " + note + ")"
	}
	return equalText(want, got, opt.FloatTolerance)
}

// equalText compares text exactly, except that with tol > 0 a line that is a number on both sides may
// differ within the tolerance. It names the first differing line.
func equalText(want, got string, tol float64) (bool, string) {
	if want == got {
		return true, ""
	}
	wl, gl := strings.Split(want, "\n"), strings.Split(got, "\n")
	for i := 0; i < len(wl) && i < len(gl); i++ {
		if wl[i] == gl[i] {
			continue
		}
		if tol > 0 && isJSONNumber(wl[i]) && isJSONNumber(gl[i]) {
			if numbersEqual(wl[i], gl[i], tol) {
				continue
			}
			return false, fmt.Sprintf("line %d: want %s got %s (outside tolerance %g)", i+1, clipValue(wl[i]), clipValue(gl[i]), tol)
		}
		return false, fmt.Sprintf("line %d: want %s got %s", i+1, strconv.Quote(clipValue(wl[i])), strconv.Quote(clipValue(gl[i])))
	}
	if len(wl) != len(gl) {
		return false, fmt.Sprintf("want %d line(s) got %d", len(wl), len(gl))
	}
	return true, ""
}

// jsonObject keeps the keys in document order, so the first difference reported is the first one a
// reader sees in the recorded body.
type jsonObject struct {
	keys []string
	vals map[string]any
}

// parseStrictJSON decodes exactly one JSON value. It refuses invalid UTF-8 (the standard decoder would
// replace bad bytes, making different bodies look equal), duplicate object keys (clients disagree on
// which one wins), nesting deeper than maxJSONDepth, and anything after the value.
func parseStrictJSON(s string) (any, error) {
	if !utf8.ValidString(s) {
		return nil, errors.New("invalid UTF-8")
	}
	dec := json.NewDecoder(strings.NewReader(s))
	dec.UseNumber()
	tok, err := dec.Token()
	if err == io.EOF {
		return nil, errors.New("empty")
	}
	if err != nil {
		return nil, err
	}
	v, err := jsonFromToken(dec, tok, "$", 0)
	if err != nil {
		return nil, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, errors.New("more than one JSON value")
	}
	return v, nil
}

func jsonFromToken(dec *json.Decoder, tok json.Token, path string, depth int) (any, error) {
	d, isDelim := tok.(json.Delim)
	if !isDelim {
		return tok, nil // string, json.Number, bool or nil
	}
	if depth >= maxJSONDepth {
		return nil, fmt.Errorf("nested deeper than %d", maxJSONDepth)
	}
	switch d {
	case '{':
		obj := &jsonObject{vals: map[string]any{}}
		for dec.More() {
			kt, err := dec.Token()
			if err != nil {
				return nil, err
			}
			key, ok := kt.(string)
			if !ok {
				return nil, fmt.Errorf("%s: object key is not a string", path)
			}
			p := childKey(path, key)
			if _, dup := obj.vals[key]; dup {
				return nil, fmt.Errorf("duplicate key at %s", p)
			}
			vt, err := dec.Token()
			if err != nil {
				return nil, err
			}
			v, err := jsonFromToken(dec, vt, p, depth+1)
			if err != nil {
				return nil, err
			}
			obj.keys = append(obj.keys, key)
			obj.vals[key] = v
		}
		if _, err := dec.Token(); err != nil { // the closing brace
			return nil, err
		}
		return obj, nil
	case '[':
		arr := []any{}
		for dec.More() {
			vt, err := dec.Token()
			if err != nil {
				return nil, err
			}
			v, err := jsonFromToken(dec, vt, fmt.Sprintf("%s[%d]", path, len(arr)), depth+1)
			if err != nil {
				return nil, err
			}
			arr = append(arr, v)
		}
		if _, err := dec.Token(); err != nil { // the closing bracket
			return nil, err
		}
		return arr, nil
	}
	return nil, fmt.Errorf("%s: unexpected %q", path, d.String())
}

// diffJSON returns the path and description of the first difference between two decoded values.
func diffJSON(path string, want, got any, tol float64) (string, string, bool) {
	if kindOf(want) != kindOf(got) {
		return path, fmt.Sprintf("want %s %s got %s %s", kindOf(want), showJSON(want), kindOf(got), showJSON(got)), false
	}
	switch w := want.(type) {
	case *jsonObject:
		g := got.(*jsonObject)
		for _, k := range w.keys {
			gv, ok := g.vals[k]
			if !ok {
				return childKey(path, k), "missing in got", false
			}
			if p, d, same := diffJSON(childKey(path, k), w.vals[k], gv, tol); !same {
				return p, d, false
			}
		}
		for _, k := range g.keys {
			if _, ok := w.vals[k]; !ok {
				return childKey(path, k), "not in want, present in got", false
			}
		}
	case []any:
		g := got.([]any)
		for i := 0; i < len(w) && i < len(g); i++ {
			if p, d, same := diffJSON(fmt.Sprintf("%s[%d]", path, i), w[i], g[i], tol); !same {
				return p, d, false
			}
		}
		if len(w) != len(g) {
			return path, fmt.Sprintf("want %d element(s) got %d", len(w), len(g)), false
		}
	case json.Number:
		g := got.(json.Number)
		if !numbersEqual(string(w), string(g), tol) {
			d := fmt.Sprintf("want %s got %s", clipValue(string(w)), clipValue(string(g)))
			if tol > 0 {
				d += fmt.Sprintf(" (outside tolerance %g)", tol)
			}
			return path, d, false
		}
	case string:
		if g := got.(string); w != g {
			return path, fmt.Sprintf("want %s got %s", strconv.Quote(clipValue(w)), strconv.Quote(clipValue(g))), false
		}
	case bool:
		if g := got.(bool); w != g {
			return path, fmt.Sprintf("want %t got %t", w, g), false
		}
	}
	return "", "", true
}

func kindOf(v any) string {
	switch v.(type) {
	case *jsonObject:
		return "object"
	case []any:
		return "array"
	case json.Number:
		return "number"
	case string:
		return "string"
	case bool:
		return "boolean"
	}
	return "null"
}

func showJSON(v any) string {
	switch x := v.(type) {
	case *jsonObject:
		return fmt.Sprintf("{%d key(s)}", len(x.keys))
	case []any:
		return fmt.Sprintf("[%d element(s)]", len(x))
	case json.Number:
		return clipValue(string(x))
	case string:
		return strconv.Quote(clipValue(x))
	case bool:
		return strconv.FormatBool(x)
	}
	return "null"
}

// childKey appends an object key to a path: $.name for a plain identifier, $["a b"] otherwise.
func childKey(path, key string) string {
	plain := key != ""
	for i, r := range key {
		if !(r == '_' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (i > 0 && r >= '0' && r <= '9')) {
			plain = false
			break
		}
	}
	if plain {
		return path + "." + key
	}
	return path + "[" + strconv.Quote(key) + "]"
}

func clipValue(s string) string { return ClipForReport(s, compareValueClip) }

// numbersEqual compares two JSON number literals by exact decimal value, within tol when tol > 0.
// A literal too long or with too large an exponent to evaluate cheaply is compared as written.
func numbersEqual(a, b string, tol float64) bool {
	if a == b {
		return true
	}
	ra, okA := ratOf(a)
	rb, okB := ratOf(b)
	if !okA || !okB {
		return false
	}
	if ra.Cmp(rb) == 0 {
		return true
	}
	if tol <= 0 {
		return false
	}
	t := new(big.Rat)
	if t.SetFloat64(tol) == nil {
		return false
	}
	diff := new(big.Rat).Sub(ra, rb)
	diff.Abs(diff)
	if diff.Cmp(t) <= 0 {
		return true // absolute
	}
	scale := new(big.Rat).Abs(ra)
	if bAbs := new(big.Rat).Abs(rb); bAbs.Cmp(scale) > 0 {
		scale = bAbs
	}
	return diff.Cmp(scale.Mul(scale, t)) <= 0 // relative
}

// ratOf evaluates a JSON number literal exactly, within the digit and exponent limits.
func ratOf(s string) (*big.Rat, bool) {
	if len(s) > maxNumberDigits || !isJSONNumber(s) {
		return nil, false
	}
	if i := strings.IndexAny(s, "eE"); i >= 0 {
		exp, err := strconv.Atoi(strings.TrimPrefix(s[i+1:], "+"))
		if err != nil || exp > maxNumberExpAbs || exp < -maxNumberExpAbs {
			return nil, false
		}
	}
	r, ok := new(big.Rat).SetString(s)
	return r, ok
}

// isJSONNumber reports whether s is exactly one JSON number: no sign other than a leading minus, no
// surrounding space, no hex, and no NaN or Infinity, so text that only looks numeric is never loosened.
func isJSONNumber(s string) bool {
	i, n := 0, len(s)
	digits := func() int {
		start := i
		for i < n && s[i] >= '0' && s[i] <= '9' {
			i++
		}
		return i - start
	}
	if i < n && s[i] == '-' {
		i++
	}
	switch {
	case i < n && s[i] == '0':
		i++
	case digits() == 0:
		return false
	}
	if i < n && s[i] == '.' {
		i++
		if digits() == 0 {
			return false
		}
	}
	if i < n && (s[i] == 'e' || s[i] == 'E') {
		i++
		if i < n && (s[i] == '+' || s[i] == '-') {
			i++
		}
		if digits() == 0 {
			return false
		}
	}
	return i == n
}

// ClipForReport shortens a value to limit bytes on a rune boundary and says how much was left out.
func ClipForReport(s string, limit int) string {
	if limit <= 0 || len(s) <= limit {
		return s
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + fmt.Sprintf(" [... %d more bytes not shown]", len(s)-cut)
}
