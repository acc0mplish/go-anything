package wiregen

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-anything/go-anything/internal/adapter/store"
	"github.com/go-anything/go-anything/internal/domain"
)

// concurrentRaceRewrite answers the recorded case exactly like the rewrite the RBAC tests use, except
// under concurrent load: a second request arriving while one is being answered gets a busy body with a
// process-wide sequence number. The sequential probes never see it, so the round agrees without the
// check and only the concurrent check can catch the divergence. The counter makes every racing answer
// different from the others, so a passing check cannot be a coincidence of two equal draws.
const concurrentRaceRewrite = `package orders

import (
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"time"
)

// enforceTenant is set when the test writes the unit: true enforces the owner tenant.
const enforceTenant = true

// inFlight counts the requests being answered; seq numbers the answers given while busy.
var inFlight, seq int32

func Handle(w http.ResponseWriter, r *http.Request) {
	if atomic.AddInt32(&inFlight, 1) > 1 {
		atomic.AddInt32(&inFlight, -1)
		http.Error(w, fmt.Sprintf("busy %d", atomic.AddInt32(&seq, 1)), http.StatusServiceUnavailable)
		return
	}
	time.Sleep(20 * time.Millisecond)
	atomic.AddInt32(&inFlight, -1)
	type who struct{ role, tenant string }
	tokens := map[string]who{
		"tok-admin-acme":   {"admin", "acme"},
		"tok-clerk-globex": {"clerk", "globex"},
	}
	c, ok := tokens[strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")]
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if r.URL.Path != "/orders/1" || (enforceTenant && c.tenant != "acme") {
		http.NotFound(w, r)
		return
	}
	w.Write([]byte("{\"id\":1}"))
}
`

// racingOriginal wraps the constant original so that concurrent requests reach it at once: the second
// and later overlapping request gets a busy status. The original is the oracle, so its own
// inconsistency makes the verdict uncertain — a rewrite defect must not be invented from it.
func racingOriginal(enforceTenant bool) http.HandlerFunc {
	var inFlight int32
	inner := rbacOrderService(enforceTenant)
	return func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&inFlight, 1) > 1 {
			atomic.AddInt32(&inFlight, -1)
			http.Error(w, "busy", http.StatusServiceUnavailable)
			return
		}
		time.Sleep(20 * time.Millisecond)
		atomic.AddInt32(&inFlight, -1)
		inner(w, r)
	}
}

// concRecording writes the one-case recording these tests refine against.
func concRecording(t *testing.T, casesPath string) {
	t.Helper()
	if err := store.SaveApp(casesPath, domain.AppRecording{Subject: "orders", Cases: []domain.AppCase{{
		Name: "get-order", Kind: "http", Method: "GET", Path: "/orders/1",
		Status: 401, ResponseBody: "unauthorized\n",
	}}}); err != nil {
		t.Fatal(err)
	}
}

// requireNarrowedNotChecked pins the not_checked contract of a round that ran the concurrent check: the
// identical-request line is narrowed, the distinct-request interleavings line stays unchecked.
func requireNarrowedNotChecked(t *testing.T, got []string) {
	t.Helper()
	if !slices.Contains(got, notCheckedConcurrentOn) {
		t.Fatalf("a round that ran the check must narrow the identical-request line: %q", got)
	}
	if slices.Contains(got, concurrentInterleavingsNote) {
		t.Fatalf("the un-narrowed concurrency line must be gone from a checked round: %q", got)
	}
}

func TestRefineConcurrentDivergenceBlocksConvergence(t *testing.T) {
	orig := httptest.NewServer(rbacOrderService(true))
	defer orig.Close()
	root := rbacModule(t, true)
	if err := os.WriteFile(filepath.Join(root, "units", "orders", "rewrite.go"), []byte(concurrentRaceRewrite), 0o644); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	casesPath := filepath.Join(dir, "app.json")
	concRecording(t, casesPath)
	var logBuf bytes.Buffer
	ok, err := Refine(RefineOptions{
		Root: root, Unit: "orders", Prefix: "/orders/", CasesPath: casesPath,
		OriginalURL: orig.URL, Rounds: 2, Probes: 4, MaxNew: 5, Seed: 1, Log: &logBuf,
		Concurrency: 4,
	})
	if ok || err == nil {
		t.Fatalf("a rewrite that races under concurrency must not converge: ok=%v err=%v\n%s", ok, err, logBuf.String())
	}
	if !strings.Contains(err.Error(), "answered identical concurrent requests differently") || !strings.Contains(err.Error(), "get-order") {
		t.Fatalf("the no-agent error must name the divergence and the cases, in its own words: %v", err)
	}
	if strings.Contains(err.Error(), "critical authorization finding") {
		t.Fatalf("a concurrent divergence must not borrow the authorization phrasing: %v", err)
	}
	check := readRoundCheck(t, filepath.Join(root, ".go-anything", "checks", "orders", "round-01.json"))
	if check.ConcurrentChecked != 1 || check.ConcurrentDiverges != 1 {
		t.Fatalf("the round must record one checked case and one divergence: %+v", check)
	}
	if len(check.ConcurrentPending) != 0 {
		t.Fatalf("a divergence is not a pending verdict: %+v", check)
	}
	if check.CleanStreak != 0 || check.Converged {
		t.Fatalf("the divergence must break the round: clean_streak=%d converged=%v", check.CleanStreak, check.Converged)
	}
	requireNarrowedNotChecked(t, check.NotChecked)
}

