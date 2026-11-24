package wiregen

import (
	"fmt"
	"net/http"
	"sync"

	"github.com/go-anything/go-anything/internal/adapter/app"
	"github.com/go-anything/go-anything/internal/domain"
)

// The concurrent-request check ports the strength of the original tool's concurrency probe: the same
// recorded case is fired n times at once at the original and at the rewrite, and the verdicts say whether
// the rewrite keeps answering the same thing under concurrent load. The order the answers arrive in is
// not comparable (interleaving is nondeterministic), so the contract is consistency: each side must be
// uniform across its own n answers, and the two representatives must agree. Whether a divergence blocks
// convergence is decided in the refine gate (a go-anything extension); the verdicts themselves follow the
// ported rules:
//   - a send that failed on either side makes the verdict uncertain — infrastructure, not a rewrite defect;
//   - a non-uniform original is uncertain — the original itself is nondeterministic, so there is no
//     stable answer to judge the rewrite against;
//   - a non-uniform rewrite is diverge — under concurrency the rewrite answered the identical request
//     differently, which the sequential probes did not reach (a race or shared state);
//   - both uniform: the representatives must match, or the divergence is again diverge;
//   - a case whose status and body are both recorded unstable has no stable field to compare: skipped.
//
// A signature is an equivalence class, not a byte string: bodies compare through domain.EqualBody, so
// the check judges an answer by exactly the rule a recorded case gets (JSON semantics, number tolerance,
// masking) rather than by a second hand-written copy of it. Two JSON-semantic-equal bodies are one
// signature even when their bytes differ.
//
// Requests are never recorded into rec.Cases and response bodies never reach the round record, the HTML
// page or the brief: the verdicts carry counts and case names only, so no token or answer value leaks
// into a report.

// concurrentClient never follows a redirect: a redirect status is an answer to compare, not a page to
// chase, so the same request must produce the same answer on both sides whatever it is.
func concurrentClient() *http.Client {
	return &http.Client{
		Timeout: app.DefaultTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// ConcurrentVerdict is the outcome of the concurrent-request check for one case. It holds counts and the
// case name only: no request header value and no response body, so nothing sensitive can ride along into
// a round record or a report.
type ConcurrentVerdict struct {
	Case         string `json:"case"`
	Verdict      string `json:"verdict"` // "match", "diverge", "uncertain" or "skipped"
	Repeats      int    `json:"repeats"`
	OrigDistinct int    `json:"orig_distinct"`
	RewDistinct  int    `json:"rew_distinct"`
	OrigErrors   int    `json:"orig_errors,omitempty"`
	RewErrors    int    `json:"rew_errors,omitempty"`
	Note         string `json:"note,omitempty"`
}

// concurrentShot is one send of one case: what the server answered, or why nothing arrived.
type concurrentShot struct {
	status int
	body   string
	err    error
}

// ProbeConcurrent fires case n times at once at each base URL and returns the verdict. The bases come
// from the caller, so a test can point them at httptest servers and the refine gate stays the only place
// that starts the rewrite. The sends use app.Runner, so the request a case gets here is built exactly
// like every other request in the tool (header ${ENV} expansion, default Content-Type); a header naming
// an unset environment variable fails that send, and a failed send makes the verdict uncertain.
func ProbeConcurrent(originalBase, rewriteBase string, c domain.AppCase, n int, cmp domain.CompareOptions) ConcurrentVerdict {
	v := ConcurrentVerdict{Case: c.Name, Verdict: "skipped", Repeats: n}
	statusUnstable, bodyUnstable := unstableField(c, "status"), unstableField(c, "body")
	if statusUnstable && bodyUnstable {
		v.Note = "the case recorded both status and body as unstable, so nothing here is comparable"
		return v
	}
	orig, rew := fireConcurrent(originalBase, c, n), fireConcurrent(rewriteBase, c, n)
	v.OrigErrors, v.RewErrors = countShotErrors(orig), countShotErrors(rew)
	v.OrigDistinct = distinctShots(orig, statusUnstable, bodyUnstable, cmp)
	v.RewDistinct = distinctShots(rew, statusUnstable, bodyUnstable, cmp)
	switch {
	case v.OrigErrors > 0 || v.RewErrors > 0:
		v.Verdict, v.Note = "uncertain", "a send failed, so the two sides may not have felt the same load"
	case v.OrigDistinct > 1:
		v.Verdict, v.Note = "uncertain", "the original answered identical concurrent requests differently, so there is no stable answer to judge against"
	case v.RewDistinct > 1:
		v.Verdict, v.Note = "diverge", "the rewrite answered identical concurrent requests differently (a race or shared state the sequential probes did not reach)"
	default:
		o, r := firstDelivered(orig), firstDelivered(rew)
		if o == nil || r == nil {
			// Unreachable: no error on either side means both have a delivered shot. Kept explicit so a
			// refactor cannot turn it into a panic.
			v.Verdict, v.Note = "uncertain", "no delivered answer to compare"
			return v
		}
		if !sameSignature(*o, *r, statusUnstable, bodyUnstable, cmp) {
			v.Verdict, v.Note = "diverge", "uniform on each side, but under concurrent load the rewrite answers differently from the original"
			return v
		}
		v.Verdict = "match"
	}
	return v
}

// ConcurrentVerdicts runs the concurrent-request check for every case and refuses to trust the answers
// once the program behind the rewrite has exited: after the exit, any listener on the address belongs to
// someone else (issue #17), so what answered is not the rewrite.
func ConcurrentVerdicts(originalBase, rewriteBase string, cases []domain.AppCase, n int, cmp domain.CompareOptions, exited <-chan struct{}) ([]ConcurrentVerdict, error) {
	var out []ConcurrentVerdict
	for _, c := range cases {
		select {
		case <-exited:
			return nil, fmt.Errorf("concurrency: the program exited before case %s was asked, so its answers do not come from the rewrite", c.Name)
		default:
		}
		out = append(out, ProbeConcurrent(originalBase, rewriteBase, c, n, cmp))
	}
	select {
	case <-exited:
		return nil, fmt.Errorf("concurrency: the program exited during the concurrent check, so its answers do not come from the rewrite")
	default:
	}
	return out, nil
}

// fireConcurrent sends case n times at once and returns the n shots in slot order.
func fireConcurrent(base string, c domain.AppCase, n int) []concurrentShot {
	shots := make([]concurrentShot, n)
	var wg sync.WaitGroup
	runner := app.Runner{BaseURL: base, Client: concurrentClient()}
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			got, err := runner.Do(c)
			if err != nil {
				shots[i] = concurrentShot{err: err}
				return
			}
			shots[i] = concurrentShot{status: got.Status, body: got.ResponseBody}
		}(i)
	}
	wg.Wait()
	return shots
}

