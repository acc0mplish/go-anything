package wiregen

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/go-anything/go-anything/internal/domain"
)

// concCase is a recorded GET whose answer one body string covers. The bases the verdict gets point at
// httptest servers, so these tests never build or start a program.
func concCase() domain.AppCase {
	return domain.AppCase{Name: "get-order", Kind: "http", Method: "GET", Path: "/orders/1"}
}

// concServer answers every request with body, or with a process-wide counter in the body when racing is
// set: then identical concurrent requests get different answers, which is the defect the check hunts.
func concServer(body string, racing bool) *httptest.Server {
	var mu sync.Mutex
	n := 0
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b := body
		if racing {
			mu.Lock()
			n++
			b = fmt.Sprintf("%s-%d", body, n)
			mu.Unlock()
		}
		w.Write([]byte(b))
	}))
}

func TestProbeConcurrentUniformSidesMatch(t *testing.T) {
	orig := concServer("{\"id\":1}", false)
	rew := concServer("{\"id\":1}", false)
	defer orig.Close()
	defer rew.Close()
	v := ProbeConcurrent(orig.URL, rew.URL, concCase(), 4, domain.CompareOptions{})
	if v.Verdict != "match" {
		t.Fatalf("two sides that answer identically must match: %+v", v)
	}
	if v.OrigDistinct != 1 || v.RewDistinct != 1 || v.OrigErrors != 0 || v.RewErrors != 0 {
		t.Fatalf("a match must be uniform on both sides with no send error: %+v", v)
	}
}

func TestProbeConcurrentUniformButDifferentAnswersDiverge(t *testing.T) {
	orig := concServer("{\"id\":1}", false)
	rew := concServer("{\"id\":2}", false)
	defer orig.Close()
	defer rew.Close()
	v := ProbeConcurrent(orig.URL, rew.URL, concCase(), 4, domain.CompareOptions{})
	if v.Verdict != "diverge" || v.RewDistinct != 1 {
		t.Fatalf("uniform sides that answer differently must diverge (systematic, not a race): %+v", v)
	}
}

func TestProbeConcurrentRewriteSideRacingDiverges(t *testing.T) {
	orig := concServer("{\"id\":1}", false)
	rew := concServer("{\"id\":1}", true)
	defer orig.Close()
	defer rew.Close()
	v := ProbeConcurrent(orig.URL, rew.URL, concCase(), 6, domain.CompareOptions{})
	if v.Verdict != "diverge" || v.RewDistinct < 2 {
		t.Fatalf("a rewrite that answers identical concurrent requests differently must diverge: %+v", v)
	}
	if v.OrigDistinct != 1 {
		t.Fatalf("the original stays the oracle and must be uniform: %+v", v)
	}
}

func TestProbeConcurrentOriginalSideRacingUncertain(t *testing.T) {
	orig := concServer("{\"id\":1}", true)
	rew := concServer("{\"id\":1}", false)
	defer orig.Close()
	defer rew.Close()
	v := ProbeConcurrent(orig.URL, rew.URL, concCase(), 6, domain.CompareOptions{})
	if v.Verdict != "uncertain" || v.OrigDistinct < 2 {
		t.Fatalf("a nondeterministic original must leave the verdict uncertain: %+v", v)
	}
}

func TestProbeConcurrentFailedSendUncertain(t *testing.T) {
	// The original is closed before the check: every send to it fails, and a verdict that came out of
	// half-failed load must never be read as a rewrite defect.
	orig := concServer("{\"id\":1}", false)
	rew := concServer("{\"id\":1}", false)
	orig.Close()
	defer rew.Close()
	v := ProbeConcurrent(orig.URL, rew.URL, concCase(), 4, domain.CompareOptions{})
	if v.Verdict != "uncertain" || v.OrigErrors == 0 {
		t.Fatalf("a failed send must make the verdict uncertain, not a divergence: %+v", v)
	}
}

func TestProbeConcurrentUnsetHeaderEnvUncertain(t *testing.T) {
	// The Runner contract turns a header naming an unset environment variable into a send failure, and a
	// failed send is an uncertain verdict, never an answer.
	c := concCase()
	c.RequestHeaders = map[string]string{"Authorization": "Bearer ${GA_CONCURRENT_UNSET_TOKEN}"}
	orig := concServer("{\"id\":1}", false)
	rew := concServer("{\"id\":1}", false)
	defer orig.Close()
	defer rew.Close()
	v := ProbeConcurrent(orig.URL, rew.URL, c, 2, domain.CompareOptions{})
	if v.Verdict != "uncertain" || v.RewErrors == 0 {
		t.Fatalf("a header with an unset variable must fail the send and leave the verdict uncertain: %+v", v)
	}
}

func TestProbeConcurrentNothingStableSkipped(t *testing.T) {
	orig := concServer("{\"id\":1}", true)
	rew := concServer("{\"id\":2}", true)
	defer orig.Close()
	defer rew.Close()
	c := concCase()
	c.Unstable = []string{"status", "body"}
	v := ProbeConcurrent(orig.URL, rew.URL, c, 4, domain.CompareOptions{})
	if v.Verdict != "skipped" {
		t.Fatalf("a case with no stable field has nothing to compare and must be skipped: %+v", v)
	}
}

func TestProbeConcurrentUnstableStatusStillComparesBody(t *testing.T) {
	// status is unstable, body is not: the status leaves the signature, the body still judges.
	orig := concServer("same", false)
	rew := concServer("same", false)
	defer orig.Close()
	defer rew.Close()
	c := concCase()
	c.Unstable = []string{"status"}
	v := ProbeConcurrent(orig.URL, rew.URL, c, 3, domain.CompareOptions{})
	if v.Verdict != "match" {
		t.Fatalf("unstable status alone must not block a match on the body: %+v", v)
	}
}

func TestProbeConcurrentMaskingAppliesToSignatures(t *testing.T) {
	// Each side names a different uuid. Without masking the signatures differ (diverge); the normalizer
	// must mask both sides before they compare, so the volatile value does not read as a divergence.
	orig := concServer("{\"id\":\"11111111-1111-1111-1111-111111111111\"}", false)
	rew := concServer("{\"id\":\"22222222-2222-2222-2222-222222222222\"}", false)
	defer orig.Close()
	defer rew.Close()
	c := concCase()
	unmasked := ProbeConcurrent(orig.URL, rew.URL, c, 3, domain.CompareOptions{})
	if unmasked.Verdict != "diverge" {
		t.Fatalf("without masking two different bodies must diverge: %+v", unmasked)
	}
	masked := ProbeConcurrent(orig.URL, rew.URL, c, 3, domain.CompareOptions{Normalize: []string{"uuids"}})
	if masked.Verdict != "match" {
		t.Fatalf("the compare options (masking) must apply to the signature comparison: %+v", masked)
	}
}

func TestConcurrentVerdictsRefusesToTrustAnExitedProgram(t *testing.T) {
	orig := concServer("{\"id\":1}", false)
	rew := concServer("{\"id\":1}", false)
	defer orig.Close()
	defer rew.Close()
	exited := make(chan struct{})
	close(exited)
	if _, err := ConcurrentVerdicts(orig.URL, rew.URL, []domain.AppCase{concCase()}, 2, domain.CompareOptions{}, exited); err == nil {
		t.Fatal("verdicts collected from a program that already exited must be refused, not returned")
	}
}
