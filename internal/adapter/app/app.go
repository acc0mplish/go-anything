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
	// Delay waits this long before each HTTP request, so a fragile original (a single-threaded PHP
	// server) is not hammered by back-to-back cases. Zero sends immediately. The CLI path ignores it.
	Delay time.Duration
	// Retries is how many more times an HTTP request that failed on the transport is sent before the
	// failure is reported. A transport failure is a client.Do error or a body read error; an HTTP status
	// code is an answer, never a transport failure, so it is never retried. Zero (the default) sends once.
	Retries int
	// RetryDelay waits between retries. Zero means the retries run back to back.
	RetryDelay time.Duration
	// Retryable decides whether a transport error may be retried. nil retries every transport error.
	// The wiregen probe passes one that refuses to retry a connection the original accepted and then
	// dropped: that drop is the original's answer (a crash), not a delivery failure.
	Retryable func(error) bool
	// sleep is the wait for Delay and RetryDelay. nil means time.Sleep; a test injects a stub instead of
	// measuring wall time.
	sleep func(time.Duration)
}

// pause waits d through the injected sleeper. Zero and negative waits return at once, so a zero-value
// Runner never sleeps.
func (r Runner) pause(d time.Duration) {
	if d <= 0 {
		return
	}
	if r.sleep != nil {
		r.sleep(d)
		return
	}
	time.Sleep(d)
}

// Pacing carries the HTTP pacing and retry settings from a command's flags into a Runner: the wait
// before each request, the retries on a transport failure, and the wait between those retries. The zero
// value is the behavior every command had before the settings existed.
type Pacing struct {
	Delay      time.Duration
	Retries    int
	RetryDelay time.Duration
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
	// A request cannot simply be resent: its body reader is consumed by the first send. Every attempt
	// builds a fresh request with the same builder steps and the same error texts a zero-value Runner
	// has always produced.
	build := func() (*http.Request, error) {
		req, err := http.NewRequest(c.Method, strings.TrimRight(r.BaseURL, "/")+c.Path, strings.NewReader(c.Body))
		if err != nil {
			return nil, fmt.Errorf("app: build request: %w", err)
		}
		for name, value := range c.RequestHeaders {
			v, err := expandEnv(value)
			if err != nil {
				return nil, fmt.Errorf("app: header %s: %w", name, err)
			}
			req.Header.Set(name, v)
		}
		// Servers that parse JSON reject a JSON body sent without its type with 415. A case that names its own
		// Content-Type keeps it; only an unlabeled body that is valid JSON gets the default.
		if c.Body != "" && req.Header.Get("Content-Type") == "" && json.Valid([]byte(c.Body)) {
			req.Header.Set("Content-Type", "application/json")
		}
		return req, nil
	}
	attempts := max(r.Retries, 0) + 1
	for attempt := 0; ; attempt++ {
		if attempt == 0 {
			r.pause(r.Delay)
		} else {
			r.pause(r.RetryDelay)
		}
		req, err := build()
		if err != nil {
			return domain.AppCase{}, err
		}
		resp, err := client.Do(req)
		if err == nil {
			body, readErr := io.ReadAll(resp.Body)
			resp.Body.Close()
			if readErr == nil {
				out := c
				out.Status = resp.StatusCode
				out.ResponseBody = string(body)
				out.Headers = captureHeaders(resp.Header, headersToCapture(c))
				return out, nil
			}
			// A body that died mid-read is a transport failure, not an answer: it may be retried.
			err = fmt.Errorf("app: read body: %w", readErr)
		} else {
			err = fmt.Errorf("app: %s %s: %w", c.Method, c.Path, err)
		}
		if attempt+1 >= attempts || (r.Retryable != nil && !r.Retryable(err)) {
			return domain.AppCase{}, err
		}
	}
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
