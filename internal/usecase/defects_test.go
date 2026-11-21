package usecase

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/go-anything/go-anything/internal/domain"
)

func TestOriginalCrashedRecognisesCLITraceback(t *testing.T) {
	crash := domain.AppCase{Kind: "cli", ExitCode: 1, Stderr: "Traceback (most recent call last):\n  File \"x\"\nValueError"}
	if !OriginalCrashed(crash) {
		t.Fatal("a traceback with a failing exit must count as an original crash")
	}
	usage := domain.AppCase{Kind: "cli", ExitCode: 1, Stderr: "usage: tool orders <id>"}
	if OriginalCrashed(usage) {
		t.Fatal("a usage error is a result, not a crash")
	}
}

func TestOriginalCrashedRecognisesHTTP500WithTrace(t *testing.T) {
	if !OriginalCrashed(domain.AppCase{Kind: "http", Status: 500, ResponseBody: "Traceback here"}) {
		t.Fatal("a 500 with a trace is a crash")
	}
	if OriginalCrashed(domain.AppCase{Kind: "http", Status: 404, ResponseBody: "no such order"}) {
		t.Fatal("a 404 is an answer")
	}
}

func TestOriginalDefectDescribesRequestAndEvidence(t *testing.T) {
	probe := domain.AppCase{Kind: "cli", Argv: []string{"orders", "007"}}
	answer := domain.AppCase{Kind: "cli", ExitCode: 1, Stderr: "Traceback\nValueError: bad id"}
	d := OriginalDefectFor(probe, answer)
	if !strings.Contains(d.Request, "007") || !strings.Contains(d.Evidence, "ValueError") {
		t.Fatalf("the defect must name the request and the failure: %+v", d)
	}
}

func TestDefectLogKeepsOnePerRequest(t *testing.T) {
	log := NewDefectLog()
	probe := domain.AppCase{Kind: "cli", Argv: []string{"orders", "007"}}
	answer := domain.AppCase{Kind: "cli", ExitCode: 1, Stderr: "Traceback\nValueError"}
	if !log.Add(probe, answer) {
		t.Fatal("the first sighting must be new")
	}
	if log.Add(probe, answer) {
		t.Fatal("the same request found again, in a later round, must not be a second defect")
	}
	if len(log.List()) != 1 {
		t.Fatalf("want one defect, got %d", len(log.List()))
	}
}

func TestSaveDefectsRemovesStaleFileWhenNoneFound(t *testing.T) {
	path := t.TempDir() + "/app.json.defects.json"
	if err := os.WriteFile(path, []byte("[]"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := SaveDefects(path, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("an earlier defect file must not survive a run that found none")
	}
}

func TestDefectLogAddTextKeepsOnePerRequest(t *testing.T) {
	l := NewDefectLog()
	if !l.AddText("get-invoice as clerk-globex", "original allow (status 200)") {
		t.Fatal("the first text defect must be new")
	}
	if l.AddText("get-invoice as clerk-globex", "again") {
		t.Fatal("a repeat of the same request must not be recorded twice")
	}
	if got := l.List(); len(got) != 1 || got[0].Request != "get-invoice as clerk-globex" {
		t.Fatalf("one defect expected, got %+v", got)
	}
}

func TestDefectEvidenceNamesTheFailureNotAStackFrame(t *testing.T) {
	probe := domain.AppCase{Kind: "cli", Argv: []string{"conv", "x"}}
	cases := map[string]string{
		goPanic:     "exit 2: panic: runtime error: index out of range [3] with length 3",
		pyTraceback: "exit 2: ValueError: agent name is classified: 007",
		nodeStack:   "exit 2: TypeError: bad name",
	}
	for stderr, want := range cases {
		d := OriginalDefectFor(probe, domain.AppCase{Kind: "cli", ExitCode: 2, Stderr: stderr})
		if d.Evidence != want {
			t.Errorf("evidence = %q, want %q", d.Evidence, want)
		}
	}
}

func TestSaveDefectsMasksEvidence(t *testing.T) {
	path := t.TempDir() + "/app.json.defects.json"
	defects := []OriginalDefect{
		{Request: `args=["conv" "/home/zed/in.txt"]`, Evidence: "exit 1: FileNotFoundError: '/home/zed/data/in.txt'"},
		{Request: "GET /conv/1", Evidence: "status 500: " + strings.Repeat("x", 5000)},
	}
	if err := SaveDefects(path, defects); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got []OriginalDefect
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got[0].Evidence != "exit 1: FileNotFoundError: 'in.txt'" {
		t.Fatalf("path in evidence not masked: %q", got[0].Evidence)
	}
	if got[0].Request != defects[0].Request {
		t.Fatalf("the request is the probe input and must be kept: %q", got[0].Request)
	}
	if len(got[1].Evidence) > CrashTextLimit+64 || !strings.Contains(got[1].Evidence, "more bytes not shown") {
		t.Fatalf("long evidence must be clipped: %d bytes", len(got[1].Evidence))
	}
	if defects[0].Evidence != "exit 1: FileNotFoundError: '/home/zed/data/in.txt'" {
		t.Fatal("SaveDefects must not change the caller's list")
	}
}

func TestHTTPDefectEvidenceNamesTheException(t *testing.T) {
	probe := domain.AppCase{Kind: "http", Method: "GET", Path: "/hello/007"}
	d := OriginalDefectFor(probe, domain.AppCase{Kind: "http", Status: 500, ResponseBody: pyTraceback + "\n"})
	if d.Evidence != "status 500: ValueError: agent name is classified: 007" {
		t.Fatalf("plain-text crash body: %q", d.Evidence)
	}
	html := "<!DOCTYPE html><html><body><pre>Traceback (most recent call last): boom</pre></body></html>"
	d = OriginalDefectFor(probe, domain.AppCase{Kind: "http", Status: 500, ResponseBody: html})
	if !strings.HasPrefix(d.Evidence, "status 500: <!DOCTYPE html>") {
		t.Fatalf("an HTML error page keeps its head: %q", d.Evidence)
	}
}
