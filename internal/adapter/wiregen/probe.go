package wiregen

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"text/template"
	"time"

	"github.com/go-anything/go-anything/internal/adapter/app"
	"github.com/go-anything/go-anything/internal/domain"
	"github.com/go-anything/go-anything/internal/usecase"
)

// probeHarnessCLI is a program inside the module that calls a command-line unit's Handle for each probe.
const probeHarnessCLI = `package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	u "{{.Module}}/units/{{.Unit}}"
)

type probe struct {
	Argv  []string ` + "`json:\"argv\"`" + `
	Stdin string   ` + "`json:\"stdin\"`" + `
}

type answer struct {
	ExitCode int    ` + "`json:\"exit_code\"`" + `
	Stdout   string ` + "`json:\"stdout\"`" + `
	Stderr   string ` + "`json:\"stderr\"`" + `
}

func main() {
	var probes []probe
	if err := json.NewDecoder(os.Stdin).Decode(&probes); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	out := make([]answer, 0, len(probes))
	for _, p := range probes {
		out = append(out, run(p))
	}
	json.NewEncoder(os.Stdout).Encode(out)
}

func run(p probe) (a answer) {
	defer func() {
		if r := recover(); r != nil {
			a = answer{ExitCode: 2, Stderr: fmt.Sprint("panic: ", r)}
		}
	}()
	var stdout, stderr bytes.Buffer
	code := u.Handle(p.Argv, strings.NewReader(p.Stdin), &stdout, &stderr)
	return answer{ExitCode: code, Stdout: stdout.String(), Stderr: stderr.String()}
}
`

// probeHarnessHTTP is a program inside the module that calls an HTTP unit's Handle for each probe.
const probeHarnessHTTP = `package main

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"strings"

	u "{{.Module}}/units/{{.Unit}}"
)

type probe struct {
	Method string ` + "`json:\"method\"`" + `
	Path   string ` + "`json:\"path\"`" + `
}

type answer struct {
	Status       int    ` + "`json:\"status\"`" + `
	ContentType  string ` + "`json:\"content_type\"`" + `
	ResponseBody string ` + "`json:\"response_body\"`" + `
}

func main() {
	var probes []probe
	if err := json.NewDecoder(os.Stdin).Decode(&probes); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	out := make([]answer, 0, len(probes))
	for _, p := range probes {
		out = append(out, run(p))
	}
	json.NewEncoder(os.Stdout).Encode(out)
}

func run(p probe) (a answer) {
	defer func() {
		if r := recover(); r != nil {
			a = answer{Status: 500, ResponseBody: fmt.Sprint("panic: ", r)}
		}
	}()
	req := httptest.NewRequest(p.Method, p.Path, strings.NewReader(""))
	rec := httptest.NewRecorder()
	u.Handle(rec, req)
	return answer{Status: rec.Code, ContentType: rec.Header().Get("Content-Type"), ResponseBody: rec.Body.String()}
}
`

// BuildProbe writes the probe program for a unit, builds it, and returns the binary and a cleanup
// function. The program lives under the unit's folder only while it is built, and is removed after.
func BuildProbe(root, unit, kind, module string) (string, func(), error) {
	// The unit name becomes a directory name, an import path and a package clause, so the one
	// validator decides: same rule as Gen, including the reserved names (issue #20).
	if err := usecase.ValidUnitName(unit); err != nil {
		return "", func() {}, err
	}
	tpl := probeHarnessHTTP
	if kind == "cli" {
		tpl = probeHarnessCLI
	}
	t, err := template.New("probe").Parse(tpl)
	if err != nil {
		return "", func() {}, err
	}
	var src bytes.Buffer
	if err := t.Execute(&src, struct{ Module, Unit string }{module, unit}); err != nil {
		return "", func() {}, err
	}
	dir := filepath.Join(root, "units", unit, "ga_probe")
	cleanup := func() { os.RemoveAll(dir) }
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", cleanup, err
	}
	if err := os.WriteFile(filepath.Join(dir, "main.go"), src.Bytes(), 0o644); err != nil {
		cleanup()
		return "", func() {}, err
	}
	bin := filepath.Join(dir, "probe.bin")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "build", "-o", bin, "./units/"+unit+"/ga_probe")
	cmd.Dir = root
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		cleanup()
		return "", func() {}, fmt.Errorf("wire: build probe for %s: %w\n%s", unit, err, out.String())
	}
	return bin, cleanup, nil
}

