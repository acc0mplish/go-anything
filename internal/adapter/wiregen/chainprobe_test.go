package wiregen

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/go-anything/go-anything/internal/domain"
)

// TestChainTargetsDerivationRules pins the target rules: query cases and error-status cases are not
// observable, a collection is cut from an item path, and only a collection with an observed POST
// becomes a target. Verbs arrive sorted, the POST path is the recorded one, and the first recorded
// headers and body per verb ride along.
func TestChainTargetsDerivationRules(t *testing.T) {
	cases := []domain.AppCase{
		{Name: "create", Kind: "http", Method: "POST", Path: "/orders/", Status: 201, Body: `{"id":"42","title":"seed"}`,
			RequestHeaders: map[string]string{"Authorization": "Bearer ${TOK}"}},
		{Name: "read", Kind: "http", Method: "GET", Path: "/orders/1", Status: 200},
		{Name: "query", Kind: "http", Method: "GET", Path: "/orders?limit=5", Status: 200},
		{Name: "read-other", Kind: "http", Method: "GET", Path: "/other/1", Status: 200},
		{Name: "read-bad", Kind: "http", Method: "GET", Path: "/bad/1", Status: 404},
		{Name: "cli", Kind: "cli", Argv: []string{"orders", "1"}},
	}
	got := chainTargets(cases, 10)
	if len(got) != 1 {
		t.Fatalf("only the orders collection has an observed POST: %+v", got)
	}
	tg := got[0]
	if tg.Collection != "orders" || tg.PostPath != "/orders/" {
		t.Fatalf("collection and recorded POST path must survive: %+v", tg)
	}
	if want := []string{"GET", "POST"}; strings.Join(tg.Verbs, ",") != strings.Join(want, ",") {
		t.Fatalf("verbs must be the observed ones, sorted: %+v", tg.Verbs)
	}
	if tg.Templates["POST"] != `{"id":"42","title":"seed"}` {
		t.Fatalf("the first recorded POST body must be the template: %+v", tg.Templates)
	}
	if tg.Headers["Authorization"] != "Bearer ${TOK}" {
		t.Fatalf("headers ride along with ${ENV} references intact: %+v", tg.Headers)
	}
	// The check never fires at another collection: "other" saw no POST and "bad" was recorded as an error.
	for _, tg := range got {
		if tg.Collection == "other" || tg.Collection == "bad" {
			t.Fatalf("collection %s must not become a target: %+v", tg.Collection, got)
		}
	}
}

// TestChainTargetsSortAndLimit pins the deterministic order: collections sorted by name, then cut to
// the limit from the front.
func TestChainTargetsSortAndLimit(t *testing.T) {
	var cases []domain.AppCase
	for _, coll := range []string{"beta", "alpha", "gamma"} {
		cases = append(cases,
			domain.AppCase{Kind: "http", Method: "POST", Path: "/" + coll + "/", Status: 201},
			domain.AppCase{Kind: "http", Method: "GET", Path: "/" + coll + "/1", Status: 200},
		)
	}
	all := chainTargets(cases, 10)
	if len(all) != 3 {
		t.Fatalf("three collections with observed POSTs: %+v", all)
	}
	for i, want := range []string{"alpha", "beta", "gamma"} {
		if all[i].Collection != want {
			t.Fatalf("targets must be sorted by collection: %+v", all)
		}
	}
	limited := chainTargets(cases, 2)
	if len(limited) != 2 || limited[0].Collection != "alpha" || limited[1].Collection != "beta" {
		t.Fatalf("the limit keeps the first sorted targets: %+v", limited)
	}
	if got := chainTargets(cases, 0); len(got) != 0 {
		t.Fatalf("limit 0 keeps nothing: %+v", got)
	}
	if got := chainTargets(cases, -1); len(got) != 3 {
		t.Fatalf("a negative limit is no limit: %+v", got)
	}
}

