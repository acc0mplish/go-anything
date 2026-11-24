package wiregen

// chainprobe — the consecutive-request check (-chains, HTTP units only). It ports the contract of the
// original tool's chain probe: collection targets are derived from the recording, each target gets a
// self-named resource under a reserved name (chain-rN-xxxxxxxx), and the check walks that one resource
// through create, read, mutate and delete, comparing the two sides step by step. A rewrite that only
// replays the recording collapses on these new requests and shows up as a divergence.
//
// Safety contracts kept from the ported design:
//   - targets come only from this unit's recorded cases, so the check never fires at another unit's
//     paths;
//   - every request carries the reserved name, so a chain touches its own resource only and never a
//     list or an operator-accumulated resource;
//   - a step uses a verb only if the recording observed it — nothing outside the recording is invented;
//   - request header values are expanded from ${ENV} at send time and never land in a transcript, so a
//     token cannot ride into a report.
//
// When DELETE was never observed the created resource stays behind on the original under its reserved
// name; the README says so, and the name keeps the leftovers findable and deletable by hand.

import (
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/go-anything/go-anything/internal/adapter/app"
	"github.com/go-anything/go-anything/internal/domain"
)

// chainTarget is one collection the chain check will exercise. Collection is the slash-joined name
// used for sorting and reports ("orders" for /orders/5). PostPath is the exact path a POST to the
// collection was observed on ("/orders/" or "/orders") — the requests use it verbatim, because the
// rewrite program's router answers the shape the recording saw (a Go ServeMux mounted at /orders/
// answers /orders/ but 307-redirects /orders, and the chain client does not follow redirects, so a
// derived path would diverge on routing, not behavior). Verbs are the methods the recording observed
// on the collection, sorted; the steps use only these. Headers are the recorded request headers of
// the first case that matched the collection, with ${ENV} references left intact for send-time
// expansion. Templates map an observed verb to its first recorded body.
type chainTarget struct {
	Collection string
	PostPath   string
	Verbs      []string
	Headers    map[string]string
	Templates  map[string]string
}

// chainStep is one request of a chain. Name is the reserved resource name. Kind marks the role of the
// step (post, get, put, patch, delete, get-after-delete); the runner uses it to find the created id.
type chainStep struct {
	Name           string
	Kind           string
	Method         string
	Path           string
	Body           string
	RequestHeaders map[string]string
}

// chainStepOutcome is one step's transcript on one side: what was asked (method and path only) and
// what came back. It deliberately carries no request headers: a header value is expanded from ${ENV}
// at send time, and recording it would put a token in a file a report links to.
type chainStepOutcome struct {
	Method string `json:"method"`
	Path   string `json:"path"`
	Status int    `json:"status"`
	Body   string `json:"body,omitempty"`
	Err    string `json:"error,omitempty"`
}

// chainVerdict judges one chain as a whole; the step index is a clue, not the unit of judgment.
// FirstDivergence is -1 when the chain did not diverge.
type chainVerdict struct {
	Divergent       bool   `json:"divergent,omitempty"`
	FirstDivergence int    `json:"first_divergence"`
	Uncertain       bool   `json:"uncertain,omitempty"`
	UncertainReason string `json:"uncertain_reason,omitempty"`
}

// chainTranscript is one chain's request/answer record on both sides, with its verdict. It is the
// body of the chain-round-NN*.json files and the evidence a report links to.
type chainTranscript struct {
	Round    int                `json:"round"`
	Target   string             `json:"target"`
	Name     string             `json:"name"`
	Original []chainStepOutcome `json:"original"`
	Rewrite  []chainStepOutcome `json:"rewrite"`
	Verdict  chainVerdict       `json:"verdict"`
}