func TestRefineConcurrentPendingDoesNotBlockConvergence(t *testing.T) {
	// The original itself is inconsistent under concurrency, so there is no stable answer to judge the
	// rewrite against: the verdict is pending, the round stays clean, and the pending name stays on the
	// record and in the convergence summary instead of quietly disappearing.
	orig := httptest.NewServer(racingOriginal(true))
	defer orig.Close()
	root := rbacModule(t, true)
	dir := t.TempDir()
	casesPath := filepath.Join(dir, "app.json")
	concRecording(t, casesPath)
	var logBuf bytes.Buffer
	ok, err := Refine(RefineOptions{
		Root: root, Unit: "orders", Prefix: "/orders/", CasesPath: casesPath,
		OriginalURL: orig.URL, Rounds: 2, Probes: 4, MaxNew: 5, Seed: 1, Log: &logBuf,
		Concurrency: 4,
	})
	if err != nil || !ok {
		t.Fatalf("a pending verdict must not block convergence: ok=%v err=%v\n%s", ok, err, logBuf.String())
	}
	if !strings.Contains(logBuf.String(), "1 concurrent case(s) left pending") {
		t.Fatalf("the convergence summary must name the pending cases:\n%s", logBuf.String())
	}
	for _, r := range []string{"round-01.json", "round-02.json"} {
		check := readRoundCheck(t, filepath.Join(root, ".go-anything", "checks", "orders", r))
		if check.ConcurrentChecked != 1 || check.ConcurrentDiverges != 0 {
			t.Fatalf("%s must record one checked case with no divergence: %+v", r, check)
		}
		if !slices.Equal(check.ConcurrentPending, []string{"get-order"}) {
			t.Fatalf("%s must keep the pending case name: %+v", r, check.ConcurrentPending)
		}
		requireNarrowedNotChecked(t, check.NotChecked)
	}
}

// TestRefineChecksThatRunNothingKeepTheCommonNotChecked pins the evidence rule behind not_checked: an
// option that is on but checked nothing (no recorded case for the concurrent check, no chain target in
// the recording) narrows nothing — a narrowed line would claim coverage the round never exercised.
func TestRefineChecksThatRunNothingKeepTheCommonNotChecked(t *testing.T) {
	orig := httptest.NewServer(rbacOrderService(true))
	defer orig.Close()
	root := rbacModule(t, true)
	dir := t.TempDir()
	casesPath := filepath.Join(dir, "app.json")
	// A store refuses a recording with no cases at all, so the recording holds one case of another
	// prefix: the orders unit still has zero recorded cases to fire the checks at.
	other := domain.AppRecording{Subject: "orders", Cases: []domain.AppCase{
		{Name: "get-item", Kind: "http", Method: "GET", Path: "/items/1", Status: 200, ResponseBody: "{}"},
	}}
	if err := store.SaveApp(casesPath, other); err != nil {
		t.Fatal(err)
	}
	var logBuf bytes.Buffer
	ok, err := Refine(RefineOptions{
		Root: root, Unit: "orders", Prefix: "/orders/", CasesPath: casesPath,
		OriginalURL: orig.URL, Rounds: 2, Probes: 4, MaxNew: 5, Seed: 1, Log: &logBuf,
		Concurrency: 4, Chains: 2,
	})
	if err != nil || !ok {
		t.Fatalf("an agreeing rewrite must converge even with no recorded case: ok=%v err=%v\n%s", ok, err, logBuf.String())
	}
	for _, r := range []string{"round-01.json", "round-02.json"} {
		check := readRoundCheck(t, filepath.Join(root, ".go-anything", "checks", "orders", r))
		if check.ConcurrentChecked != 0 || check.ChainsChecked != 0 {
			t.Fatalf("%s ran no check, so it must record zero on both: %+v", r, check)
		}
		if slices.Contains(check.NotChecked, notCheckedConcurrentOn) || slices.Contains(check.NotChecked, notCheckedChainOn) {
			t.Fatalf("%s must not narrow a line for a check that ran nothing: %q", r, check.NotChecked)
		}
		if !slices.Contains(check.NotChecked, concurrentInterleavingsNote) || !slices.Contains(check.NotChecked, chainStateNote) {
			t.Fatalf("%s must keep the common lines: %q", r, check.NotChecked)
		}
	}
}

func TestRefineConcurrentRejectsCommandLineUnit(t *testing.T) {
	_, err := Refine(RefineOptions{
		Root: "unused", Unit: "orders", CasesPath: "unused.json", OriginalCLI: "orig",
		Concurrency: 4,
	})
	if err == nil || !strings.Contains(err.Error(), "HTTP units only") {
		t.Fatalf("the concurrent check on a command-line unit must be refused with a clear error: %v", err)
	}
}