// probeInput is the JSON the probe program reads: CLI probes carry argv and stdin, HTTP probes method and path.
func probeInput(kind string, probes []domain.AppCase) ([]byte, error) {
	type cliProbe struct {
		Argv  []string `json:"argv"`
		Stdin string   `json:"stdin"`
	}
	type httpProbe struct {
		Method string `json:"method"`
		Path   string `json:"path"`
	}
	var list any
	if kind == "cli" {
		var rows []cliProbe
		for _, p := range probes {
			rows = append(rows, cliProbe{Argv: p.Argv, Stdin: p.Stdin})
		}
		list = rows
	} else {
		var rows []httpProbe
		for _, p := range probes {
			rows = append(rows, httpProbe{Method: p.Method, Path: p.Path})
		}
		list = rows
	}
	return json.Marshal(list)
}

// probeTimeout bounds one probe. A unit that hangs or never returns is reported as an error, not waited
// on. It is a var so tests can shorten it instead of waiting a real ten seconds.
var probeTimeout = 10 * time.Second

// RunProbe runs the probe program on every probe and returns the rewrite's answers, in order.
// CLI probes run each probe in its own child process with a timeout, so a unit that calls os.Exit ends only
// its own child, and a hang is killed at the timeout. HTTP probes keep one process, because they do not exit it.
func RunProbe(bin, kind string, probes []domain.AppCase) ([]domain.AppCase, error) {
	if kind == "cli" {
		return runCLIProbes(bin, probes)
	}
	in, err := probeInput(kind, probes)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout*time.Duration(len(probes)+1))
	defer cancel()
	cmd := exec.CommandContext(ctx, bin)
	cmd.Stdin = bytes.NewReader(in)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return nil, fmt.Errorf("wire: probe program did not finish within the time limit")
		}
		return nil, fmt.Errorf("wire: probe program failed: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return decodeAnswers(kind, probes, stdout.Bytes())
}

// runCLIProbes sends each CLI probe to its own child process of the harness binary, with a timeout.
// The harness answers one probe per run, so an exit or hang inside the unit stays in that child.
func runCLIProbes(bin string, probes []domain.AppCase) ([]domain.AppCase, error) {
	out := make([]domain.AppCase, len(probes))
	for i, p := range probes {
		in, err := probeInput("cli", []domain.AppCase{p})
		if err != nil {
			return nil, err
		}
		ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
		cmd := exec.CommandContext(ctx, bin)
		cmd.Stdin = bytes.NewReader(in)
		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		runErr := cmd.Run()
		timedOut := ctx.Err() == context.DeadlineExceeded
		cancel()
		if timedOut {
			out[i] = withProbe(p, domain.AppCase{ExitCode: 124, Stderr: "probe timed out after " + probeTimeout.String()})
			continue
		}
		if runErr != nil {
			// The unit exited the child (for example through os.Exit) before it answered. That exit is the unit's
			// answer for this probe, so it is recorded as the exit code rather than failing the whole run.
			if ee, ok := runErr.(*exec.ExitError); ok {
				out[i] = withProbe(p, domain.AppCase{ExitCode: ee.ExitCode(), Stdout: stdout.String(), Stderr: stderr.String()})
				continue
			}
			return nil, fmt.Errorf("wire: probe program failed: %w: %s", runErr, strings.TrimSpace(stderr.String()))
		}
		ans, err := decodeAnswers("cli", []domain.AppCase{p}, stdout.Bytes())
		if err != nil {
			// The unit wrote to the harness's stdout, so the answer JSON is not clean. Record the raw output as the
			// answer with a zero exit code instead of aborting the whole run (issue #19).
			out[i] = withProbe(p, domain.AppCase{ExitCode: 0, Stdout: stdout.String(), Stderr: stderr.String()})
			continue
		}
		out[i] = ans[0]
	}
	return out, nil
}

