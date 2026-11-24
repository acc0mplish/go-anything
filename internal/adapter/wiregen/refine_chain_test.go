package wiregen

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/go-anything/go-anything/internal/adapter/store"
	"github.com/go-anything/go-anything/internal/domain"
)

// chainStore is the original in these tests: an in-memory CRUD collection that really creates, reads,
// updates and deletes. It is served behind a ServeMux exactly like the rewrite app mounts its units,
// so path cleaning and redirects behave the same on both sides and a divergence can only be behavior,
// never routing (the PR2a finding: a Python stub reproduced Go routing artifacts as fake divergences).
type chainStore struct {
	mu    sync.Mutex
	items map[string]string
}

func (s *chainStore) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/orders/", s.serve)
	return mux
}

func (s *chainStore) serve(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := strings.Trim(strings.TrimPrefix(r.URL.Path, "/orders/"), "/")
	var body struct{ ID, Title string }
	if data, err := io.ReadAll(r.Body); err == nil {
		_ = json.Unmarshal(data, &body)
	}
	switch {
	case r.Method == "POST" && r.URL.Path == "/orders/":
		if body.ID != "" {
			id = body.ID
		} else {
			id = fmt.Sprintf("srv-%d", len(s.items)+1)
		}
		s.items[id] = body.Title
		w.WriteHeader(http.StatusCreated)
		io.WriteString(w, chainItemJSON(id, body.Title))
	case r.Method == "PUT" && id != "":
		s.items[id] = body.Title
		io.WriteString(w, chainItemJSON(id, body.Title))
	case r.Method == "DELETE" && id != "":
		delete(s.items, id)
		w.WriteHeader(http.StatusNoContent)
	case r.Method == "GET":
		title, ok := s.items[id]
		if !ok {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		io.WriteString(w, chainItemJSON(id, title))
	default:
		http.Error(w, "not found", http.StatusNotFound)
	}
}

func chainItemJSON(id, title string) string {
	b, _ := json.Marshal(struct {
		ID    string `json:"id"`
		Title string `json:"title"`
	}{ID: id, Title: title})
	return string(b)
}

// chainOriginalWithFailingCreate is the same original with a create that always 503s: there is no
// stable answer to judge the rewrite against, so every chain stays unjudgeable.
func chainOriginalWithFailingCreate(inner http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" && strings.HasPrefix(r.URL.Path, "/orders/") {
			http.Error(w, "down", http.StatusServiceUnavailable)
			return
		}
		inner.ServeHTTP(w, r)
	})
}

// chainReplayRewrite replays the recording verbatim: the four answers it holds are returned for the
// recorded method and path, everything else gets the same fixed 404 the original would not have
// answered either. It passes every recorded case — and every chain probe collapses on it, because a
// chain asks for resources the recording never saw.
const chainReplayRewrite = `package orders

import (
	"net/http"
	"strings"
)

func Handle(w http.ResponseWriter, r *http.Request) {
	id := strings.Trim(strings.TrimPrefix(r.URL.Path, "/orders/"), "/")
	switch {
	case r.Method == "POST" && r.URL.Path == "/orders/":
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte("{\"id\":\"42\",\"title\":\"seed\"}"))
	case r.Method == "GET" && id == "1":
		w.Write([]byte("{\"id\":\"1\",\"title\":\"alpha\"}"))
	case r.Method == "PUT" && id == "1":
		w.Write([]byte("{\"id\":\"1\",\"title\":\"updated\"}"))
	case r.Method == "DELETE" && id == "1":
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "not found", http.StatusNotFound)
	}
}
`

