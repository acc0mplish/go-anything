package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-anything/go-anything/internal/adapter/app"
	"github.com/go-anything/go-anything/internal/adapter/doctor"
	"github.com/go-anything/go-anything/internal/adapter/gatecheck"
	"github.com/go-anything/go-anything/internal/adapter/mcp"
	"github.com/go-anything/go-anything/internal/adapter/store"
	"github.com/go-anything/go-anything/internal/adapter/subject"
	"github.com/go-anything/go-anything/internal/domain"
	"github.com/go-anything/go-anything/internal/usecase"
)

// registerExtraTools adds the tools that run the other steps: gate, app replay, fuzz, map, doctor and
// wire status. Each calls the same code the command line uses, so the two cannot disagree.
func registerExtraTools(srv *mcp.Server) {
	srv.Register(mcp.Tool{
		Name:        "gate",
		Description: "Completion gate for a Go module: gofmt, go vet, go build, go test, no placeholder bodies, unsafe justified. Optionally replays an app recording against a base URL.",
		Schema: schema(map[string]any{
			"root":       map[string]any{"type": "string", "description": "Go module root (default .)"},
			"skip_tests": map[string]any{"type": "boolean"},
			"app":        map[string]any{"type": "string", "description": "app recording to replay"},
			"base":       map[string]any{"type": "string", "description": "URL the recording replays against"},
		}),
		Call: func(args json.RawMessage) (string, error) {
			var a struct {
				Root      string `json:"root"`
				SkipTests bool   `json:"skip_tests"`
				App       string `json:"app"`
				Base      string `json:"base"`
			}
			if err := decode(args, &a); err != nil {
				return "", err
			}
			root := orDefault(a.Root, ".")
			checks := gatecheck.Run(gatecheck.Options{Root: root, SkipTests: a.SkipTests})
			if a.App != "" {
				if a.Base == "" {
					return "", errors.New("gate: app needs base")
				}
				rec, err := store.LoadApp(a.App)
				if err != nil {
					return "", err
				}
				v, err := usecase.AppReplay(rec, app.Runner{BaseURL: a.Base})
				if err != nil {
					return "", err
				}
				checks = append(checks, domain.GateCheck{Name: "recorded behavior (app)", Passed: v.Passed(),
					Detail: fmt.Sprintf("%d of %d cases match", v.Matched, v.Total)})
			}
			return formatChecks(checks), gateError(checks)
		},
	})

	srv.Register(mcp.Tool{
		Name:        "app_replay",
		Description: "Replay an app recording against a running HTTP service (base) or a command-line program (cli). Returns matched/total and each mismatch.",
		Schema: schema(map[string]any{
			"recording": map[string]any{"type": "string"},
			"base":      map[string]any{"type": "string"},
			"cli":       map[string]any{"type": "string"},
		}, "recording"),
		Call: func(args json.RawMessage) (string, error) {
			var a struct {
				Recording string `json:"recording"`
				Base      string `json:"base"`
				CLI       string `json:"cli"`
			}
			if err := decode(args, &a); err != nil {
				return "", err
			}
			rec, err := store.LoadApp(a.Recording)
			if err != nil {
				return "", err
			}
			runner, err := appRunner(a.Base, a.CLI)
			if err != nil {
				return "", err
			}
			v, err := usecase.AppReplay(rec, runner)
			if err != nil {
				return "", err
			}
			var b strings.Builder
			fmt.Fprintf(&b, "%d of %d app cases match (%s)\n", v.Matched, v.Total, v.Subject)
			for _, m := range v.Mismatches {
				fmt.Fprintf(&b, "  %s: %s differs: want %q, got %q\n", m.Case, m.What, m.Want, m.Got)
			}
			if !v.Passed() {
				return b.String(), fmt.Errorf("app replay: %d mismatch(es)", len(v.Mismatches))
			}
			return b.String(), nil
		},
	})

	srv.Register(mcp.Tool{
		Name:        "fuzz",
		Description: "Live differential test of two command-line programs on fresh generated inputs. Returns the seed (replayable) and every disagreement.",
		Schema: schema(map[string]any{
			"original": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "original command argv"},
			"rewrite":  map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "rewrite command argv"},
			"n":        map[string]any{"type": "integer"},
			"seed":     map[string]any{"type": "integer", "description": "0 picks one"},
		}, "original", "rewrite"),
		Call: func(args json.RawMessage) (string, error) {
			var a struct {
				Original []string `json:"original"`
				Rewrite  []string `json:"rewrite"`
				N        int      `json:"n"`
				Seed     uint64   `json:"seed"`
			}
			if err := decode(args, &a); err != nil {
				return "", err
			}
			if len(a.Original) == 0 || len(a.Rewrite) == 0 {
				return "", errors.New("fuzz: original and rewrite argv are required")
			}
			n := a.N
			if n < 1 {
				n = 200
			}
			seed := a.Seed
			if seed == 0 {
				seed = uint64(time.Now().UnixNano())
			}
			rep := usecase.Fuzz(subject.Exec{Argv: a.Original}, subject.Exec{Argv: a.Rewrite}, domain.FuzzInputs(seed, n), seed)
			var b strings.Builder
			fmt.Fprintf(&b, "fuzz: %d fresh inputs, seed %d (replay with seed %d)\n", rep.Total, rep.Seed, rep.Seed)
			for _, m := range rep.Mismatches {
				fmt.Fprintf(&b, "  %s: input %s original %s rewrite %s\n", m.Detail, shortQuote(m.Input), shortQuote(m.Original), shortQuote(m.Rewrite))
			}
			if !rep.Passed() {
				return b.String(), errors.New("fuzz: the rewrite disagrees with the original")
			}
			return b.String(), nil
		},
	})

	srv.Register(mcp.Tool{
		Name:        "doctor",
		Description: "Read-only health check: Go toolchain, go tool covdata/pprof, agent connections and skills, report folder, webhook port.",
		Schema:      schema(map[string]any{"project": map[string]any{"type": "string"}}),
		Call: func(args json.RawMessage) (string, error) {
			var a struct {
				Project string `json:"project"`
			}
			if err := decode(args, &a); err != nil {
				return "", err
			}
			home, err := os.UserHomeDir()
			if err != nil {
				return "", err
			}
			project := orDefault(a.Project, ".")
			rep := usecase.Doctor(doctor.Gather(project, project, home))
			return formatChecks(checksOf(rep)), nil
		},
	})

	srv.Register(mcp.Tool{
		Name:        "map",
		Description: "Drift report for recordings (unit=recording.json=source-dir) and optionally the list of packages no recording covers (discover=module root).",
		Schema: schema(map[string]any{
			"units":    map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"discover": map[string]any{"type": "string"},
		}),
		Call: func(args json.RawMessage) (string, error) {
			var a struct {
				Units    []string `json:"units"`
				Discover string   `json:"discover"`
			}
			if err := decode(args, &a); err != nil {
				return "", err
			}
			var targets []usecase.Target
			for _, u := range a.Units {
				rec, src, ok := strings.Cut(u, "=")
				if !ok || rec == "" || src == "" {
					return "", fmt.Errorf("map: %q must look like recording.json=source-dir", u)
				}
				targets = append(targets, usecase.Target{Recording: rec, Source: src})
			}
			var b strings.Builder
			if len(targets) > 0 {
				for _, r := range usecase.Map(targets, func(p string) (domain.Recording, error) {
					return (store.FileStore{}).Load(p)
				}) {
					fmt.Fprintf(&b, "[%s] %s (%d cases)", r.Status, r.Unit, r.Cases)
					if r.Detail != "" {
						fmt.Fprintf(&b, " - %s", r.Detail)
					}
					b.WriteString("\n")
				}
			}
			if a.Discover != "" {
				var buf bytes.Buffer
				if _, err := reportUnported(&buf, a.Discover, targets, func(p string) (domain.Recording, error) {
					return (store.FileStore{}).Load(p)
				}); err != nil {
					return "", err
				}
				b.Write(buf.Bytes())
			}
			return b.String(), nil
		},
	})

	srv.Register(mcp.Tool{
		Name:        "wire_status",
		Description: "Which target (rewrite or original) each unit in a routing file currently points at.",
		Schema:      schema(map[string]any{"routes": map[string]any{"type": "string"}}, "routes"),
		Call: func(args json.RawMessage) (string, error) {
			var a struct {
				Routes string `json:"routes"`
			}
			if err := decode(args, &a); err != nil {
				return "", err
			}
			routes, err := loadRoutes(a.Routes)
			if err != nil {
				return "", err
			}
			var b strings.Builder
			for name, target := range unitTargets(routes) {
				fmt.Fprintf(&b, "%s %s\n", name, target)
			}
			return b.String(), nil
		},
	})
}