func decodeAnswers(kind string, probes []domain.AppCase, data []byte) ([]domain.AppCase, error) {
	out := make([]domain.AppCase, len(probes))
	if kind == "cli" {
		var rows []struct {
			ExitCode int    `json:"exit_code"`
			Stdout   string `json:"stdout"`
			Stderr   string `json:"stderr"`
		}
		if err := json.Unmarshal(data, &rows); err != nil || len(rows) != len(probes) {
			return nil, fmt.Errorf("wire: probe answers do not match the probes (%v)", err)
		}
		for i, r := range rows {
			out[i] = withProbe(probes[i], domain.AppCase{ExitCode: r.ExitCode, Stdout: r.Stdout, Stderr: r.Stderr})
		}
		return out, nil
	}
	var rows []struct {
		Status       int    `json:"status"`
		ContentType  string `json:"content_type"`
		ResponseBody string `json:"response_body"`
	}
	if err := json.Unmarshal(data, &rows); err != nil || len(rows) != len(probes) {
		return nil, fmt.Errorf("wire: probe answers do not match the probes (%v)", err)
	}
	for i, r := range rows {
		a := domain.AppCase{Status: r.Status, ResponseBody: r.ResponseBody}
		if r.ContentType != "" {
			a.Headers = map[string]string{"Content-Type": r.ContentType}
		}
		out[i] = withProbe(probes[i], a)
	}
	return out, nil
}

func withProbe(request, answer domain.AppCase) domain.AppCase {
	request.ExitCode = answer.ExitCode
	request.Stdout = answer.Stdout
	request.Stderr = answer.Stderr
	request.Status = answer.Status
	request.ResponseBody = answer.ResponseBody
	request.Headers = answer.Headers
	return request
}

// droppedAfterAccept reports whether the original accepted the request and then closed the connection
// without answering (end of stream or reset): the original crashed on this input. A failure to connect
// is not that; it means the original is not running, and the loop must stop instead of reporting defects.
func droppedAfterAccept(err error) bool {
	var op *net.OpError
	if errors.As(err, &op) && op.Op == "dial" {
		return false
	}
	return errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, syscall.ECONNRESET)
}

// WireOptions paces and retries the probes sent to the original. The zero value sends each probe once,
// immediately, which is what every caller that does not ask for the new options gets.
type WireOptions struct {
	// Delay waits before each probe, so a fragile original (a single-threaded php -S) is not hammered.
	Delay time.Duration
	// Retries is how many times a connection-level failure (refused, timed out) is retried before the
	// loop reports the error and stops. A connection the original accepted and then dropped is NOT
	// retried: the drop is the original's answer (a crash on this input) and is classified as NoResponse.
	Retries int
	// RetryDelay waits between retries.
	RetryDelay time.Duration
}

// retryableWire refuses to retry a connection the original accepted and then dropped: that drop is the
// original's answer, not a delivery failure. A connection-level failure (refused, timed out) retries.
func retryableWire(err error) bool {
	return !droppedAfterAccept(err)
}

// AskOriginal sends one probe to the original: a program for command-line units, a URL for HTTP units.
// wo applies only to the HTTP original: pacing before each probe and retries on a connection-level
// failure. A command-line original is a fresh process per probe, so there is nothing to pace or retry.
func AskOriginal(kind, originalCLI, originalURL string, probe domain.AppCase, wo WireOptions) (domain.AppCase, error) {
	if kind == "cli" {
		ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
		defer cancel()
		cmd := exec.CommandContext(ctx, originalCLI, probe.Argv...)
		cmd.Stdin = strings.NewReader(probe.Stdin)
		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		code := 0
		if err := cmd.Run(); err != nil {
			if ctx.Err() == context.DeadlineExceeded {
				return domain.AppCase{}, fmt.Errorf("wire: original %s did not finish within %s on %v", originalCLI, probeTimeout, probe.Argv)
			}
			ee, ok := err.(*exec.ExitError)
			if !ok {
				return domain.AppCase{}, fmt.Errorf("wire: run original %s: %w", originalCLI, err)
			}
			code = ee.ExitCode()
		}
		return withProbe(probe, domain.AppCase{ExitCode: code, Stdout: stdout.String(), Stderr: stderr.String()}), nil
	}
	// HTTP probes go through app.Runner, the same request builder the rewrite side uses, so method, path,
	// body, request headers and default Content-Type are identical on both sides (issue #16).
	runner := app.Runner{BaseURL: originalURL, Delay: wo.Delay, Retries: wo.Retries, RetryDelay: wo.RetryDelay,
		Retryable: retryableWire}
	got, err := runner.Do(probe)
	if err != nil {
		if droppedAfterAccept(err) {
			return withProbe(probe, domain.AppCase{Status: domain.NoResponse,
				ResponseBody: "the original closed the connection without a response: " + err.Error()}), nil
		}
		return domain.AppCase{}, fmt.Errorf("wire: ask original %s: %w", probe.Path, err)
	}
	return withProbe(probe, got), nil
}