// chainRealRewrite implements the same in-memory CRUD as the original handler, so it answers the
// chains too: the reserved resource really exists on both sides and every step matches.
const chainRealRewrite = `package orders

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
)

var (
	chainMu    sync.Mutex
	chainItems = map[string]string{"1": "alpha"}
)

func Handle(w http.ResponseWriter, r *http.Request) {
	chainMu.Lock()
	defer chainMu.Unlock()
	id := strings.Trim(strings.TrimPrefix(r.URL.Path, "/orders/"), "/")
	var body struct{ ID, Title string }
	if data, err := io.ReadAll(r.Body); err == nil {
		_ = json.Unmarshal(data, &body)
	}
	switch {
	case r.Method == "POST" && r.URL.Path == "/orders/":
		if body.ID != "" {
			id = body.ID
		} else {
			id = fmt.Sprintf("srv-%d", len(chainItems)+1)
		}
		chainItems[id] = body.Title
		w.WriteHeader(http.StatusCreated)
		io.WriteString(w, chainItemJSON(id, body.Title))
	case r.Method == "PUT" && id != "":
		chainItems[id] = body.Title
		io.WriteString(w, chainItemJSON(id, body.Title))
	case r.Method == "DELETE" && id != "":
		delete(chainItems, id)
		w.WriteHeader(http.StatusNoContent)
	case r.Method == "GET":
		title, ok := chainItems[id]
		if !ok {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		io.WriteString(w, chainItemJSON(id, title))
	default:
		http.Error(w, "not found", http.StatusNotFound)
	}
}

func chainItemJSON(id, title string) string {
	// Keys marshal sorted, so this is byte-identical to the struct form the test original uses.
	b, _ := json.Marshal(map[string]string{"id": id, "title": title})
	return string(b)
}
`