// chainTargets derives the chain targets from one unit's recorded cases. Rules: a case counts only
// when it is HTTP, has a method, carries no query and was recorded with a status under 400 (an error
// path is not an observable collection). Collections are cut from item paths (two segments or more)
// by dropping the last segment, and a collection stays a target only if the recording observed a POST
// on it — without an observed create there is no way to make the check's own resource. Verbs, headers
// and body templates are collected per collection from the same cases, first observation winning, so
// the derivation is deterministic in recording order. Targets are sorted by collection name and cut to
// the limit. No POST anywhere yields an empty slice — the caller logs that, it does not fail.
func chainTargets(cases []domain.AppCase, limit int) []chainTarget {
	verbs := map[string]map[string]bool{}
	// Pass 1: cut collections out of the item paths, so only a collection an item was read from
	// becomes a target.
	for _, c := range cases {
		if !chainCaseEligible(c) {
			continue
		}
		segs := chainSegments(c.Path)
		if len(segs) < 2 {
			continue // a single-segment path names no collection for this purpose
		}
		coll := strings.Join(segs[:len(segs)-1], "/")
		if verbs[coll] == nil {
			verbs[coll] = map[string]bool{}
		}
	}
	// Pass 2: collect the observed verbs, the first request headers and the first body per verb, from
	// every case on the collection or under it. The POST path is the raw recorded path of the first
	// POST the collection saw.
	postPath := map[string]string{}
	meta := map[string]map[string]string{} // collection -> verb -> first recorded body
	hdr := map[string]map[string]string{}  // collection -> first recorded request headers
	for _, c := range cases {
		if !chainCaseEligible(c) {
			continue
		}
		joined := "/" + strings.Join(chainSegments(c.Path), "/")
		if joined == "/" {
			continue
		}
		for coll, m := range verbs {
			if joined != "/"+coll && !strings.HasPrefix(joined, "/"+coll+"/") {
				continue
			}
			verb := strings.ToUpper(strings.TrimSpace(c.Method))
			m[verb] = true
			if postPath[coll] == "" && verb == "POST" {
				postPath[coll] = c.Path
			}
			if hdr[coll] == nil && len(c.RequestHeaders) > 0 {
				h := map[string]string{}
				for k, v := range c.RequestHeaders {
					h[k] = v
				}
				hdr[coll] = h
			}
			if c.Body != "" {
				if meta[coll] == nil {
					meta[coll] = map[string]string{}
				}
				if _, ok := meta[coll][verb]; !ok {
					meta[coll][verb] = c.Body
				}
			}
		}
	}
	out := make([]chainTarget, 0, len(verbs))
	for coll, m := range verbs {
		if !m["POST"] || postPath[coll] == "" {
			continue // no observed create: the check could not make its own resource here
		}
		vs := make([]string, 0, len(m))
		for v := range m {
			vs = append(vs, v)
		}
		sort.Strings(vs)
		out = append(out, chainTarget{Collection: coll, PostPath: postPath[coll], Verbs: vs, Headers: hdr[coll], Templates: meta[coll]})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Collection < out[j].Collection })
	if limit >= 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

// chainCaseEligible says whether a case may feed the target derivation: HTTP, a method, no query, and
// a recorded status under 400 (0, unset, counts — an answer was simply not compared at record time).
func chainCaseEligible(c domain.AppCase) bool {
	if c.Kind != "" && c.Kind != "http" {
		return false
	}
	if strings.TrimSpace(c.Method) == "" || strings.Contains(c.Path, "?") {
		return false
	}
	return c.Status < 400
}

// chainSegments strips the query and splits the path on slashes, dropping empty segments, so trailing
// and doubled slashes cannot shape a collection.
func chainSegments(path string) []string {
	path = strings.SplitN(path, "?", 2)[0]
	var segs []string
	for _, s := range strings.Split(strings.Trim(path, "/"), "/") {
		if s != "" {
			segs = append(segs, s)
		}
	}
	return segs
}

// chainName builds the reserved resource name from the seed and the round, deterministically. The
// charset [a-z0-9-] is safe as a path segment and inside JSON, and a new round gets a new name, so a
// leftover from an earlier round never meets this one.
func chainName(seed uint64, round int) string {
	r := rand.New(rand.NewPCG(seed, uint64(round)*0x9E3779B97F4A7C15^seed))
	return fmt.Sprintf("chain-r%d-%08x", round, r.Uint32())
}

// chainSteps builds one target's chain: POST the collection to create the reserved resource, then
// GET it, then — only for verbs the recording observed — mutate it and read it back, and finally
// delete it and read the absence. A POST carries the client-assigned id (the reserved name) in its
// body, so a server that honors client ids creates the resource exactly where the following steps
// look; a server that assigns its own id is handled per side by the id replacement in runChainAgainst.
func chainCases(seed uint64, t chainTarget, round int) []chainStep {
	name := chainName(seed, round)
	base := strings.TrimSuffix(t.PostPath, "/")
	itemPath := base + "/" + name
	vals := domain.ProbeValues(seed, 2)
	h := t.Headers
	steps := []chainStep{
		{Name: name, Kind: "post", Method: "POST", Path: t.PostPath, Body: chainBody(name, t.Templates["POST"], vals), RequestHeaders: h},
		{Name: name, Kind: "get", Method: "GET", Path: itemPath, RequestHeaders: h},
	}
	observed := func(verb string) bool {
		for _, v := range t.Verbs {
			if v == verb {
				return true
			}
		}
		return false
	}
	if observed("PUT") {
		steps = append(steps,
			chainStep{Name: name, Kind: "put", Method: "PUT", Path: itemPath, Body: chainBody(name, t.Templates["PUT"], domain.ProbeValues(seed^0xA5A5A5A5+1, 2)), RequestHeaders: h},
			chainStep{Name: name, Kind: "get", Method: "GET", Path: itemPath, RequestHeaders: h},
		)
	}
	if observed("PATCH") {
		steps = append(steps,
			chainStep{Name: name, Kind: "patch", Method: "PATCH", Path: itemPath, Body: chainBody(name, t.Templates["PATCH"], domain.ProbeValues(seed^0xA5A5A5A5+2, 2)), RequestHeaders: h},
			chainStep{Name: name, Kind: "get", Method: "GET", Path: itemPath, RequestHeaders: h},
		)
	}
	if observed("DELETE") {
		steps = append(steps,
			chainStep{Name: name, Kind: "delete", Method: "DELETE", Path: itemPath, RequestHeaders: h},
			chainStep{Name: name, Kind: "get-after-delete", Method: "GET", Path: itemPath, RequestHeaders: h},
		)
	}
	return steps
}

