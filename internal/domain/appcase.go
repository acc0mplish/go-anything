package domain

import (
	"errors"
	"fmt"
)

// AppCase is one interaction with a whole program: an HTTP request, or a CLI invocation.
// It is kept apart from Case so single-function recordings stay unchanged.
type AppCase struct {
	Name string `json:"name"`
	// Kind is "http" or "cli".
	Kind string `json:"kind"`

	// HTTP request fields.
	Method string `json:"method,omitempty"`
	Path   string `json:"path,omitempty"`
	Body   string `json:"body,omitempty"`
	// RequestHeaders are sent with the request. A value may be ${NAME}: it is read from the environment
	// when the case runs, so a secret such as a token is never written into the recording.
	RequestHeaders map[string]string `json:"request_headers,omitempty"`

	// CLI invocation fields.
	Argv  []string `json:"argv,omitempty"`
	Stdin string   `json:"stdin,omitempty"`

	// Expected response. HTTP uses Status and ResponseBody; CLI uses ExitCode, Stdout and Stderr.
	Status       int    `json:"status,omitempty"`
	ResponseBody string `json:"response_body,omitempty"`
	// Headers holds the response headers the recording compares, keyed by canonical name.
	// Only the headers named at record time are kept; volatile ones (Date, Content-Length) are not.
	Headers map[string]string `json:"headers,omitempty"`

	// Effects are the outbound calls the program made while answering this case (for example
	// webhooks). They are observed, not answered, and compared on replay.
	Effects []Effect `json:"effects,omitempty"`

	// Unstable names the fields that differed between repeated runs of this case at record time
	// (for example "body" or "header Date"). Replay does not compare them; everything else still is.
	Unstable []string `json:"unstable,omitempty"`
	ExitCode int      `json:"exit_code,omitempty"`
	Stdout   string   `json:"stdout,omitempty"`
	Stderr   string   `json:"stderr,omitempty"`
}

// AppRecording is the captured behavior of a whole program.
type AppRecording struct {
	Subject string    `json:"subject"`
	Cases   []AppCase `json:"cases"`
}

// ErrInvalidAppCase is returned when a case is missing what its kind needs.
var ErrInvalidAppCase = errors.New("domain: invalid app case")

// Validate checks that every case has a name, a known kind, and the fields that kind needs.
func (r AppRecording) Validate() error {
	if len(r.Cases) == 0 {
		return ErrEmptyRecording
	}
	seen := map[string]bool{}
	for i, c := range r.Cases {
		if c.Name == "" {
			return fmt.Errorf("%w: case %d has no name", ErrInvalidAppCase, i)
		}
		if seen[c.Name] {
			return fmt.Errorf("%w: duplicate case %q", ErrInvalidAppCase, c.Name)
		}
		seen[c.Name] = true
		switch c.Kind {
		case "http":
			if c.Method == "" || c.Path == "" {
				return fmt.Errorf("%w: http case %q needs method and path", ErrInvalidAppCase, c.Name)
			}
		case "cli":
			if len(c.Argv) == 0 {
				return fmt.Errorf("%w: cli case %q needs argv", ErrInvalidAppCase, c.Name)
			}
		default:
			return fmt.Errorf("%w: case %q has unknown kind %q", ErrInvalidAppCase, c.Name, c.Kind)
		}
	}
	return nil
}

// DefaultHeaders are compared when a recording does not name its own. They describe what the
// client receives and change only when the program changes. Volatile headers such as Date,
// Content-Length, ETag and Set-Cookie are excluded: they differ on every run without a real change.
var DefaultHeaders = []string{"Content-Type"}

// Effect is one outbound call a program made: a webhook, for instance.
type Effect struct {
	// Kind is "webhook" for an outbound HTTP call or "db" for a row change. Empty means webhook,
	// which is what recordings made before Kind existed contain.
	Kind   string `json:"kind,omitempty"`
	Method string `json:"method,omitempty"`
	Path   string `json:"path,omitempty"`
	Body   string `json:"body,omitempty"`
	// Table, Op and Key describe a row change when Kind is "db".
	Table string `json:"table,omitempty"`
	Op    string `json:"op,omitempty"`
	Key   string `json:"key,omitempty"`
}

// Effect kinds.
const (
	EffectWebhook = "webhook"
	EffectDB      = "db"
	EffectMail    = "mail"
)

// NoResponse is the Status of an HTTP case whose server accepted the request and closed the connection
// without answering, the way a Python http.server handler that raises does. Real answers have a status of
// 100 or more, so 0 cannot be mistaken for one.
const NoResponse = 0

// AnswerOf is the compared part of a recorded or observed case.
func AnswerOf(c AppCase) Answer {
	return Answer{Kind: c.Kind, Status: c.Status, Body: c.ResponseBody, Headers: c.Headers,
		ExitCode: c.ExitCode, Stdout: c.Stdout, Stderr: c.Stderr}
}