// chainModule writes a module whose orders unit is mounted at /orders/ and holds the given rewrite.
func chainModule(t *testing.T, rewrite string) string {
	t.Helper()
	root := newModule(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte(fmt.Sprintf(rbacMainSrc, addr)), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Gen(root, "orders", "/orders/"); err != nil {
		t.Fatal(err)
	}
	if _, err := WireMain(root, "main.go", "example.com/app", "http"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "units", "orders", "rewrite.go"), []byte(rewrite), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

// chainRecording writes the recording these tests refine against: a full CRUD collection, created,
// read, updated and deleted against the real original some time before the run.
func chainRecording(t *testing.T, casesPath string) {
	t.Helper()
	rec := domain.AppRecording{Subject: "orders", Cases: []domain.AppCase{
		{Name: "create-order", Kind: "http", Method: "POST", Path: "/orders/", Body: `{"id":"42","title":"seed"}`, Status: 201, ResponseBody: `{"id":"42","title":"seed"}`},
		{Name: "read-order", Kind: "http", Method: "GET", Path: "/orders/1", Status: 200, ResponseBody: `{"id":"1","title":"alpha"}`},
		{Name: "update-order", Kind: "http", Method: "PUT", Path: "/orders/1", Body: `{"id":"1","title":"updated"}`, Status: 200, ResponseBody: `{"id":"1","title":"updated"}`},
		{Name: "delete-order", Kind: "http", Method: "DELETE", Path: "/orders/1", Status: 204},
	}}
	if err := store.SaveApp(casesPath, rec); err != nil {
		t.Fatal(err)
	}
}

func TestRefineChainDivergenceBlocksConvergence(t *testing.T) {
	origStore := &chainStore{items: map[string]string{"1": "alpha"}}
	orig := httptest.NewServer(origStore.handler())
	defer orig.Close()
	root := chainModule(t, chainReplayRewrite)
	dir := t.TempDir()
	casesPath := filepath.Join(dir, "app.json")
	chainRecording(t, casesPath)
	var log bytes.Buffer
	ok, err := Refine(RefineOptions{
		Root: root, Unit: "orders", Prefix: "/orders/", CasesPath: casesPath,
		OriginalURL: orig.URL, Rounds: 2, Probes: 4, MaxNew: 5, Seed: 1, Log: &log,
		Chains: 2,
	})
	if ok || err == nil {
		t.Fatalf("a replaying rewrite must not survive the chain check: ok=%v err=%v\n%s", ok, err, log.String())
	}
	if !strings.Contains(err.Error(), "chain probes diverged") || !strings.Contains(err.Error(), "orders") {
		t.Fatalf("the no-agent error must name the chain divergence and its collection in its own words: %v", err)
	}
	if strings.Contains(err.Error(), "critical authorization finding") {
		t.Fatalf("a chain divergence must not borrow the authorization phrasing: %v", err)
	}
	check := readRoundCheck(t, roundRecordPath(root, 1))
	if check.ChainsChecked != 1 || check.ChainDiverges != 1 || check.ChainRequests != 6 {
		t.Fatalf("the round must record the chain counts: %+v", check)
	}
	if check.ChainFirstDivergence != 2 {
		t.Fatalf("the first read after the create is the first differing step, counted from 1: %+v", check)
	}
	// The log names the step the same way the record does: counted from 1.
	if !strings.Contains(log.String(), "diverged at step 2") {
		t.Fatalf("the chain log must name the first differing step counted from 1, like the record:\n%s", log.String())
	}
	if len(check.ChainPending) != 0 {
		t.Fatalf("a divergence is not a pending verdict: %+v", check.ChainPending)
	}
	if check.CleanStreak != 0 || check.Converged {
		t.Fatalf("the divergence must break the round: clean_streak=%d converged=%v", check.CleanStreak, check.Converged)
	}
	// The chain-on round narrows the state line and keeps the others.
	if !slices.Contains(check.NotChecked, notCheckedChainOn) || slices.Contains(check.NotChecked, chainStateNote) {
		t.Fatalf("a chain round must narrow the state line only: %q", check.NotChecked)
	}
	if !slices.Contains(check.NotChecked, concurrentInterleavingsNote) {
		t.Fatalf("a round without -concurrency keeps the concurrency line: %q", check.NotChecked)
	}
	// The chain requests never reach the recording.
	rec, err := store.LoadApp(casesPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(rec.Cases) != 4 {
		t.Fatalf("the recording must keep exactly its four cases, got %d: %+v", len(rec.Cases), rec.Cases)
	}
	for _, c := range rec.Cases {
		if strings.Contains(c.Name, "chain-r") {
			t.Fatalf("a chain request must not become a case: %+v", c)
		}
	}
	// The transcript is on disk and says divergent.
	raw, err := os.ReadFile(filepath.Join(root, ".go-anything", "checks", "orders", "chain-round-01.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"divergent": true`) || strings.Contains(string(raw), "tok-") {
		t.Fatalf("the transcript must record the verdict and no token: %s", raw)
	}
	// The page shows the count card and links the transcript instead of inlining it.
	page, err := os.ReadFile(filepath.Join(root, ".go-anything", "checks", "orders", "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(page), "Chain probes") || !strings.Contains(string(page), `href="chain-round-01.json"`) {
		t.Fatalf("the page must show the chain card and link the transcript:\n%.400s", page)
	}
}

func TestRefineChainRealImplementationConverges(t *testing.T) {
	origStore := &chainStore{items: map[string]string{"1": "alpha"}}
	orig := httptest.NewServer(origStore.handler())
	defer orig.Close()
	root := chainModule(t, chainRealRewrite)
	dir := t.TempDir()
	casesPath := filepath.Join(dir, "app.json")
	chainRecording(t, casesPath)
	var log bytes.Buffer
	ok, err := Refine(RefineOptions{
		Root: root, Unit: "orders", Prefix: "/orders/", CasesPath: casesPath,
		OriginalURL: orig.URL, Rounds: 2, Probes: 4, MaxNew: 5, Seed: 1, Log: &log,
		Chains: 2,
	})
	if err != nil || !ok {
		t.Fatalf("a real in-memory implementation must survive the chain check: ok=%v err=%v\n%s", ok, err, log.String())
	}
	if !strings.Contains(log.String(), "1 target(s), 6 step(s) per side, 0 diverge, 0 pending") {
		t.Fatalf("the round must log the chain outcome:\n%s", log.String())
	}
	for _, r := range []string{"01", "02"} {
		check := readRoundCheck(t, filepath.Join(root, ".go-anything", "checks", "orders", "round-"+r+".json"))
		if check.ChainsChecked != 1 || check.ChainDiverges != 0 || len(check.ChainPending) != 0 {
			t.Fatalf("round %s must record a clean chain check: %+v", r, check)
		}
	}
	last := readRoundCheck(t, filepath.Join(root, ".go-anything", "checks", "orders", "round-02.json"))
	if !last.Converged || last.CleanStreak != 2 {
		t.Fatalf("the run must converge: %+v", last)
	}
	raw, err := os.ReadFile(filepath.Join(root, ".go-anything", "checks", "orders", "chain-round-02.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), `"divergent": true`) {
		t.Fatalf("a matching chain must not record divergent: %s", raw)
	}
	page, err := os.ReadFile(filepath.Join(root, ".go-anything", "checks", "orders", "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(page), `href="chain-round-02.json"`) {
		t.Fatalf("the page must link the transcript:\n%.400s", page)
	}
}

// TestRefineChainPendingBlocksConvergence: a chain the run cannot judge (the original's create 503s)
// keeps the round from being clean, and with no agent the run names the target and the reason.
func TestRefineChainPendingBlocksConvergence(t *testing.T) {
	inner := (&chainStore{items: map[string]string{"1": "alpha"}}).handler()
	orig := httptest.NewServer(chainOriginalWithFailingCreate(inner))
	defer orig.Close()
	root := chainModule(t, chainReplayRewrite)
	dir := t.TempDir()
	casesPath := filepath.Join(dir, "app.json")
	chainRecording(t, casesPath)
	var log bytes.Buffer
	ok, err := Refine(RefineOptions{
		Root: root, Unit: "orders", Prefix: "/orders/", CasesPath: casesPath,
		OriginalURL: orig.URL, Rounds: 2, Probes: 4, MaxNew: 5, Seed: 1, Log: &log,
		Chains: 2,
	})
	if ok || err == nil {
		t.Fatalf("an unjudgeable chain must block convergence: ok=%v err=%v\n%s", ok, err, log.String())
	}
	if !strings.Contains(err.Error(), "could not be judged on 1 collection(s): orders (original returned 503 on step 0)") {
		t.Fatalf("the error must name the pending target and its reason: %v", err)
	}
	check := readRoundCheck(t, roundRecordPath(root, 1))
	if !slices.Equal(check.ChainPending, []string{"orders"}) || check.ChainDiverges != 0 {
		t.Fatalf("the pending target must stay on the record: %+v", check)
	}
	if check.CleanStreak != 0 || check.Converged {
		t.Fatalf("a pending chain breaks the clean verdict: %+v", check)
	}
}

// TestRefineChainPendingSurvivesRoundsNamesTargetsAtTheEnd: with an agent to keep the loop going, the
// same target's repeated pending verdicts never converge the run, and running out of rounds names the
// surviving target and its reason instead of a bare budget message.
func TestRefineChainPendingSurvivesRoundsNamesTargetsAtTheEnd(t *testing.T) {
	inner := (&chainStore{items: map[string]string{"1": "alpha"}}).handler()
	orig := httptest.NewServer(chainOriginalWithFailingCreate(inner))
	defer orig.Close()
	root := chainModule(t, chainReplayRewrite)
	dir := t.TempDir()
	casesPath := filepath.Join(dir, "app.json")
	chainRecording(t, casesPath)
	ok, err := Refine(RefineOptions{
		Root: root, Unit: "orders", Prefix: "/orders/", CasesPath: casesPath,
		OriginalURL: orig.URL, Rounds: 2, Probes: 4, MaxNew: 5, Seed: 1, Log: io.Discard,
		Chains: 2, Agent: "cat >/dev/null", Attempts: 1,
	})
	if ok || err == nil {
		t.Fatalf("repeated pending must never converge: ok=%v err=%v", ok, err)
	}
	if !strings.Contains(err.Error(), "Chain probes stayed unjudgeable on: orders (original returned 503 on step 0)") {
		t.Fatalf("the out-of-rounds error must list the surviving target and reason: %v", err)
	}
	check := readRoundCheck(t, roundRecordPath(root, 2))
	if !slices.Equal(check.ChainPending, []string{"orders"}) {
		t.Fatalf("round 2 stays pending on the same target: %+v", check.ChainPending)
	}
}

func TestRefineChainRejectsCommandLineUnit(t *testing.T) {
	_, err := Refine(RefineOptions{
		Root: "unused", Unit: "orders", CasesPath: "unused.json", OriginalCLI: "orig",
		Chains: 2,
	})
	if err == nil || !strings.Contains(err.Error(), "HTTP units only") {
		t.Fatalf("the chain check on a command-line unit must be refused with a clear error: %v", err)
	}
}
