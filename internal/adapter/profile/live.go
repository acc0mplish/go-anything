// Package profile is the adapter that takes a CPU profile from a running Go service through its
// net/http/pprof endpoint and reads it with the official go tool pprof. Parsing the pprof table
// is left to domain.ParseTop.
package profile

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
)

// PprofProfilePath is where net/http/pprof serves a CPU profile.
const PprofProfilePath = "/debug/pprof/profile"

// maxProfileBytes bounds one profile read from a service. A CPU profile of a few minutes is far smaller.
const maxProfileBytes = 64 << 20

// maxDetailBytes bounds how much of an error body is quoted in a message.
const maxDetailBytes = 200

// ValidateBase checks the base URL of a live service. Credentials are refused, and the URL is not
// echoed in the error, so a token typed into it cannot reach the terminal or a log.
func ValidateBase(base string) error {
	u, err := url.Parse(base)
	if err != nil {
		return errors.New("profile: -url is not a valid URL")
	}
	if u.User != nil {
		return errors.New("profile: -url must not contain credentials; the profile request sends none")
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return errors.New("profile: -url must be an http:// or https:// URL with a host, e.g. http://127.0.0.1:8080")
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return errors.New("profile: -url must not have a query or a fragment")
	}
	return nil
}

// FetchCPUProfile asks the service at base for a CPU profile sampled over the given seconds. It returns
// the gzip bytes only when the whole body arrived and decodes. Any other answer is an error, so a caller
// never saves or ranks a profile that was cut short.
func FetchCPUProfile(ctx context.Context, client *http.Client, base string, seconds int) ([]byte, error) {
	target := strings.TrimRight(base, "/") + PprofProfilePath + "?seconds=" + strconv.Itoa(seconds)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, fmt.Errorf("profile: build request: %w", err)
	}
	// No recorded headers are sent: the profile endpoint needs none, and they may carry credentials.
	resp, err := client.Do(req)
	if err != nil {
		return nil, Unreachable(base, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, statusError(base, resp)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxProfileBytes+1))
	if err != nil {
		return nil, incomplete(base, err)
	}
	if len(body) > maxProfileBytes {
		return nil, fmt.Errorf("profile: the profile from %s is larger than %d MB; nothing was saved", base, maxProfileBytes>>20)
	}
	zr, err := gzip.NewReader(bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("profile: %s%s answered 200 with a body that is not a CPU profile; the service must import net/http/pprof and mount it (a catch-all route answers 200 without it)", base, PprofProfilePath)
	}
	if _, err := io.Copy(io.Discard, zr); err != nil {
		return nil, incomplete(base, err)
	}
	return body, nil
}

// statusError explains a non-200 answer. A 404 is the usual sign that the service never mounted pprof.
func statusError(base string, resp *http.Response) error {
	if resp.StatusCode == http.StatusNotFound {
		return fmt.Errorf("profile: %s%s answered HTTP 404 Not Found: the service does not serve pprof. It must import net/http/pprof and mount its handlers; they register on http.DefaultServeMux, so a custom mux must serve that too", base, PprofProfilePath)
	}
	detail, _ := io.ReadAll(io.LimitReader(resp.Body, maxDetailBytes))
	msg := strings.Join(strings.Fields(string(detail)), " ")
	if msg == "" {
		return fmt.Errorf("profile: %s%s answered HTTP %d %s; nothing was saved", base, PprofProfilePath, resp.StatusCode, http.StatusText(resp.StatusCode))
	}
	return fmt.Errorf("profile: %s%s answered HTTP %d %s: %s; nothing was saved", base, PprofProfilePath, resp.StatusCode, http.StatusText(resp.StatusCode), msg)
}

// Unreachable names the cause of a transport failure. A refused connection or an unknown host means the
// URL is not reachable. Anything else is reported as a failed request, with the original error kept.
func Unreachable(base string, err error) error {
	var dns *net.DNSError
	if errors.Is(err, syscall.ECONNREFUSED) || errors.As(err, &dns) {
		return fmt.Errorf("profile: %s is not reachable (connection refused or unknown host); start the service or fix -url: %w", base, err)
	}
	return fmt.Errorf("profile: request to %s failed: %w", base, err)
}

func incomplete(base string, err error) error {
	return fmt.Errorf("profile: the profile from %s was cut short or corrupt (%v); nothing was saved", base, err)
}

// WriteProfile saves a complete profile. It is private to the user, since profiles name the code paths
// of the service.
func WriteProfile(path string, data []byte) error {
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("profile: save %s: %w", path, err)
	}
	return nil
}

// Top runs `go tool pprof -top` on a profile and returns its text. The binary is optional: a Go CPU profile
// carries its function names, so the table works without it. A binary only adds symbols the profile lacks.
func Top(profilePath, bin string) ([]byte, error) {
	args := []string{"tool", "pprof", "-top"}
	if bin != "" {
		args = append(args, bin)
	}
	args = append(args, profilePath)
	var stdout, stderr bytes.Buffer
	cmd := exec.Command("go", args...)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("profile: pprof: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
}

// HasSamples reports whether a `go tool pprof -top` report counted any CPU samples. An idle service gives a
// header and no rows, which would otherwise look like "nothing is hot".
func HasSamples(top []byte) bool {
	for _, line := range strings.Split(string(top), "\n") {
		if strings.HasPrefix(line, "Showing nodes accounting for") {
			return !strings.Contains(line, " of 0 total")
		}
	}
	return false
}
