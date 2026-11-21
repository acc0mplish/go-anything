// Package app is the adapter that runs AppCases against a real program: an HTTP server
// reached at a base URL, or a command-line program.
package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/go-anything/go-anything/internal/domain"
)

// DefaultTimeout bounds one request or one process run.
const DefaultTimeout = 30 * time.Second

// Runner runs HTTP and CLI cases against a base URL (for HTTP) and an argv prefix (for CLI).
type Runner struct {
	// BaseURL is where the HTTP program listens, e.g. http://127.0.0.1:8080.
	BaseURL string
	// Client is used for HTTP; nil means a client with DefaultTimeout.
	Client *http.Client
	// Timeout bounds each CLI run. Zero means DefaultTimeout.
	Timeout time.Duration
	// Argv0 is the program that CLI cases run; each case's Argv replaces the arguments.
	Argv0 string
}

// Do implements usecase.AppRunner.
func (r Runner) Do(c domain.AppCase) (domain.AppCase, error) {
	switch c.Kind {
	case "http":
		return r.doHTTP(c)
	case "cli":
		return r.doCLI(c)
	}
	return domain.AppCase{}, fmt.Errorf("app: unknown kind %q", c.Kind)
}

func (r Runner) doHTTP(c domain.AppCase) (domain.AppCase, error) {
	if r.BaseURL == "" {
		return domain.AppCase{}, errors.New("app: http case needs a base URL")
	}
	client := r.Client
	if client == nil {
		client = &http.Client{Timeout: DefaultTimeout}
	}
	req, err := http.NewRequest(c.Method, strings.TrimRight(r.BaseURL, "/")+c.Path, strings.NewReader(c.Body))
	if err != nil {
		return domain.AppCase{}, fmt.Errorf("app: build request: %w", err)
	}
	for name, value := range c.RequestHeaders {
		v, err := expandEnv(value)
		if err != nil {
			return domain.AppCase{}, fmt.Errorf("app: header %s: %w", name, err)
		}
		req.Header.Set(name, v)
	}
	// Servers that parse JSON reject a JSON body sent without its type with 415. A case that names its own
	// Content-Type keeps it; only an unlabeled body that is valid JSON gets the default.
	if c.Body != "" && req.Header.Get("Content-Type") == "" && json.Valid([]byte(c.Body)) {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := client.Do(req)
	if err != nil {
		return domain.AppCase{}, fmt.Errorf("app: %s %s: %w", c.Method, c.Path, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return domain.AppCase{}, fmt.Errorf("app: read body: %w", err)
	}
	out := c
	out.Status = resp.StatusCode
	out.ResponseBody = string(body)
	out.Headers = captureHeaders(resp.Header, headersToCapture(c))
	return out, nil
}

func (r Runner) doCLI(c domain.AppCase) (domain.AppCase, error) {
	if r.Argv0 == "" {
		return domain.AppCase{}, errors.New("app: cli case needs a program")
	}
	timeout := r.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	// Arguments go straight to exec, never through a shell.
	cmd := exec.CommandContext(ctx, r.Argv0, c.Argv...)
	cmd.Stdin = strings.NewReader(c.Stdin)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		return domain.AppCase{}, fmt.Errorf("app: cli timed out after %s", timeout)
	}
	code := 0
	if err != nil {
		var ee *exec.ExitError
		if !errors.As(err, &ee) {
			return domain.AppCase{}, fmt.Errorf("app: run %s: %w", r.Argv0, err)
		}
		code = ee.ExitCode()
	}
	out := c
	out.ExitCode = code
	out.Stdout = stdout.String()
	out.Stderr = stderr.String()
	return out, nil
}

// headersToCapture is the set of response headers to keep for a case: the case's own list
// when it has one (its Headers keys), otherwise the defaults.
func headersToCapture(c domain.AppCase) []string {
	if len(c.Headers) > 0 {
		names := make([]string, 0, len(c.Headers))
		for k := range c.Headers {
			names = append(names, k)
		}
		return names
	}
	return domain.DefaultHeaders
}

// captureHeaders copies the named headers, canonicalized, from a response.
func captureHeaders(h http.Header, names []string) map[string]string {
	out := map[string]string{}
	for _, n := range names {
		if v := h.Get(n); v != "" {
			out[http.CanonicalHeaderKey(n)] = v
		}
	}
	return out
}

// envRef matches ${NAME}, the only form a recorded header value may use to point at a secret.
var envRef = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

// expandEnv replaces each ${NAME} with the environment variable NAME. A missing variable is an error:
// sending a request without its credential would record a meaningless answer.
func expandEnv(value string) (string, error) {
	var missing string
	out := envRef.ReplaceAllStringFunc(value, func(m string) string {
		name := envRef.FindStringSubmatch(m)[1]
		v, ok := os.LookupEnv(name)
		if !ok && missing == "" {
			missing = name
		}
		return v
	})
	if missing != "" {
		return "", fmt.Errorf("environment variable %s is not set", missing)
	}
	return out, nil
}
