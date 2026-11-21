package usecase

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/go-anything/go-anything/internal/domain"
)

// OriginalDefect is an input where the original itself failed: it crashed or panicked. The rewrite is
// not expected to copy a crash, so these are reported rather than recorded as behavior to match.
type OriginalDefect struct {
	Request  string `json:"request"`
	Evidence string `json:"evidence"`
}

// OriginalCrashed reports whether an answer from the original is a crash rather than a result.
// A command-line crash shows a Python traceback or a Go panic on stderr and exits non-zero.
// An HTTP crash is a 5xx answer whose body or the server's log shows the failure.
func OriginalCrashed(a domain.AppCase) bool {
	if a.Kind == "cli" {
		return a.ExitCode != 0 && (strings.Contains(a.Stderr, "Traceback (most recent call last)") ||
			strings.Contains(a.Stderr, "panic:") || strings.Contains(a.Stderr, "goroutine "))
	}
	if a.Status == domain.NoResponse {
		return true
	}
	return a.Status >= 500 && (strings.Contains(a.ResponseBody, "Traceback") || strings.Contains(a.ResponseBody, "panic"))
}

// OriginalDefectFor describes one probe the original crashed on.
func OriginalDefectFor(probe, answer domain.AppCase) OriginalDefect {
	return OriginalDefect{Request: describeRequest(probe), Evidence: evidence(answer)}
}

func describeRequest(c domain.AppCase) string {
	if c.Kind == "cli" {
		return fmt.Sprintf("args=%q stdin=%q", c.Argv, c.Stdin)
	}
	return c.Method + " " + c.Path
}

// evidence is the exit code (or status) and the line that names the failure. For a command-line crash that
// is the Python exception line, the Go "panic:" line, or a Node "...Error:" line (see ExceptionLine), not
// a stack frame. It is masked by SaveDefects before it reaches disk.
func evidence(a domain.AppCase) string {
	if a.Kind == "cli" {
		return fmt.Sprintf("exit %d: %s", a.ExitCode, ExceptionLine(strings.TrimSpace(a.Stderr)))
	}
	// A plain-text crash body is a traceback or a panic: name its exception line. An HTML error page
	// would yield its closing tag, so its head is kept as before.
	if body := strings.TrimSpace(a.ResponseBody); OriginalCrashed(a) && !strings.HasPrefix(strings.ToLower(body), "<") {
		return fmt.Sprintf("status %d: %.200s", a.Status, ExceptionLine(body))
	}
	return fmt.Sprintf("status %d: %.120s", a.Status, a.ResponseBody)
}

// DefectLog keeps one entry per crashing request for the whole run, however many rounds find it again.
type DefectLog struct {
	seen map[string]bool
	list []OriginalDefect
}

// NewDefectLog returns an empty log.
func NewDefectLog() *DefectLog {
	return &DefectLog{seen: map[string]bool{}}
}

// Add records a defect for a request and reports whether it was new.
func (l *DefectLog) Add(request domain.AppCase, answer domain.AppCase) bool {
	key := CaseKey(request)
	if l.seen[key] {
		return false
	}
	l.seen[key] = true
	l.list = append(l.list, OriginalDefectFor(request, answer))
	return true
}

// AddText records a defect that has no single probe behind it, such as an original answer the policy
// forbids. It is keyed by the request text, so a repeat in a later round is one defect, and it reports
// whether the defect was new.
func (l *DefectLog) AddText(request, evidence string) bool {
	key := "text\x00" + request
	if l.seen[key] {
		return false
	}
	l.seen[key] = true
	l.list = append(l.list, OriginalDefect{Request: request, Evidence: evidence})
	return true
}

// List returns the defects in the order they were first found.
func (l *DefectLog) List() []OriginalDefect {
	return l.list
}

// SaveDefects writes the defect list next to the recording, replacing any earlier file. With no defects
// the earlier file is removed, so a stale list from a previous run cannot be read as current.
// Every evidence text is masked first (MaskCrashText): crash text holds file paths and home directories
// of the machine the original ran on, and this file is shared. The request is the probe input and is kept.
func SaveDefects(path string, defects []OriginalDefect) error {
	if len(defects) == 0 {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("refine: remove stale defects: %w", err)
		}
		return nil
	}
	masked := make([]OriginalDefect, len(defects))
	for i, d := range defects {
		masked[i] = OriginalDefect{Request: d.Request, Evidence: MaskCrashText(d.Evidence)}
	}
	data, err := json.MarshalIndent(masked, "", "  ")
	if err != nil {
		return fmt.Errorf("refine: encode defects: %w", err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
		return fmt.Errorf("refine: write defects: %w", err)
	}
	return nil
}
