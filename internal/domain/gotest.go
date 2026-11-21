package domain

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
)

// TestEvent is one line of `go test -json` output (the fields this tool reads).
type TestEvent struct {
	Action  string `json:"Action"`
	Package string `json:"Package"`
	Test    string `json:"Test"`
	Output  string `json:"Output"`
}

// ParseGoTestJSON reads `go test -json` and returns one case per finished test.
// Passing tests record their name only; failing tests are recorded as error cases with the
// failure text, so a rewrite that reproduces the failure is accepted and one that does not is caught.
func ParseGoTestJSON(r io.Reader) ([]Case, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	outputs := map[string]string{}
	var cases []Case
	for sc.Scan() {
		var ev TestEvent
		if err := json.Unmarshal(sc.Bytes(), &ev); err != nil {
			return nil, fmt.Errorf("gotest: parse event: %w", err)
		}
		if ev.Test == "" {
			continue
		}
		key := ev.Package + "/" + ev.Test
		switch ev.Action {
		case "output":
			outputs[key] += ev.Output
		case "pass":
			cases = append(cases, Case{Name: key, Input: ev.Test, Output: "PASS"})
		case "fail":
			cases = append(cases, Case{Name: key, Input: ev.Test, Output: "", Error: "FAIL: " + trimLines(outputs[key])})
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("gotest: read: %w", err)
	}
	return cases, nil
}

func trimLines(s string) string {
	const max = 2000
	if len(s) > max {
		return s[len(s)-max:]
	}
	return s
}