func schema(props map[string]any, required ...string) map[string]any {
	out := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		out["required"] = required
	}
	return out
}

func decode(raw json.RawMessage, v any) error {
	if len(raw) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, v); err != nil {
		return fmt.Errorf("invalid arguments: %w", err)
	}
	return nil
}

func orDefault(v, d string) string {
	if v == "" {
		return d
	}
	return v
}

func formatChecks(checks []domain.GateCheck) string {
	var b strings.Builder
	for _, c := range checks {
		mark := "ok  "
		if !c.Passed {
			mark = "FAIL"
		}
		fmt.Fprintf(&b, "[%s] %-40s %s\n", mark, c.Name, c.Detail)
	}
	return b.String()
}

func gateError(checks []domain.GateCheck) error {
	if !domain.GatePassed(checks) {
		return errors.New("gate: not complete")
	}
	return nil
}

func checksOf(rep domain.DoctorReport) []domain.GateCheck {
	out := make([]domain.GateCheck, 0, len(rep.Checks))
	for _, c := range rep.Checks {
		detail := c.Detail
		if c.Fix != "" {
			detail += " (fix: " + c.Fix + ")"
		}
		out = append(out, domain.GateCheck{Name: c.Name, Passed: c.Level != domain.LevelFail, Detail: detail})
	}
	return out
}

func loadRoutes(path string) ([]domain.Route, error) {
	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return nil, err
	}
	var f struct {
		Routes []domain.Route `json:"routes"`
	}
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, err
	}
	return f.Routes, nil
}

func unitTargets(routes []domain.Route) map[string]string {
	out := map[string]string{}
	for _, r := range routes {
		if r.Unit == "" {
			continue
		}
		if prev, ok := out[r.Unit]; ok && prev != r.Target {
			out[r.Unit] = "mixed"
			continue
		}
		if _, ok := out[r.Unit]; !ok {
			out[r.Unit] = r.Target
		}
	}
	return out
}