// unstableField reports whether the case recorded the named answer field as unstable.
func unstableField(c domain.AppCase, what string) bool {
	for _, u := range c.Unstable {
		if u == what {
			return true
		}
	}
	return false
}

// sameSignature reports whether two delivered answers carry the same signature: the status and the body,
// each left out of the comparison when the case recorded it as unstable. The body compares through
// domain.EqualBody, so the comparison is the tool's own answer rule, not a second copy of it.
//
// Recorded limit, not a defect: with FloatTolerance > 0 this pairwise equality is not transitive (a
// within tolerance of b and b of c does not make a within tolerance of c), so the distinct-class count
// can depend on the order the shots are walked in. The default tolerance of 0 is exact, and the slack
// only over-counts classes — it errs toward a false mismatch, never a false pass.
func sameSignature(a, b concurrentShot, statusUnstable, bodyUnstable bool, cmp domain.CompareOptions) bool {
	if !statusUnstable && a.status != b.status {
		return false
	}
	if bodyUnstable {
		return true
	}
	eq, _ := domain.EqualBody(a.body, b.body, cmp)
	return eq
}

// distinctShots counts the signature classes one side produced among its delivered answers. The pairwise
// walk keeps the counting on the same comparison rule the verdict uses; n is small (the -concurrency
// value), so the quadratic cost is nothing next to the network sends.
func distinctShots(shots []concurrentShot, statusUnstable, bodyUnstable bool, cmp domain.CompareOptions) int {
	kinds := 0
	for i, s := range shots {
		if s.err != nil {
			continue
		}
		novel := true
		for _, prev := range shots[:i] {
			if prev.err == nil && sameSignature(s, prev, statusUnstable, bodyUnstable, cmp) {
				novel = false
				break
			}
		}
		if novel {
			kinds++
		}
	}
	return kinds
}

// countShotErrors counts the sends that delivered nothing.
func countShotErrors(shots []concurrentShot) int {
	n := 0
	for _, s := range shots {
		if s.err != nil {
			n++
		}
	}
	return n
}

// firstDelivered returns the first shot that got an answer, or nil when none did.
func firstDelivered(shots []concurrentShot) *concurrentShot {
	for _, s := range shots {
		if s.err == nil {
			return &s
		}
	}
	return nil
}

// divergeNames lists the cases the rewrite answered inconsistently, for the log, the agent's brief and
// the no-agent error.
func divergeNames(vs []ConcurrentVerdict) []string {
	var out []string
	for _, v := range vs {
		if v.Verdict == "diverge" {
			out = append(out, v.Case)
		}
	}
	return out
}
