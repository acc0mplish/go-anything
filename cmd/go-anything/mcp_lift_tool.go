package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/go-anything/go-anything/internal/adapter/app"
	"github.com/go-anything/go-anything/internal/adapter/gatecheck"
	"github.com/go-anything/go-anything/internal/adapter/mcp"
	"github.com/go-anything/go-anything/internal/adapter/store"
	"github.com/go-anything/go-anything/internal/domain"
	"github.com/go-anything/go-anything/internal/usecase"
)

// registerLiftTool exposes lift with a narrower scope than the command line. It replays the app against
// the rewrite, runs the static gate, and checks each unit's recording for drift. It does not run unit
// commands, so it never shows that a unit still matches its recording. The command-line lift does that.
func registerLiftTool(srv *mcp.Server) {
	srv.Register(mcp.Tool{
		Name: "lift",
		Description: "Check whether a program can be lifted: app replay against the rewrite, the static gate, and " +
			"drift of each unit recording. It does NOT run unit commands, so unit behavior is not verified here; " +
			"use the command-line lift for that.",
		Schema: schema(map[string]any{
			"app":        map[string]any{"type": "string", "description": "app recording to replay"},
			"base":       map[string]any{"type": "string", "description": "URL of the rewrite (must not be the original)"},
			"original":   map[string]any{"type": "string", "description": "URL of the original; refused when equal to base"},
			"root":       map[string]any{"type": "string", "description": "Go module root for the static gate (default .)"},
			"units":      map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "recording.json=source-dir pairs to check for drift"},
			"skip_tests": map[string]any{"type": "boolean", "description": "skip go test in the gate (default true here)"},
		}, "app", "base"),
		Call: func(args json.RawMessage) (string, error) {
			var a struct {
				App       string   `json:"app"`
				Base      string   `json:"base"`
				Original  string   `json:"original"`
				Root      string   `json:"root"`
				Units     []string `json:"units"`
				SkipTests *bool    `json:"skip_tests"`
			}
			if err := decode(args, &a); err != nil {
				return "", err
			}
			if a.App == "" || a.Base == "" {
				return "", errors.New("lift: app and base are required")
			}
			skip := true
			if a.SkipTests != nil {
				skip = *a.SkipTests
			}
			var report strings.Builder
			var blockers []string
			if a.Original != "" && a.Original == a.Base {
				blockers = append(blockers, "replay target is the original itself: point base at the rewrite")
			}
			rec, err := store.LoadApp(a.App)
			if err != nil {
				return "", err
			}
			v, err := usecase.AppReplay(rec, app.Runner{BaseURL: a.Base})
			if err != nil {
				return "", err
			}
			fmt.Fprintf(&report, "replay: %d of %d app cases match (%s)\n", v.Matched, v.Total, a.Base)

			if len(a.Units) > 0 {
				var targets []usecase.Target
				for _, u := range a.Units {
					rp, src, ok := strings.Cut(u, "=")
					if !ok || rp == "" || src == "" {
						return "", fmt.Errorf("lift: unit %q must look like recording.json=source-dir", u)
					}
					targets = append(targets, usecase.Target{Recording: rp, Source: src})
				}
				for _, r := range usecase.Map(targets, func(p string) (domain.Recording, error) {
					return (store.FileStore{}).Load(p)
				}) {
					fmt.Fprintf(&report, "unit %s: drift status %s\n", r.Unit, r.Status)
					switch r.Status {
					case usecase.StatusDrifted:
						blockers = append(blockers, fmt.Sprintf("unit %s: its source changed since the recording; record it again", r.Unit))
					case usecase.StatusOK:
					default:
						blockers = append(blockers, fmt.Sprintf("unit %s: cannot be checked (%s %s)", r.Unit, r.Status, r.Detail))
					}
				}
			}

			checks := gatecheck.Run(gatecheck.Options{Root: orDefault(a.Root, "."), SkipTests: skip})
			report.WriteString(formatChecks(checks))
			// Units are judged by drift above. LiftBlockers would read them as behavior mismatches, so it
			// gets only the replay and the gate.
			blockers = append(blockers, usecase.LiftBlockers(usecase.LiftInput{
				ReplayTotal: v.Total, ReplayMatched: v.Matched, GatePassed: domain.GatePassed(checks),
			})...)

			if len(blockers) > 0 {
				report.WriteString("lift: not ready\n")
				for _, b := range blockers {
					fmt.Fprintf(&report, "  - %s\n", b)
				}
				return report.String(), fmt.Errorf("lift: %d blocker(s)", len(blockers))
			}
			report.WriteString("lift: no blockers found. Unit behavior was not run here; the command-line lift checks it.\n")
			return report.String(), nil
		},
	})
}
