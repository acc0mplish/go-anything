package usecase

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/go-anything/go-anything/internal/domain"
)

// MaxExtraInputs bounds the values an extra-inputs file may hold. Each value becomes up to four requests
// on both sides, so the bound keeps the cross-check short.
const MaxExtraInputs = 1000

// ParseExtraInputs reads the values of an -extra-inputs file: a JSON array of strings when the text starts
// with "[", otherwise one value per line. In the line form a trailing "\r" is dropped and blank lines are
// skipped (use the JSON form for an empty value or one with spaces at the ends). A value may not hold a NUL
// byte, which no command line can carry. An empty list is an error: the operator asked for a check.
func ParseExtraInputs(data []byte) ([]string, error) {
	var values []string
	if t := bytes.TrimSpace(data); len(t) > 0 && t[0] == '[' {
		if err := json.Unmarshal(t, &values); err != nil {
			return nil, fmt.Errorf("extra inputs: not a JSON array of strings: %w", err)
		}
	} else {
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSuffix(line, "\r")
			if strings.TrimSpace(line) == "" {
				continue
			}
			values = append(values, line)
		}
	}
	if len(values) == 0 {
		return nil, fmt.Errorf("extra inputs: the file holds no value")
	}
	if len(values) > MaxExtraInputs {
		return nil, fmt.Errorf("extra inputs: %d values, at most %d are allowed", len(values), MaxExtraInputs)
	}
	for i, v := range values {
		if strings.ContainsRune(v, 0) {
			return nil, fmt.Errorf("extra inputs: value %d holds a NUL byte", i+1)
		}
	}
	return values, nil
}

// AnswerText is a short, one-line description of an answer for a report: the exit code and output of a
// command, or the status and body of an HTTP answer. Of crash text only the masked line that names the
// failure is kept (ExceptionLine); every part is clipped.
func AnswerText(a domain.AppCase) string {
	crashed := OriginalCrashed(a)
	clip := func(s string) string { return domain.ClipForReport(s, 300) }
	crashText := func(s string) string {
		if crashed {
			s = ExceptionLine(maskPaths(s))
		}
		return clip(s)
	}
	if a.Kind == "cli" {
		return fmt.Sprintf("exit %d, stdout=%q, stderr=%q", a.ExitCode, clip(a.Stdout), crashText(a.Stderr))
	}
	return fmt.Sprintf("status %d, body=%q", a.Status, crashText(a.ResponseBody))
}