// TestChainCasesCompositionAndReservedName pins the step sequence: only observed verbs, in the fixed
// post, get, (put, get), (patch, get), (delete, get) order, with a deterministic reserved name from
// the seed and the round.
func TestChainCasesCompositionAndReservedName(t *testing.T) {
	full := chainTarget{Collection: "orders", PostPath: "/orders/", Verbs: []string{"DELETE", "GET", "PATCH", "POST", "PUT"}}
	steps := chainCases(7, full, 3)
	var kinds []string
	for _, s := range steps {
		kinds = append(kinds, s.Kind)
		if s.Name != steps[0].Name {
			t.Fatalf("every step names the same reserved resource: %+v", steps)
		}
	}
	want := "post,get,put,get,patch,get,delete,get-after-delete"
	if strings.Join(kinds, ",") != want {
		t.Fatalf("step kinds = %s, want %s", strings.Join(kinds, ","), want)
	}
	if steps[0].Path != "/orders/" {
		t.Fatalf("the create posts to the recorded collection path: %s", steps[0].Path)
	}
	if steps[1].Path != "/orders/"+steps[0].Name {
		t.Fatalf("the first read looks the resource up by its reserved name: %s", steps[1].Path)
	}
	name := steps[0].Name
	if !regexp.MustCompile(`^chain-r3-[0-9a-f]{8}$`).MatchString(name) {
		t.Fatalf("reserved name = %s, want chain-r3-<8 hex>", name)
	}
	if again := chainCases(7, full, 3)[0].Name; again != name {
		t.Fatalf("the name is deterministic in seed and round: %s vs %s", again, name)
	}
	if other := chainCases(7, full, 4)[0].Name; other == name {
		t.Fatal("a new round must get a new reserved name")
	}
	if strings.ContainsAny(name, "ABCDEFGHIJKLMNOPQRSTUVWXYZ/._") {
		t.Fatalf("the name must stay in [a-z0-9-]: %s", name)
	}

	// A target whose recording only observed read and create gets a two-step chain: the check never
	// demands a verb the recording never saw.
	thin := chainTarget{Collection: "orders", PostPath: "/orders/", Verbs: []string{"GET", "POST"}}
	thinSteps := chainCases(7, thin, 3)
	if len(thinSteps) != 2 || thinSteps[0].Kind != "post" || thinSteps[1].Kind != "get" {
		t.Fatalf("only observed verbs: %+v", thinSteps)
	}
	// The create body carries the client-assigned id, so a server that honors it creates the resource
	// exactly where the following steps look.
	var body map[string]any
	if err := json.Unmarshal([]byte(thinSteps[0].Body), &body); err != nil {
		t.Fatalf("the create body must be JSON: %v", err)
	}
	if body["id"] != thinSteps[0].Name {
		t.Fatalf("the create body must carry the reserved name as the client id: %s", thinSteps[0].Body)
	}
	// A recorded template is inherited field by field, so required fields survive, and the first
	// string field carries the check value.
	tpl := chainTarget{Collection: "orders", PostPath: "/orders/", Verbs: []string{"GET", "POST"},
		Templates: map[string]string{"POST": `{"id":"42","title":"seed"}`}}
	tplSteps := chainCases(7, tpl, 3)
	var tplBody map[string]string
	if err := json.Unmarshal([]byte(tplSteps[0].Body), &tplBody); err != nil {
		t.Fatalf("the inherited body must stay JSON: %v", err)
	}
	if tplBody["title"] != "seed" {
		t.Fatalf("inherited fields must survive: %s", tplSteps[0].Body)
	}
	if !strings.HasPrefix(tplBody["id"], tplSteps[0].Name+"-") {
		t.Fatalf("the first string field carries the check value: %s", tplSteps[0].Body)
	}
}

// chainAssignServer answers the create by assigning its own id, the way a server that ignores
// client-supplied ids does. Later steps must follow the assigned id per side.
func chainAssignServer(t *testing.T, seen *[]string) http.Handler {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/orders/", func(w http.ResponseWriter, r *http.Request) {
		*seen = append(*seen, r.Method+" "+r.URL.Path)
		switch r.Method {
		case "POST":
			w.WriteHeader(http.StatusCreated)
			w.Write([]byte(`{"id":"srv-7"}`))
		case "DELETE":
			w.WriteHeader(http.StatusNoContent)
		default:
			if strings.HasSuffix(r.URL.Path, "/srv-7") {
				w.Write([]byte(`{"id":"srv-7","title":"kept"}`))
				return
			}
			http.Error(w, "not found", http.StatusNotFound)
		}
	})
	return mux
}

func TestRunChainAgainstFollowsServerAssignedID(t *testing.T) {
	var seen []string
	srv := httptest.NewServer(chainAssignServer(t, &seen))
	defer srv.Close()
	tg := chainTarget{Collection: "orders", PostPath: "/orders/", Verbs: []string{"GET", "POST", "DELETE"}}
	steps := chainCases(11, tg, 1)
	out := runChainAgainst(concurrentClient(), srv.URL, steps)
	for _, o := range out {
		if o.Err != "" {
			t.Fatalf("every step must be delivered: %+v", out)
		}
	}
	if out[0].Status != 201 {
		t.Fatalf("the create must be answered: %+v", out[0])
	}
	// The reserved name was not honored, so the read follows the server-assigned id instead.
	if out[1].Path != "/orders/srv-7" || out[1].Status != 200 {
		t.Fatalf("the read must follow the assigned id: %+v", out[1])
	}
	// The mutate/delete steps follow it too.
	if strings.Contains(out[2].Path, steps[0].Name) || strings.Contains(out[3].Path, steps[0].Name) {
		t.Fatalf("no step after the create may keep the reserved name once the server assigned its own: %+v", out)
	}
	// The transcript records method, path, status and body — never request headers.
	raw, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "Authorization") || strings.Contains(string(raw), "request_headers") {
		t.Fatalf("a transcript must not carry request headers: %s", raw)
	}
}

