package domain

import (
	"bufio"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// Coverage is the share of statements executed, read from a Go coverprofile.
type Coverage struct {
	Statements int     `json:"statements"`
	Covered    int     `json:"covered"`
	Percent    float64 `json:"percent"`
}

// ParseCoverProfile reads a `go test -coverprofile` file (mode line, then
// "file:start.col,end.col numStmts count" rows) and returns the statement coverage.
func ParseCoverProfile(r io.Reader) (Coverage, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	var c Coverage
	first := true
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		if first {
			first = false
			if !strings.HasPrefix(line, "mode:") {
				return Coverage{}, fmt.Errorf("coverage: missing mode line, got %q", line)
			}
			continue
		}
		// last two fields are numStmts and count; the rest is file:range
		fields := strings.Fields(line)
		if len(fields) != 3 {
			return Coverage{}, fmt.Errorf("coverage: malformed row %q", line)
		}
		stmts, err := strconv.Atoi(fields[1])
		if err != nil {
			return Coverage{}, fmt.Errorf("coverage: bad statement count in %q: %w", line, err)
		}
		count, err := strconv.Atoi(fields[2])
		if err != nil {
			return Coverage{}, fmt.Errorf("coverage: bad hit count in %q: %w", line, err)
		}
		c.Statements += stmts
		if count > 0 {
			c.Covered += stmts
		}
	}
	if err := sc.Err(); err != nil {
		return Coverage{}, fmt.Errorf("coverage: read: %w", err)
	}
	if first {
		return Coverage{}, fmt.Errorf("coverage: empty profile")
	}
	if c.Statements > 0 {
		c.Percent = 100 * float64(c.Covered) / float64(c.Statements)
	}
	return c, nil
}