// chainBody builds a create or mutate body. A recorded body that parses as a JSON object is inherited
// field by field — required fields survive — and the alphabetically first string field carries the
// check value, so each round mutates differently. Anything else falls back to chainObject. The value
// comes from the probe generator and may hold odd bytes; json.Marshal makes it safe inside a string.
func chainBody(name, template string, vals []string) string {
	m := map[string]any{}
	if template == "" || json.Unmarshal([]byte(template), &m) != nil {
		return chainObject(name, vals)
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if _, ok := m[k].(string); ok {
			if len(vals) > 0 {
				m[k] = name + "-" + vals[0]
			}
			break
		}
	}
	b, err := json.Marshal(m)
	if err != nil {
		return chainObject(name, vals)
	}
	return string(b)
}

// chainObject builds the fallback create/mutate body: the reserved name as the client-assigned id and
// probe values as the other fields. Keys marshal sorted, so the same inputs always give the same body.
func chainObject(name string, vals []string) string {
	at := func(i int) string {
		if i < len(vals) {
			return vals[i]
		}
		return ""
	}
	b, err := json.Marshal(map[string]string{"id": name, "title": at(0), "body": at(1)})
	if err != nil {
		return fmt.Sprintf(`{"id":%q}`, name) // unreachable: a map[string]string always marshals
	}
	return string(b)
}

// runChainAgainst walks one side through the steps in order and returns the transcript. If the POST
// response names a different id than the reserved name (the server assigns ids), the later steps'
// paths and bodies follow that id — per side, so two sides that assigned different ids each keep
// walking their own resource and the judgment compares behavior, not id spellings. The sends go
// through app.Runner with the caller's client (the no-redirect client the concurrent check uses), so
// request building, ${ENV} header expansion and the default Content-Type are exactly every other
// request's rule; a header naming an unset variable fails the step, and a failed step makes the
// verdict uncertain.
//
// A deliberate deviation from the eGov-anything original: its checker followed redirects, while this
// client does not — a redirecting step is compared by the redirect response itself (its status and
// body, not the destination). For a PRG-style original the later steps then drop out identically on
// both sides, so no verdict is wrong, but the coverage is narrower than eGov-anything's.
func runChainAgainst(client *http.Client, base string, steps []chainStep) []chainStepOutcome {
	runner := app.Runner{BaseURL: base, Client: client}
	name := ""
	if len(steps) > 0 {
		name = steps[0].Name
	}
	itemID := name
	out := make([]chainStepOutcome, 0, len(steps))
	for _, s := range steps {
		path, body := s.Path, s.Body
		if itemID != name {
			path = strings.ReplaceAll(path, name, itemID)
			body = strings.ReplaceAll(body, name, itemID)
		}
		o := chainStepOutcome{Method: strings.ToUpper(s.Method), Path: path}
		c := domain.AppCase{Kind: "http", Name: s.Name, Method: o.Method, Path: path, Body: body, RequestHeaders: s.RequestHeaders}
		got, err := runner.Do(c)
		if err != nil {
			o.Err = err.Error()
			out = append(out, o)
			continue
		}
		o.Status = got.Status
		o.Body = got.ResponseBody
		// The POST path holds the collection, not the resource, so only the response can reveal the
		// id the server chose; catch it once, before any later step needs it.
		if s.Kind == "post" && itemID == name {
			if id := extractJSONID(o.Body); id != "" && id != name {
				itemID = id
			}
		}
		out = append(out, o)
	}
	return out
}

// extractJSONID pulls the created resource's id out of a POST response: the top-level "id" field of an
// object, or the whole body when it is a bare scalar (a server that answers only the new id). Anything
// else yields "".
func extractJSONID(body string) string {
	trimmed := strings.TrimSpace(body)
	var m map[string]any
	if err := json.Unmarshal([]byte(trimmed), &m); err == nil {
		return scalarID(m["id"])
	}
	var scalar any
	if err := json.Unmarshal([]byte(trimmed), &scalar); err == nil {
		return scalarID(scalar)
	}
	return ""
}