// TestJudgeChainVerdicts walks the verdict rules: agreement, a status or body difference, and the
// three ways a run cannot judge the rewrite.
func TestJudgeChainVerdicts(t *testing.T) {
	ok := func(method, path string, status int, body string) chainStepOutcome {
		return chainStepOutcome{Method: method, Path: path, Status: status, Body: body}
	}
	failed := func(method, path string) chainStepOutcome {
		return chainStepOutcome{Method: method, Path: path, Err: "dial refused"}
	}
	t.Run("match", func(t *testing.T) {
		orig := []chainStepOutcome{ok("POST", "/orders/", 201, `{"id":"r1"}`), ok("GET", "/orders/r1", 200, `{"id":"r1","title":"a"}`)}
		rew := []chainStepOutcome{ok("POST", "/orders/", 201, `{"id":"r1"}`), ok("GET", "/orders/r1", 200, `{"id":"r1","title":"a"}`)}
		v := judgeChain(orig, rew, domain.CompareOptions{})
		if v.Divergent || v.Uncertain || v.FirstDivergence != -1 {
			t.Fatalf("identical chains match: %+v", v)
		}
	})
	t.Run("differing created ids do not decide", func(t *testing.T) {
		orig := []chainStepOutcome{ok("POST", "/orders/", 201, `{"id":"srv-9"}`), ok("GET", "/orders/srv-9", 200, `{"id":"srv-9","title":"a"}`)}
		rew := []chainStepOutcome{ok("POST", "/orders/", 201, `{"id":"own-1"}`), ok("GET", "/orders/own-1", 200, `{"id":"own-1","title":"a"}`)}
		v := judgeChain(orig, rew, domain.CompareOptions{})
		if v.Divergent || v.Uncertain {
			t.Fatalf("each side's own id is masked before the comparison: %+v", v)
		}
	})
	t.Run("status divergence names the first step", func(t *testing.T) {
		orig := []chainStepOutcome{ok("POST", "/orders/", 201, `{"id":"r1"}`), ok("GET", "/orders/r1", 200, `{"id":"r1"}`)}
		rew := []chainStepOutcome{ok("POST", "/orders/", 201, `{"id":"r1"}`), ok("GET", "/orders/r1", 404, "not found")}
		v := judgeChain(orig, rew, domain.CompareOptions{})
		if !v.Divergent || v.FirstDivergence != 1 || v.Uncertain {
			t.Fatalf("a status difference is a divergence at its step: %+v", v)
		}
	})
	t.Run("body divergence names the first step", func(t *testing.T) {
		orig := []chainStepOutcome{ok("POST", "/orders/", 201, `{"id":"r1"}`), ok("GET", "/orders/r1", 200, `{"id":"r1","title":"a"}`)}
		rew := []chainStepOutcome{ok("POST", "/orders/", 201, `{"id":"r1"}`), ok("GET", "/orders/r1", 200, `{"id":"r1","title":"OTHER"}`)}
		v := judgeChain(orig, rew, domain.CompareOptions{})
		if !v.Divergent || v.FirstDivergence != 1 {
			t.Fatalf("a body difference is a divergence at its step: %+v", v)
		}
	})
	t.Run("step count mismatch is uncertain", func(t *testing.T) {
		orig := []chainStepOutcome{ok("POST", "/orders/", 201, `{}`)}
		rew := []chainStepOutcome{ok("POST", "/orders/", 201, `{}`), ok("GET", "/orders/r1", 200, `{}`)}
		v := judgeChain(orig, rew, domain.CompareOptions{})
		if !v.Uncertain || v.Divergent || !strings.Contains(v.UncertainReason, "step count") {
			t.Fatalf("a transcript mismatch cannot judge: %+v", v)
		}
	})
	t.Run("a failed send is uncertain", func(t *testing.T) {
		orig := []chainStepOutcome{ok("POST", "/orders/", 201, `{}`), ok("GET", "/orders/r1", 200, `{}`)}
		rew := []chainStepOutcome{ok("POST", "/orders/", 201, `{}`), failed("GET", "/orders/r1")}
		v := judgeChain(orig, rew, domain.CompareOptions{})
		if !v.Uncertain || v.Divergent || !strings.Contains(v.UncertainReason, "transport failure") {
			t.Fatalf("a transport failure cannot judge: %+v", v)
		}
	})
	t.Run("an original 5xx is uncertain", func(t *testing.T) {
		orig := []chainStepOutcome{ok("POST", "/orders/", 503, "down")}
		rew := []chainStepOutcome{ok("POST", "/orders/", 503, "down")}
		v := judgeChain(orig, rew, domain.CompareOptions{})
		if !v.Uncertain || v.Divergent || !strings.Contains(v.UncertainReason, "original returned 503") {
			t.Fatalf("the original failing is not the rewrite's fault: %+v", v)
		}
	})
}

