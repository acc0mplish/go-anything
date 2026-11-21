package wiregen

import (
	"net"
	"strings"
	"testing"

	"github.com/go-anything/go-anything/internal/domain"
	"github.com/go-anything/go-anything/internal/usecase"
)

func TestOriginalThatDropsTheConnectionIsADefectNotAnError(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			buf := make([]byte, 1024)
			c.Read(buf)
			c.Close() // like a Python handler that raised: no status line
		}
	}()
	probe := domain.AppCase{Name: "p", Kind: "http", Method: "GET", Path: "/orders/007"}
	got, err := AskOriginal("http", "", "http://"+ln.Addr().String(), probe)
	if err != nil {
		t.Fatalf("a dropped connection is the original's answer, not a loop error: %v", err)
	}
	if !usecase.OriginalCrashed(got) {
		t.Fatalf("a dropped connection must count as an original crash: %+v", got)
	}
	for _, recordsCrash := range []bool{false, true} {
		rewrite := domain.AppCase{Kind: "http", Status: 404, ResponseBody: "not found"}
		if outcome, _ := usecase.JudgeProbe(got, rewrite, recordsCrash); outcome != usecase.ProbeOriginalDefect {
			t.Fatalf("recordsCrash=%v: a dropped connection must be a defect, got %v", recordsCrash, outcome)
		}
	}
}

func TestOriginalNotRunningStopsTheLoop(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	probe := domain.AppCase{Name: "p", Kind: "http", Method: "GET", Path: "/orders/1"}
	if _, err := AskOriginal("http", "", "http://"+addr, probe); err == nil {
		t.Fatal("a refused connection means the original is down; it must be an error, not a defect")
	}
}

func TestAgentFailureSaysWhyTheAgentStopped(t *testing.T) {
	err := runShell(t.TempDir(), `echo "You've hit your usage limit"; exit 1`, "")
	if err == nil || !strings.Contains(err.Error(), "usage limit") {
		t.Fatalf("the agent's own message must be in the error: %v", err)
	}
}