// scalarID turns a JSON scalar into the id string: strings as they are, whole numbers without a
// decimal point, anything else not an id at all.
func scalarID(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case float64:
		if t == float64(int64(t)) {
			return strconv.FormatInt(int64(t), 10)
		}
		return strconv.FormatFloat(t, 'f', -1, 64)
	}
	return ""
}

// judgeChain compares the two transcripts step by step. A step count mismatch, a failed send on
// either side (an unset ${ENV} header variable fails a send too) or a 5xx from the original mean the
// run cannot judge the rewrite here: uncertain. A status difference or a body difference is the
// rewrite's fault: divergent, at the first differing step. Bodies compare through domain.EqualBody
// after each side's created id is masked, so id spellings never decide; paths are not compared at
// all, because the id replacement makes them differ on purpose.
func judgeChain(orig, rew []chainStepOutcome, opt domain.CompareOptions) chainVerdict {
	v := chainVerdict{FirstDivergence: -1}
	if len(orig) == 0 || len(orig) != len(rew) {
		v.Uncertain = true
		v.UncertainReason = fmt.Sprintf("transcript step count mismatch (%d vs %d)", len(orig), len(rew))
		return v
	}
	origID, rewID := chainSideID(orig), chainSideID(rew)
	for i := range orig {
		o, r := orig[i], rew[i]
		if o.Err != "" || r.Err != "" {
			v.Uncertain = true
			v.UncertainReason = fmt.Sprintf("transport failure on step %d (original: %q, rewrite: %q)", i, o.Err, r.Err)
			return v
		}
		if o.Status >= 500 {
			v.Uncertain = true
			v.UncertainReason = fmt.Sprintf("original returned %d on step %d", o.Status, i)
			return v
		}
		if o.Status != r.Status {
			v.Divergent = true
			v.FirstDivergence = i
			return v
		}
		if eq, _ := domain.EqualBody(chainMaskID(o.Body, origID), chainMaskID(r.Body, rewID), opt); !eq {
			v.Divergent = true
			v.FirstDivergence = i
			return v
		}
	}
	return v
}

// chainSideID reads the created id from a side's POST response, for masking before the comparison.
func chainSideID(outs []chainStepOutcome) string {
	for _, o := range outs {
		if o.Method == "POST" && o.Err == "" {
			return extractJSONID(o.Body)
		}
	}
	return ""
}

// chainMaskID replaces a side's created id with a fixed mask, so both sides compare as "<chain-id>".
func chainMaskID(body, id string) string {
	if id == "" {
		return body
	}
	return strings.ReplaceAll(body, id, "<chain-id>")
}

// chainLedgerFile is the per-target transcript file name. A collection name comes from recorded paths
// and can hold any character, so it is sanitized to [a-z0-9-], the chainName charset.
func chainLedgerFile(round int, tr chainTranscript) string {
	var b strings.Builder
	fmt.Fprintf(&b, "chain-round-%02d-", round)
	for _, r := range strings.ToLower(tr.Target) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			b.WriteRune(r)
			continue
		}
		b.WriteByte('-')
	}
	b.WriteString(".json")
	return b.String()
}

// writeChainRound writes a round's chain transcripts into the unit's checks directory: the summary
// file chain-round-NN.json holds the first diverging chain's transcript (the first target's when
// nothing diverged), and chain-round-NN-<target>.json holds every target's. All writes are atomic
// (temp file + rename), so a reader never sees half a transcript. The caller passes the checks
// directory and skips the call for rounds that ran no chain, so a no-option run writes nothing here.
func writeChainRound(dir string, round int, trs []chainTranscript) error {
	if len(trs) == 0 {
		return fmt.Errorf("checks: chain round %d has no transcript to record", round)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("checks: create %s: %w", dir, err)
	}
	pick := trs[0]
	for _, tr := range trs {
		if tr.Verdict.Divergent {
			pick = tr
			break
		}
	}
	roundPath := filepath.Join(dir, fmt.Sprintf("chain-round-%02d.json", round))
	if err := writeChainTranscriptFile(roundPath, pick); err != nil {
		return err
	}
	for _, tr := range trs {
		if err := writeChainTranscriptFile(filepath.Join(dir, chainLedgerFile(round, tr)), tr); err != nil {
			return err
		}
	}
	return nil
}

// writeChainTranscriptFile writes one transcript, atomically.
func writeChainTranscriptFile(path string, tr chainTranscript) error {
	data, err := json.MarshalIndent(tr, "", "  ")
	if err != nil {
		return fmt.Errorf("checks: encode chain transcript %s: %w", filepath.Base(path), err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o644); err != nil {
		return fmt.Errorf("checks: write %s: %w", filepath.Base(path), err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("checks: write %s: %w", filepath.Base(path), err)
	}
	return nil
}