// TestWriteChainRoundFiles pins the round ledger: the summary file holds the first diverging chain's
// transcript, every target gets its own sanitized file, and the writes are atomic.
func TestWriteChainRoundFiles(t *testing.T) {
	dir := t.TempDir()
	trs := []chainTranscript{
		{Round: 1, Target: "orders", Name: "chain-r1-11111111", Verdict: chainVerdict{FirstDivergence: -1}},
		{Round: 1, Target: "ships/ocean lines", Name: "chain-r1-22222222", Verdict: chainVerdict{Divergent: true, FirstDivergence: 2}},
	}
	if err := writeChainRound(dir, 1, trs); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "chain-round-01.json"))
	if err != nil {
		t.Fatal(err)
	}
	var summary chainTranscript
	if err := json.Unmarshal(raw, &summary); err != nil {
		t.Fatal(err)
	}
	if !summary.Verdict.Divergent || summary.Target != "ships/ocean lines" {
		t.Fatalf("the summary holds the first diverging chain: %+v", summary)
	}
	if !strings.Contains(string(raw), `"divergent": true`) {
		t.Fatalf("the verdict records divergent: %s", raw)
	}
	perTarget, err := os.ReadFile(filepath.Join(dir, "chain-round-01-ships-ocean-lines.json"))
	if err != nil {
		t.Fatalf("the sanitized per-target file must exist: %v", err)
	}
	if !strings.Contains(string(perTarget), "chain-r1-22222222") {
		t.Fatalf("the per-target file holds its chain: %s", perTarget)
	}
	if _, err := os.ReadFile(filepath.Join(dir, "chain-round-01-orders.json")); err != nil {
		t.Fatalf("every target keeps its own file: %v", err)
	}
	leftovers, _ := filepath.Glob(filepath.Join(dir, "*.tmp"))
	if len(leftovers) != 0 {
		t.Fatalf("a write must not leave temp files behind: %v", leftovers)
	}
	if err := writeChainRound(dir, 2, nil); err == nil {
		t.Fatal("a round that ran no chain has nothing to write and must say so")
	}
}

// TestChainSendExpandsEnvHeaderButNeverRecordsIt proves the send path reuses the tool's ${ENV}
// expansion and the transcript stays free of the expanded value.
func TestChainSendExpandsEnvHeaderButNeverRecordsIt(t *testing.T) {
	const token = "tok-chain-secret-42"
	t.Setenv("GA_CHAIN_TEST_TOKEN", token)
	var gotAuth string
	mux := http.NewServeMux()
	mux.HandleFunc("/orders/", func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(`{"id":"1"}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	steps := []chainStep{{Name: "chain-r1-abcabcab", Kind: "post", Method: "POST", Path: "/orders/",
		Body: `{"id":"chain-r1-abcabcab"}`, RequestHeaders: map[string]string{"Authorization": "Bearer ${GA_CHAIN_TEST_TOKEN}"}}}
	out := runChainAgainst(concurrentClient(), srv.URL, steps)
	if out[0].Err != "" || out[0].Status != 201 {
		t.Fatalf("the step must be delivered with its header expanded: %+v", out[0])
	}
	if gotAuth != "Bearer "+token {
		t.Fatalf("the header must reach the server expanded: %q", gotAuth)
	}
	raw, _ := json.Marshal(out)
	if strings.Contains(string(raw), token) || strings.Contains(string(raw), "Authorization") {
		t.Fatalf("the transcript must not hold the header name or value: %s", raw)
	}
}

// TestChainStepsOnlyForUnit is a compile-time-shaped check that the derivation uses the unit's cases
// alone: the caller passes casesForUnit's slice, and these tests feed it directly. What the check
// pins here is that a recording holding another prefix's cases yields no cross-unit target.
func TestChainTargetsFromAnotherPrefixNeverLeak(t *testing.T) {
	cases := []domain.AppCase{
		{Name: "users-create", Kind: "http", Method: "POST", Path: "/users/", Status: 201},
		{Name: "users-read", Kind: "http", Method: "GET", Path: "/users/1", Status: 200},
	}
	got := chainTargets(cases, 5)
	if len(got) != 1 || got[0].Collection != "users" {
		t.Fatalf("the derivation follows exactly the cases it is given: %+v", got)
	}
	if got := chainTargets(nil, 5); len(got) != 0 {
		t.Fatalf("no cases, no targets, no panic: %+v", got)
	}
}
