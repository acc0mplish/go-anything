package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/go-anything/go-anything/internal/adapter/mcp"
	"github.com/go-anything/go-anything/internal/adapter/store"
	"github.com/go-anything/go-anything/internal/adapter/subject"
	"github.com/go-anything/go-anything/internal/domain"
	"github.com/go-anything/go-anything/internal/usecase"
)

// registerRecordTools adds the tools that record behavior and time a rewrite. They run only the
// programs the caller names, the same way the command line does. Tools that build, start programs or
// run an agent's shell command (lift, wire verify, wire refine, profile) are not exposed here.
func registerRecordTools(srv *mcp.Server) {
	srv.Register(mcp.Tool{
		Name:        "record",
		Description: "Record a program's behavior: each input line is run through the program (argv) and its output or failure is saved as a recording.",
		Schema: schema(map[string]any{
			"out":    map[string]any{"type": "string", "description": "recording file to write (.json)"},
			"argv":   map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "the original program's command"},
			"inputs": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "one input per entry; each is one line on stdin"},
		}, "out", "argv", "inputs"),
		Call: func(args json.RawMessage) (string, error) {
			var a struct {
				Out    string   `json:"out"`
				Argv   []string `json:"argv"`
				Inputs []string `json:"inputs"`
			}
			if err := decode(args, &a); err != nil {
				return "", err
			}
			if a.Out == "" || len(a.Argv) == 0 || len(a.Inputs) == 0 {
				return "", errors.New("record: out, argv and inputs are required")
			}
			inputs := map[string]string{}
			for i, in := range a.Inputs {
				if strings.ContainsAny(in, "\r\n") {
					return "", fmt.Errorf("record: input %d spans lines; one input is one line", i+1)
				}
				if in == "" {
					continue
				}
				inputs[fmt.Sprintf("line-%d", i+1)] = in
			}
			rec, err := usecase.Record(strings.Join(a.Argv, " "), inputs, subject.Exec{Argv: a.Argv})
			if err != nil {
				return "", err
			}
			if err := (store.FileStore{}).Save(a.Out, rec); err != nil {
				return "", err
			}
			return fmt.Sprintf("recorded %d cases to %s", len(rec.Cases), a.Out), nil
		},
	})

	srv.Register(mcp.Tool{
		Name:        "bench",
		Description: "Time a recording's cases on the original and on the rewrite. Timing counts only when every output matches; otherwise the result is an error.",
		Schema: schema(map[string]any{
			"recording": map[string]any{"type": "string"},
			"original":  map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"rewrite":   map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"runs":      map[string]any{"type": "integer", "description": "repetitions per case (default 5)"},
		}, "recording", "original", "rewrite"),
		Call: func(args json.RawMessage) (string, error) {
			var a struct {
				Recording string   `json:"recording"`
				Original  []string `json:"original"`
				Rewrite   []string `json:"rewrite"`
				Runs      int      `json:"runs"`
			}
			if err := decode(args, &a); err != nil {
				return "", err
			}
			if len(a.Original) == 0 || len(a.Rewrite) == 0 {
				return "", errors.New("bench: original and rewrite argv are required")
			}
			runs := a.Runs
			if runs < 1 {
				runs = 5
			}
			rec, err := (store.FileStore{}).Load(a.Recording)
			if err != nil {
				return "", err
			}
			res, err := usecase.Bench(rec, subject.Exec{Argv: a.Original}, subject.Exec{Argv: a.Rewrite}, runs)
			if err != nil {
				return "", err
			}
			text := fmt.Sprintf("cases=%d runs=%d original=%s rewrite=%s speedup=%.2fx outputs_match=%t",
				res.Cases, res.Runs, res.OriginalMed, res.RewriteMed, res.Speedup, res.OutputsMatch)
			if !res.OutputsMatch {
				return text, errors.New("bench: outputs differ; timing is not a valid result until they match")
			}
			return text, nil
		},
	})

	srv.Register(mcp.Tool{
		Name:        "app_record",
		Description: "Record a whole program: run each HTTP or command-line case in a cases file against a running service (base) or a program (cli) and save the answers.",
		Schema: schema(map[string]any{
			"cases":  map[string]any{"type": "string", "description": "JSON file with an array of cases"},
			"out":    map[string]any{"type": "string", "description": "app recording file to write (.json)"},
			"base":   map[string]any{"type": "string", "description": "base URL of an HTTP service"},
			"cli":    map[string]any{"type": "string", "description": "command-line program"},
			"repeat": map[string]any{"type": "integer", "description": "runs per case (default 2)"},
		}, "cases", "out"),
		Call: func(args json.RawMessage) (string, error) {
			var a struct {
				Cases  string `json:"cases"`
				Out    string `json:"out"`
				Base   string `json:"base"`
				CLI    string `json:"cli"`
				Repeat int    `json:"repeat"`
			}
			if err := decode(args, &a); err != nil {
				return "", err
			}
			if a.Cases == "" || a.Out == "" {
				return "", errors.New("app_record: cases and out are required")
			}
			data, err := os.ReadFile(a.Cases)
			if err != nil {
				return "", err
			}
			var in []domain.AppCase
			if err := json.Unmarshal(data, &in); err != nil {
				return "", fmt.Errorf("app_record: parse cases: %w", err)
			}
			runner, err := appRunner(a.Base, a.CLI)
			if err != nil {
				return "", err
			}
			repeat := a.Repeat
			if repeat < 1 {
				repeat = 2
			}
			rec, err := usecase.AppRecordFull(subjectName(a.Base, a.CLI), in, runner, usecase.RecordOptions{Repeat: repeat})
			if err != nil {
				return "", err
			}
			if err := store.SaveApp(a.Out, rec); err != nil {
				return "", err
			}
			unstable := 0
			for _, c := range rec.Cases {
				if len(c.Unstable) > 0 {
					unstable++
				}
			}
			return fmt.Sprintf("recorded %d app cases to %s; %d vary between runs and are not compared on replay", len(rec.Cases), a.Out, unstable), nil
		},
	})
}
