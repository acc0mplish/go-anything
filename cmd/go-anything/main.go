// Command go-anything records an original program's behavior and verifies a rewrite against it.
// main is the composition root: it is the only place that wires adapters to use cases.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/go-anything/go-anything/internal/adapter/analyze"
	"github.com/go-anything/go-anything/internal/adapter/app"
	"github.com/go-anything/go-anything/internal/adapter/capture"
	"github.com/go-anything/go-anything/internal/adapter/cover"
	"github.com/go-anything/go-anything/internal/adapter/dbsnap"
	"github.com/go-anything/go-anything/internal/adapter/doctor"
	"github.com/go-anything/go-anything/internal/adapter/frontdoor"
	"github.com/go-anything/go-anything/internal/adapter/gatecheck"
	"github.com/go-anything/go-anything/internal/adapter/mailcap"
	"github.com/go-anything/go-anything/internal/adapter/mcp"
	"github.com/go-anything/go-anything/internal/adapter/routesfile"
	"github.com/go-anything/go-anything/internal/adapter/settings"
	"github.com/go-anything/go-anything/internal/adapter/setup"
	"github.com/go-anything/go-anything/internal/adapter/store"
	"github.com/go-anything/go-anything/internal/adapter/subject"
	"github.com/go-anything/go-anything/internal/adapter/wiregen"
	"github.com/go-anything/go-anything/internal/domain"
	"github.com/go-anything/go-anything/internal/harness"
	"github.com/go-anything/go-anything/internal/usecase"
)

// version is a var, not a const, so a release build can set it with -ldflags "-X main.version=v1.2.3".
var version = "0.40.0"

const usage = `go-anything: record an original program, verify a rewrite against it, and analyze Go source.

Usage:
  go-anything record  -o <recording.json> [-timeout 30s] [-src <dir>] -- <original cmd> [args...]
  go-anything record  -o <recording.json> -build <pkg> [-src <dir>] [-cover-dir <dir>]
      Builds a Go package with coverage instrumentation, records through it, and saves the statement
      coverage the cases reached (measured by the Go toolchain, not estimated).
      Reads inputs from stdin (one per line; each line is one case).
  go-anything verify  <recording.json> [-timeout 30s] [-src <dir>] [-report <file.md|.json|.html>] -- <rewrite cmd> [args...]
      Replays every recorded case against the rewrite. Exits non-zero unless all match (the gate).
      With -src, fails if the original's source changed since record (stale recordings never pass).
  go-anything analyze [root]
      Reports packages, imports and functions of a Go module as JSON. No code is executed.
  go-anything bench   <recording.json> [-runs 5] -- <original cmd> -- <rewrite cmd>
      Times each case on both programs (median). Timing counts only if every output matches.
  go-anything shadow  <recording.json> -- <original cmd> -- <rewrite cmd>
  go-anything shadow gen -module-root <dir> -original <import path> -rewrite <import path> [-out <dir>] [-dry-run]
      Generates a Go package with the original package's API. GA_SHADOW_<PKG>=old|shadow|new picks the
      implementation per call; shadow returns the original's results and logs mismatches to GA_SHADOW_LOG.
      Runs both, returns the original's output, reports where the rewrite disagrees.
  go-anything from-tests -o <recording.json> -- go test -json ./...
      Records a case per Go test from 'go test -json' output (failures become error cases).
  go-anything setup   [-apply] [-agent claude|cursor|gemini|opencode|codex] [-skill=false]
      Registers the MCP server and the skill with agent apps. Dry-run unless -apply.
  go-anything map -unit <recording.json>=<source dir> [-unit ...] [-discover <module root>]
      Reports each unit's drift state (ok, drifted, no-fingerprint, missing-source, error).
      -discover also lists packages no recording covers, most valuable first (lines, then narrow API).
      Exits non-zero if a unit cannot be trusted; unported packages are listed, not failed.
  go-anything profile -pkg <pkg> [-bench <regexp>] [-cpuprofile <file>]
      Runs the package's benchmarks under the Go CPU profiler and ranks the functions that take the
      time. Each hot function is marked: 'port' when its package is already recorded, 'record first'
      when it is not (record the behavior before rewriting). Uses the official go tool pprof.
  go-anything profile -url <base> -cases <app.json> [-seconds 10] [-bin <binary>] [-cpuprofile <file>]
      Profiles a running service: fetches its CPU profile from /debug/pprof/profile (the service must import
      net/http/pprof) while the app cases replay against it, then ranks the functions the same way.
  go-anything app record -cases <cases.json> -o <app.json> (-base <url> | -cli <program>) [-repeat 2] [-strict] [-webhooks addr] [-mail addr] [-db ... -tables a,b [-tenant <id>]]
      Records a whole program: each case is an HTTP request or a CLI invocation; the answer is saved.
  go-anything app replay <app.json> (-base <url> | -cli <program>) [-db ... -tables ... [-tenant <id>]]
      Sends every recorded request again. Exits non-zero unless every answer matches.
  go-anything wire gen <module root> <unit> [prefix e.g. /orders/] [-main main.go] [-no-main] [-cases app.json] [-agent "<cmd>"]
      With a prefix the unit is an HTTP unit; without one it is a command-line unit named after the unit.
      Writes glue code for one unit (units/<unit>/*.go and the registry units/units.go), adds the mount to
      main.go, and checks that the module builds. If the build fails, main.go is restored unchanged.
      The rewrite is a placeholder that gate refuses. Each unit serves the original (through GA_ORIGINAL_URL)
      until GA_UNIT_<NAME>=rewrite is set. Never overwrites an existing unit or a hand-written registry.
  go-anything wire refine <module root> <unit> [prefix] -cases app.json (-original-cli BIN | -original-url URL) [-agent CMD] [-rounds 4] [-probes 40] [-max-new 20] [-rbac-principals FILE -rbac-policy FILE]
      Probes the rewrite with fresh inputs against the original. Every input where they disagree is recorded with
      the original's answer; the unit's test is regenerated and the agent fixes the rewrite. Stops when a round
      finds no disagreement.
      The defect policy (an original crash is reported, or recorded as expected) is read from
      <module root>/.go-anything/config.json; a missing file means the defaults. Precedence, strongest first:
      -defect-policy defect|expected, the unit's entry in unit_defect_policy, the file's defect_policy, then defect.
  go-anything wire verify <module root> <unit> [prefix] -cases app.json [-original-cli BIN | -original-url URL] [-main main.go]
      Builds the program, sends only this unit to its rewrite (GA_UNIT_<NAME>=rewrite), starts it (HTTP) or runs
      it (command-line), and replays the unit's recorded cases. Stops the program afterwards. Exits non-zero on mismatch.
  go-anything wire status <routes.json> [-root <module root>]
      With -root, also prints each unit's effective defect policy and whether the unit entry, the file default or
      the built-in default decided it. The -defect-policy flag only applies to wire refine.
  go-anything wire set <routes.json> <unit> <rewrite|original>
      Switches every route of one unit. A running front door started with -routes-file picks the change up
      on the next request, so one unit can go back to the original without restarting anything.
  go-anything frontdoor -listen <addr> -original <url> [-rewrite <url>] -route <METHOD|*>:<prefix>=<rewrite|original> ...
  go-anything frontdoor -listen <addr> -original <url> [-rewrite <url>] -routes-file <routes.json>
  go-anything frontdoor scaffold <repo> -cases <app.json> -original-cmd "<cmd>" [-port-env PORT] [-listen addr] [-check=true]
      Writes <repo>/go/app, a standard-library Go entry point that sends everything to the original until
      wire gen mounts units into it. Never overwrites. -check builds it and replays the cases.
  go-anything package <module root> [-targets linux/amd64,...] [-dry-run] [-check]
      Writes a release workflow, scripts/build-all.sh and docs/INSTALL.md; never overwrites, never publishes.
      -check cross-compiles every main for every target with CGO_ENABLED=0 on this machine.
      Serves HTTP on <addr>, sending each request to the original or the rewrite by route rule.
      Unlisted requests go to the original. A rewrite that is down returns 502, never a silent fallback.
      Each response carries X-Served-By so the migration's progress is visible.
  go-anything fuzz [-n 200] [-seed <n>] [-timeout 30s] -- <original cmd> -- <rewrite cmd>
      Live differential test: fresh inputs (not in any recording) go through both programs. Prints the
      seed; the same seed replays the same inputs. Exits non-zero on any disagreement.
  go-anything lift -app <app.json> -base <rewrite url> [-original <original url>] [-root <dir>] [-units <units.json>] [-skip-tests]
      Decides whether the program can be lifted to pure Go. Ready only when: every app case replays
      from -base, every unit in -units (a JSON list of {"recording","source","command"}) still matches
      its rewrite command and has no drift, and the gate passes. Lists every blocker. Refuses when
      -base is the same as -original. It does not prove the rewrite never calls the original internally.
  go-anything rbac -cases <cases.json> -principals <principals.json> -policy <policy.json> -base <rewrite url> [-original <url>]
      Structural authorization check. Asks every case as every principal and audits allow or deny outcomes
      against the declared policy and, with -original, against the original. Any critical finding exits non-zero.
      Tokens are read from the environment variables the principal file names. Bodies are not compared.
  go-anything secure [-root <dir>] [-out <dir>] [-report-only] [-no-html]
      Runs the security function confirmation checklist (Korean government scheme) on a Go module and
      writes checklist.json and security.html (Korean first, English second) under
      <root>/.go-anything/security by default. Manual items are listed, not judged. Missing external
      tools (govulncheck) skip with a note. Exits non-zero only when a mandatory item fails and
      -report-only was not given. Opt-in everywhere: no other command runs this unless asked.
  go-anything commands
      Lists every command, which ones an agent can reach through MCP, and why the others are not exposed.
  go-anything gate [-root <dir>] [-skip-tests] [-app <app.json> -base <url>]
      Completion gate for a Go project: gofmt, go vet, go build, go test, no placeholder bodies
      (panic "not implemented"), every unsafe use justified by a SAFETY comment. With -app, the
      recorded behavior must also replay against -base. Exits non-zero unless every check passes.
  go-anything doctor [-project <dir>]
      Checks the toolchain, the go tools coverage and profile need, agent connections and skills,
      the report folder and the webhook port. Read-only. Exits non-zero only when something is broken.
  go-anything coverage <coverprofile>
      Reports statement coverage from a 'go test -coverprofile' file, as JSON.
  go-anything mcp
      Serves analyze and verify as MCP tools over stdio (JSON-RPC 2.0).
`

func main() {
	if err := run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return errors.New("missing command")
	}
	switch args[0] {
	case "record":
		return cmdRecord(args[1:], stdin, stdout)
	case "verify":
		return cmdVerify(args[1:], stdout)
	case "analyze":
		return cmdAnalyze(args[1:], stdout)
	case "bench":
		return cmdBench(args[1:], stdout)
	case "shadow":
		return cmdShadowEntry(args[1:], stdout)
	case "from-tests":
		return cmdFromTests(args[1:], stdout)
	case "setup":
		return cmdSetup(args[1:], stdout)
	case "coverage":
		return cmdCoverage(args[1:], stdout)
	case "map":
		return cmdMap(args[1:], stdout)
	case "profile":
		return cmdProfile(args[1:], stdout)
	case "app":
		return cmdApp(args[1:], stdout)
	case "doctor":
		return cmdDoctor(args[1:], stdout)
	case "gate":
		return cmdGate(args[1:], stdout)
	case "secure":
		return cmdSecure(args[1:], stdout)
	case "commands":
		return cmdCommands(stdout)
	case "rbac":
		return cmdRBAC(args[1:], stdout)
	case "fuzz":
		return cmdFuzz(args[1:], stdout)
	case "lift":
		return cmdLift(args[1:], stdout)
	case "wire":
		return cmdWire(args[1:], stdout)
	case "frontdoor":
		return cmdFrontDoorEntry(args[1:], stdout)
	case "package":
		return cmdPackage(args[1:], stdout)
	case "mcp":
		return serveMCP(stdin, stdout)
	case "version", "-v", "--version":
		fmt.Fprintln(stdout, version)
		return nil
	case "help", "-h", "--help":
		fmt.Fprint(stdout, usage)
		return nil
	default:
		fmt.Fprint(stderr, usage)
		return fmt.Errorf("unknown command %q", args[0])
	}
}

// cmdShadowEntry keeps the original `shadow <recording> -- a -- b` form and adds `shadow gen`.
func cmdShadowEntry(args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return cmdShadow(args, stdout)
	}
	switch args[0] {
	case "gen":
		return cmdShadowGen(args[1:], stdout)
	default:
		return cmdShadow(args, stdout)
	}
}

// cmdFrontDoorEntry keeps the runtime proxy form and adds `frontdoor scaffold`.
func cmdFrontDoorEntry(args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return cmdFrontDoor(args, stdout)
	}
	switch args[0] {
	case "scaffold":
		return cmdFrontdoorScaffold(args[1:], stdout)
	default:
		return cmdFrontDoor(args, stdout)
	}
}

// splitCommand separates flags from the program after "--".
func splitCommand(args []string) (flagsPart, argv []string, err error) {
	for i, a := range args {
		if a == "--" {
			return args[:i], args[i+1:], nil
		}
	}
	return nil, nil, errors.New("missing `--` before the program to run")
}

func cmdRecord(args []string, stdin io.Reader, stdout io.Writer) error {
	// `-build` needs no program after `--`; the plain form does.
	flagsPart, argv, err := splitCommand(args)
	if err != nil {
		if !hasFlag(args, "-build") {
			return err
		}
		flagsPart, argv = args, nil
	}
	fs := flag.NewFlagSet("record", flag.ContinueOnError)
	out := fs.String("o", "", "output recording file")
	timeout := fs.Duration("timeout", 30*time.Second, "per-case timeout")
	src := fs.String("src", "", "original's source directory; its fingerprint is saved so later changes are detected")
	build := fs.String("build", "", "Go package to build with coverage instrumentation (e.g. ./cmd/app); the command after -- is then ignored")
	coverDir := fs.String("cover-dir", "", "where coverage counters go (default: a new temp dir); must be empty or absent")
	edges := fs.Int("edges", 0, "also record this many generated edge-case inputs (boundary values programs often get wrong)")
	edgeSeed := fs.Uint64("edge-seed", 1, "seed for -edges (the same seed gives the same inputs)")
	if err := fs.Parse(flagsPart); err != nil {
		return err
	}
	if *out == "" {
		return errors.New("record: -o is required")
	}
	inputs, err := readInputs(stdin)
	if err != nil {
		return err
	}
	if *edges > 0 {
		added := 0
		for i, v := range domain.ProbeValues(*edgeSeed, *edges) {
			key := fmt.Sprintf("edge-%d", i+1)
			inputs[key] = v
			added++
		}
		fmt.Fprintf(stdout, "added %d generated edge-case input(s)\n", added)
	}
	var rec domain.Recording
	if *build != "" {
		rec, err = recordWithCoverage(*build, *coverDir, *timeout, inputs)
	} else {
		if len(argv) == 0 {
			return errors.New("record: give the original command after `--`, or use -build <pkg>")
		}
		rec, err = usecase.Record(strings.Join(argv, " "), inputs, subject.Exec{Argv: argv, Timeout: *timeout})
	}
	if err != nil {
		return err
	}
	if *src != "" {
		fp, err := domain.FingerprintDir(*src)
		if err != nil {
			return err
		}
		rec.Source = &fp
	}
	var s usecase.RecordingStore = store.FileStore{}
	if err := s.Save(*out, rec); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "recorded %d cases to %s\n", len(rec.Cases), *out)
	if rec.Coverage != nil {
		fmt.Fprintf(stdout, "original statement coverage from these cases: %.1f%% (%d of %d)\n",
			rec.Coverage.Percent, rec.Coverage.Covered, rec.Coverage.Statements)
	}
	return nil
}

// recordWithCoverage builds the original with instrumentation, records every input through it,
// and merges the counters the runs wrote into the recording's coverage.
func recordWithCoverage(pkg, coverDir string, timeout time.Duration, inputs map[string]string) (domain.Recording, error) {
	dir, err := os.MkdirTemp("", "go-anything-build-*")
	if err != nil {
		return domain.Recording{}, fmt.Errorf("record: temp dir: %w", err)
	}
	defer os.RemoveAll(dir)
	bin := filepath.Join(dir, "original")
	if err := cover.Build(pkg, bin); err != nil {
		return domain.Recording{}, err
	}
	if coverDir == "" {
		coverDir = filepath.Join(dir, "counters")
	}
	col, err := cover.NewCollector(coverDir)
	if err != nil {
		return domain.Recording{}, err
	}
	original := subject.Exec{Argv: []string{bin}, Timeout: timeout, Env: col.Env()}
	rec, err := usecase.Record(pkg, inputs, original)
	if err != nil {
		return domain.Recording{}, err
	}
	// Subject must be the import path, not the path typed on the command line: `map` matches
	// recordings to packages by import path, so a relative path would look unrecorded.
	importPath, err := cover.ImportPath("", pkg)
	if err != nil {
		return domain.Recording{}, err
	}
	rec.Subject = importPath
	ctx, cancel := context.WithTimeout(context.Background(), cover.Timeout)
	defer cancel()
	c, err := col.Profile(ctx)
	if err != nil {
		return domain.Recording{}, err
	}
	rec.Coverage = &c
	return rec, nil
}

// hasFlag reports whether a flag (with or without a value) appears in args.
func hasFlag(args []string, name string) bool {
	for _, a := range args {
		if a == name || strings.HasPrefix(a, name+"=") {
			return true
		}
	}
	return false
}

func cmdVerify(args []string, stdout io.Writer) error {
	flagsPart, argv, err := splitCommand(args)
	if err != nil {
		return err
	}
	fs := flag.NewFlagSet("verify", flag.ContinueOnError)
	timeout := fs.Duration("timeout", 30*time.Second, "per-case timeout")
	src := fs.String("src", "", "original's source directory; fails the gate if it changed since record")
	reportPath := fs.String("report", "", "also write a report: .md, .json or .html")
	compareOpts := compareFlags(fs)
	pos, err := parseInterspersed(fs, flagsPart)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return errors.New("verify: exactly one recording file is required before `--`")
	}
	opt, err := compareOpts("verify")
	if err != nil {
		return err
	}
	rec, err := runVerifyWith(pos[0], subject.Exec{Argv: argv, Timeout: *timeout}, *src, opt)
	if err != nil {
		return err
	}
	if *reportPath != "" {
		var s usecase.RecordingStore = store.FileStore{}
		recording, err := s.Load(pos[0])
		if err != nil {
			return err
		}
		r := usecase.UnitReport(rec, *src != "", coverageOf(recording))
		if err := writeReport(*reportPath, r, domain.RedactCommand(argv), pos[0], opt); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "report written to %s\n", *reportPath)
	}
	return finishVerify(stdout, rec)
}

// compareFlags registers -json-body and -float-tolerance on fs. The returned function, called after
// parsing, gives the options or a flag error naming cmd when the tolerance is not finite and >= 0.
func compareFlags(fs *flag.FlagSet) func(cmd string) (domain.CompareOptions, error) {
	jsonBody := fs.Bool("json-body", false, "compare HTTP bodies, CLI stdout and unit outputs as JSON when both sides parse: key order and whitespace ignored, arrays ordered, numbers by exact decimal value")
	tol := fs.Float64("float-tolerance", 0, "numbers (in JSON answers with -json-body, and lines that are a single number) may differ by this much, absolute or relative; 0 is exact")
	var norms unitFlags
	fs.Var(&norms, "normalize", "mask volatile values on both sides before comparing: timestamps, uuids, or name=regex (repeatable); a differing clock reading or fresh UUID then compares as equal")
	return func(cmd string) (domain.CompareOptions, error) {
		opt := domain.CompareOptions{JSONBody: *jsonBody, FloatTolerance: *tol, Normalize: norms}
		if err := opt.Validate(); err != nil {
			return domain.CompareOptions{}, fmt.Errorf("%s: %v", cmd, err)
		}
		return opt, nil
	}
}

// printAppMismatch prints one difference. When the comparison named the first difference (a JSON path
// or a line), that is printed instead of both whole values.
func printAppMismatch(w io.Writer, m usecase.AppMismatch) {
	if m.Detail != "" {
		fmt.Fprintf(w, "  %s: %s differs at %s\n", m.Case, m.What, m.Detail)
		return
	}
	fmt.Fprintf(w, "  %s: %s differs: want %q, got %q\n", m.Case, m.What, m.Want, m.Got)
}

// coverageOf returns the recording's coverage, or nil when it was not measured.
func coverageOf(rec domain.Recording) *domain.Coverage {
	return rec.Coverage
}

// writeReport saves a report in the format its file extension asks for. rewrite (already redacted)
// and recording name the run, so the report says what was checked, when, and by which version.
func writeReport(path string, r domain.Report, rewrite, recording string, compare domain.CompareOptions) error {
	r.Run = &domain.ReportRun{
		Rewrite:     rewrite,
		Recording:   recording,
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
		ToolVersion: version,
		Compare:     compare.Describe(),
	}
	var data []byte
	switch strings.ToLower(filepath.Ext(path)) {
	case ".md", ".markdown":
		data = []byte(r.Markdown())
	case ".json":
		b, err := r.JSON()
		if err != nil {
			return fmt.Errorf("report: encode: %w", err)
		}
		data = append(b, '\n')
	case ".html", ".htm":
		b, err := r.HTML()
		if err != nil {
			return err
		}
		data = b
	default:
		return fmt.Errorf("report: %q must end in .md, .json or .html", path)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("report: write %s: %w", path, err)
	}
	return nil
}

// splitTwo splits "flags -- A... -- B..." into flags, A and B.
func splitTwo(args []string) (flagsPart, a, b []string, err error) {
	var seps []int
	for i, x := range args {
		if x == "--" {
			seps = append(seps, i)
		}
	}
	if len(seps) != 2 {
		return nil, nil, nil, errors.New("expected: <flags> -- <original cmd> -- <rewrite cmd>")
	}
	flagsPart = args[:seps[0]]
	a = args[seps[0]+1 : seps[1]]
	b = args[seps[1]+1:]
	if len(a) == 0 || len(b) == 0 {
		return nil, nil, nil, errors.New("both the original and the rewrite command are required")
	}
	return flagsPart, a, b, nil
}

func loadRecording(path string) (domain.Recording, error) {
	var s usecase.RecordingStore = store.FileStore{}
	return s.Load(path)
}

func cmdBench(args []string, stdout io.Writer) error {
	flagsPart, orig, rew, err := splitTwo(args)
	if err != nil {
		return err
	}
	fs := flag.NewFlagSet("bench", flag.ContinueOnError)
	runs := fs.Int("runs", 5, "repetitions per case (median is reported)")
	timeout := fs.Duration("timeout", 30*time.Second, "per-case timeout")
	if err := fs.Parse(flagsPart); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("bench: exactly one recording file is required; put flags before it, e.g. `bench -runs 3 rec.json -- orig -- rew`")
	}
	rec, err := loadRecording(fs.Arg(0))
	if err != nil {
		return err
	}
	res, err := usecase.Bench(rec, subject.Exec{Argv: orig, Timeout: *timeout}, subject.Exec{Argv: rew, Timeout: *timeout}, *runs)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "cases=%d runs=%d original=%s rewrite=%s speedup=%.2fx outputs_match=%t\n",
		res.Cases, res.Runs, res.OriginalMed, res.RewriteMed, res.Speedup, res.OutputsMatch)
	if !res.OutputsMatch {
		return errors.New("bench: outputs differ; timing is not a valid result until they match")
	}
	return nil
}

func cmdShadow(args []string, stdout io.Writer) error {
	flagsPart, orig, rew, err := splitTwo(args)
	if err != nil {
		return err
	}
	fs := flag.NewFlagSet("shadow", flag.ContinueOnError)
	timeout := fs.Duration("timeout", 30*time.Second, "per-case timeout")
	if err := fs.Parse(flagsPart); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("shadow: exactly one recording file is required")
	}
	rec, err := loadRecording(fs.Arg(0))
	if err != nil {
		return err
	}
	res, err := usecase.Shadow(rec, subject.Exec{Argv: orig, Timeout: *timeout}, subject.Exec{Argv: rew, Timeout: *timeout})
	if err != nil {
		return err
	}
	names := make([]string, 0, len(res))
	for n := range res {
		names = append(names, n)
	}
	sort.Strings(names)
	mismatches := 0
	for _, n := range names {
		r := res[n]
		if len(r.Mismatches) > 0 {
			mismatches++
			fmt.Fprintf(stdout, "  %s: rewrite disagrees (original answered %q)\n", n, r.Output)
		}
	}
	fmt.Fprintf(stdout, "shadow: %d of %d inputs disagree; callers received the original's output\n", mismatches, len(res))
	return nil
}

func cmdFromTests(args []string, stdout io.Writer) error {
	flagsPart, argv, err := splitCommand(args)
	if err != nil {
		return err
	}
	fs := flag.NewFlagSet("from-tests", flag.ContinueOnError)
	out := fs.String("o", "", "output recording file")
	if err := fs.Parse(flagsPart); err != nil {
		return err
	}
	if *out == "" {
		return errors.New("from-tests: -o is required")
	}
	if len(argv) == 0 {
		return errors.New("from-tests: give the test command after `--`, e.g. -- go test -json ./...")
	}
	// The user chooses the command; we ensure it emits JSON so the events can be parsed.
	if !containsJSONFlag(argv) {
		return errors.New("from-tests: the test command must produce `go test -json` output (add -json)")
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stderr = os.Stderr
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("from-tests: start: %w", err)
	}
	cases, parseErr := domain.ParseGoTestJSON(pipe)
	waitErr := cmd.Wait() // a failing test run is expected here: its failures are the data
	if parseErr != nil {
		return parseErr
	}
	if len(cases) == 0 {
		return errors.New("from-tests: no test results found")
	}
	seen := map[string]int{}
	for i := range cases {
		seen[cases[i].Name]++
		if seen[cases[i].Name] > 1 {
			cases[i].Name = fmt.Sprintf("%s#%d", cases[i].Name, seen[cases[i].Name])
		}
	}
	rec := domain.Recording{Subject: strings.Join(argv, " "), Cases: cases}
	var s usecase.RecordingStore = store.FileStore{}
	if err := s.Save(*out, rec); err != nil {
		return err
	}
	_ = waitErr
	fmt.Fprintf(stdout, "recorded %d test results to %s\n", len(cases), *out)
	return nil
}

func containsJSONFlag(argv []string) bool {
	for _, a := range argv {
		if a == "-json" || a == "--json" {
			return true
		}
	}
	return false
}

func cmdSetup(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("setup", flag.ContinueOnError)
	apply := fs.Bool("apply", false, "write the changes (default is a dry run)")
	agent := fs.String("agent", "", "only this agent: claude, cursor, gemini, opencode or codex")
	skill := fs.Bool("skill", true, "also install the workflow skill where the agent reads skills")
	if err := fs.Parse(args); err != nil {
		return err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("setup: home directory: %w", err)
	}
	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("setup: locate binary: %w", err)
	}
	changes, err := setup.Run(setup.Options{Home: home, Command: self, Args: []string{"mcp"}, Apply: *apply, Only: *agent, Skill: *skill})
	for _, c := range changes {
		fmt.Fprintf(stdout, "[%s] %s %s %s", c.Action, c.Kind, c.Agent, c.Path)
		if c.Applied {
			fmt.Fprint(stdout, " (written)")
		}
		if c.Backup != "" {
			fmt.Fprintf(stdout, " backup=%s", c.Backup)
		}
		fmt.Fprintln(stdout)
	}
	if len(changes) == 0 {
		fmt.Fprintln(stdout, "no supported coding-agent app found (looked for ~/.claude, ~/.cursor, ~/.gemini, ~/.config/opencode, ~/.codex)")
	}
	if !*apply {
		fmt.Fprintln(stdout, "dry run: nothing written. Re-run with -apply to write.")
	}
	return err
}

// unitFlags collects repeated -unit recording=source values.
type unitFlags []string

func (u *unitFlags) String() string     { return strings.Join(*u, ",") }
func (u *unitFlags) Set(v string) error { *u = append(*u, v); return nil }

func cmdMap(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("map", flag.ContinueOnError)
	var units unitFlags
	fs.Var(&units, "unit", "recording.json=source-dir (repeatable)")
	discover := fs.String("discover", "", "module root: also list packages no recording covers yet, most valuable first")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if len(units) == 0 && *discover == "" {
		return errors.New("map: give -unit recording.json=source-dir and/or -discover <module root>")
	}
	targets := make([]usecase.Target, 0, len(units))
	for _, u := range units {
		rec, src, ok := strings.Cut(u, "=")
		if !ok || rec == "" || src == "" {
			return fmt.Errorf("map: -unit %q must look like recording.json=source-dir", u)
		}
		targets = append(targets, usecase.Target{Recording: rec, Source: src})
	}
	loader := func(p string) (domain.Recording, error) {
		var s usecase.RecordingStore = store.FileStore{}
		return s.Load(p)
	}
	problems := 0
	if len(targets) > 0 {
		results := usecase.Map(targets, loader)
		for _, r := range results {
			fmt.Fprintf(stdout, "[%s] %s (%d cases)", r.Status, r.Unit, r.Cases)
			if r.Detail != "" {
				fmt.Fprintf(stdout, " - %s", r.Detail)
			}
			fmt.Fprintln(stdout)
		}
		problems = usecase.Problems(results)
		fmt.Fprintf(stdout, "map: %d of %d units need attention\n", problems, len(results))
	}
	if *discover != "" {
		// Unrecorded packages are work still to do, not a failure: they are listed, never counted
		// toward the exit status. Only units that were recorded and can no longer be trusted fail.
		if _, err := reportUnported(stdout, *discover, targets, loader); err != nil {
			return err
		}
	}
	if problems > 0 {
		return fmt.Errorf("map: %d unit(s) cannot be trusted as-is", problems)
	}
	return nil
}

// reportUnported lists packages no recording covers, most valuable to port first.
// It returns how many such packages exist; those are work still to do, not failures.
func reportUnported(w io.Writer, root string, targets []usecase.Target, load func(string) (domain.Recording, error)) (int, error) {
	res, err := usecase.Analyze(analyze.GoSource{}, root)
	if err != nil {
		return 0, err
	}
	var recs []domain.Recording
	for _, t := range targets {
		r, err := load(t.Recording)
		if err != nil {
			return 0, fmt.Errorf("map: %w", err)
		}
		recs = append(recs, r)
	}
	units := make([]usecase.Unit, 0, len(res.Packages))
	for _, p := range res.Packages {
		exported := len(p.Exported)
		units = append(units, usecase.Unit{ImportPath: p.ImportPath, Dir: p.Dir, Files: len(p.Files), Lines: p.Lines, Exported: exported})
	}
	units = usecase.MarkRecorded(units, recs)
	missing := usecase.UnportedUnits(units)
	fmt.Fprintf(w, "not yet recorded (%d packages, most valuable first):\n", len(missing))
	for _, u := range missing {
		fmt.Fprintf(w, "  [unported] %s (%d lines, %d exported)\n", u.ImportPath, u.Lines, u.Exported)
	}
	return len(missing), nil
}

// hotLimit is how many functions the profile report shows; the rest add little to a decision.
const hotLimit = 10

// hotPortThreshold is the cumulative share above which a function is worth porting at all.
const hotPortThreshold = 5.0

func cmdProfile(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("profile", flag.ContinueOnError)
	pkg := fs.String("pkg", "", "package whose benchmarks to profile (e.g. ./internal/domain)")
	bench := fs.String("bench", ".", "benchmark regexp to run")
	prof := fs.String("cpuprofile", "", "where to keep the CPU profile (default: a temp file)")
	recordedFrom := fs.String("recorded", "", "comma-separated import paths already recorded (default: none)")
	liveURL := fs.String("url", "", "live service to profile, e.g. http://127.0.0.1:8080 (needs net/http/pprof mounted)")
	cases := fs.String("cases", "", "app recording replayed against -url while the profile is taken")
	seconds := fs.Int("seconds", 10, "how long the live CPU profile samples (with -url)")
	liveBin := fs.String("bin", "", "optional binary for pprof symbols (with -url)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *liveURL != "" {
		if *pkg != "" {
			return errors.New("profile: give -pkg (benchmarks) or -url (live service), not both")
		}
		return cmdProfileLive(liveOptions{URL: *liveURL, Cases: *cases, Seconds: *seconds, Bin: *liveBin, Keep: *prof, Recorded: recordedSet(*recordedFrom)}, stdout)
	}
	if *cases != "" || *liveBin != "" {
		return errors.New("profile: -cases and -bin need -url")
	}
	if *pkg == "" {
		return errors.New("profile: -pkg or -url is required")
	}
	dir, err := os.MkdirTemp("", "go-anything-profile-*")
	if err != nil {
		return fmt.Errorf("profile: temp dir: %w", err)
	}
	defer os.RemoveAll(dir)
	cpu := *prof
	if cpu == "" {
		cpu = filepath.Join(dir, "cpu.out")
	}
	bin := filepath.Join(dir, "pkg.test")
	// -o keeps the test binary, which pprof needs to resolve symbols.
	run := exec.Command("go", "test", "-run", "^$", "-bench", *bench, "-cpuprofile", cpu, "-o", bin, *pkg)
	var benchOut, stderr bytes.Buffer
	run.Stdout = &benchOut
	run.Stderr = &stderr
	if err := run.Run(); err != nil {
		return fmt.Errorf("profile: go test failed (%s): %w: %s", *pkg, err, stderr.String())
	}
	// A benchmark result line proves something ran. Without one, "nothing is hot" would be a lie.
	if !strings.Contains(benchOut.String(), "ns/op") {
		return fmt.Errorf("profile: no benchmark matched %q in %s; add a Benchmark function or change -bench", *bench, *pkg)
	}
	top, err := exec.Command("go", "tool", "pprof", "-top", bin, cpu).Output()
	if err != nil {
		return fmt.Errorf("profile: pprof: %w", err)
	}
	hot, err := domain.ParseTop(bytes.NewReader(top))
	if err != nil {
		return err
	}
	printHot(stdout, "hot functions in "+*pkg, hot, recordedSet(*recordedFrom))
	return nil
}

// recordedSet turns the -recorded list into a set of import paths.
func recordedSet(list string) map[string]bool {
	recorded := map[string]bool{}
	for _, p := range strings.Split(list, ",") {
		if p = strings.TrimSpace(p); p != "" {
			recorded[p] = true
		}
	}
	return recorded
}

// printHot writes the hottest functions under a title, for both the benchmark and the live mode.
func printHot(stdout io.Writer, title string, hot []domain.HotFunc, recorded map[string]bool) {
	fmt.Fprintf(stdout, "%s (cumulative share):\n", title)
	shown := 0
	for _, h := range hot {
		if shown == hotLimit {
			break
		}
		if h.CumPct < hotPortThreshold && shown > 0 {
			break
		}
		fmt.Fprintf(stdout, "  %6.2f%% cum  %6.2f%% flat  %-9s %s\n", h.CumPct, h.FlatPct, recommend(h, recorded), h.Name)
		shown++
	}
	if shown == 0 {
		fmt.Fprintln(stdout, "  (no function above the threshold; nothing worth porting here)")
	}
}

// recommend says what to do with a hot function. Porting without a recording of its
// behavior would be unverified, so unrecorded hot code is "record first".
func recommend(h domain.HotFunc, recorded map[string]bool) string {
	if h.CumPct < hotPortThreshold {
		return "skip"
	}
	if pkgOf(h.Name) != "" && recorded[pkgOf(h.Name)] {
		return "port"
	}
	return "record first"
}

// pkgOf returns the import path part of a pprof function name ("path/to/pkg.Func" -> "path/to/pkg").
func pkgOf(name string) string {
	slash := strings.LastIndex(name, "/")
	dot := strings.Index(name[slash+1:], ".")
	if dot < 0 {
		return ""
	}
	return name[:slash+1+dot]
}

// appRunner builds the runner for -base (HTTP server) or -cli (command-line program).
func appRunner(base, cli string) (usecase.AppRunner, error) {
	switch {
	case base != "" && cli != "":
		return nil, errors.New("app: give -base or -cli, not both")
	case base != "":
		return app.Runner{BaseURL: base}, nil
	case cli != "":
		return app.Runner{Argv0: cli}, nil
	}
	return nil, errors.New("app: give -base <url> for an HTTP program or -cli <program>")
}

func cmdApp(args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return errors.New("app: expected `record` or `replay`")
	}
	switch args[0] {
	case "record":
		return cmdAppRecord(args[1:], stdout)
	case "replay":
		return cmdAppReplay(args[1:], stdout)
	}
	return fmt.Errorf("app: unknown subcommand %q (want record or replay)", args[0])
}

// parseInterspersed parses flags that may appear before or after positional arguments.
// Go's flag package stops at the first non-flag, which silently ignored flags written after a
// file name; this keeps reading flags until none are left.
func parseInterspersed(fs *flag.FlagSet, args []string) ([]string, error) {
	var positional []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		if fs.NArg() == 0 {
			return positional, nil
		}
		positional = append(positional, fs.Arg(0))
		args = fs.Args()[1:]
	}
}

func cmdAppRecord(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("app record", flag.ContinueOnError)
	cases := fs.String("cases", "", "JSON array of cases to run")
	out := fs.String("o", "", "output app recording file")
	base := fs.String("base", "", "base URL of an HTTP program")
	cli := fs.String("cli", "", "command-line program to run each CLI case with")
	webhooks := fs.String("webhooks", "", "capture the outbound calls the program makes: 127.0.0.1:<port> (fixed, so the program can be started with it)")
	repeat := fs.Int("repeat", 2, "runs per case; answers that differ between runs are marked unstable")
	strict := fs.Bool("strict", false, "fail the recording when any case is unstable")
	dbPath := fs.String("db", "", "SQLite database to watch for row changes (read-only)")
	dbTables := fs.String("tables", "", "comma-separated tables to watch with -db")
	dbTenant := fs.String("tenant", "", "tenant id for PostgreSQL row-level security (sets app.current_tenant_id per snapshot)")
	mailAddr := fs.String("mail", "", "capture the mail the program sends: 127.0.0.1:<port> (point the program's SMTP server here)")
	dbReset := fs.String("db-reset", "", "SQL file run against -db before EVERY case (destructive; needs -allow-db-write; scratch databases only)")
	dbDump := fs.String("db-dump", "", "write the final state of -tables to this JSON file (read-only)")
	allowWrite := fs.Bool("allow-db-write", false, "allow -db-reset to change data")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *cases == "" || *out == "" {
		return errors.New("app record: -cases and -o are required")
	}
	data, err := os.ReadFile(*cases)
	if err != nil {
		return fmt.Errorf("app record: read cases: %w", err)
	}
	var in []domain.AppCase
	if err := json.Unmarshal(data, &in); err != nil {
		return fmt.Errorf("app record: parse cases: %w", err)
	}
	runner, err := appRunner(*base, *cli)
	if err != nil {
		return err
	}
	src, closeSrc, err := combinedSource(*webhooks, *mailAddr, *dbPath, *dbTables, *dbTenant)
	if err != nil {
		return err
	}
	defer closeSrc()
	life, err := setupLifecycle(*dbPath, *dbTables, *dbTenant, *dbReset, *dbDump, *allowWrite)
	if err != nil {
		return err
	}
	defer life.close()
	rec, err := usecase.AppRecordFull(subjectName(*base, *cli), in, runner, usecase.RecordOptions{Src: src, Before: life.before, Repeat: *repeat})
	if err != nil {
		return err
	}
	if err := life.dump(); err != nil {
		return err
	}
	unstable := 0
	for _, c := range rec.Cases {
		if len(c.Unstable) > 0 {
			unstable++
			fmt.Fprintf(stdout, "  unstable: %s varies between runs: %s (not compared on replay)\n", c.Name, strings.Join(c.Unstable, ", "))
		}
	}
	if unstable > 0 && *strict {
		return fmt.Errorf("app record: %d case(s) vary between runs; fix the program or drop -strict", unstable)
	}
	if err := store.SaveApp(*out, rec); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "recorded %d app cases to %s\n", len(rec.Cases), *out)
	return nil
}

func cmdAppReplay(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("app replay", flag.ContinueOnError)
	base := fs.String("base", "", "base URL of an HTTP program")
	cli := fs.String("cli", "", "command-line program to run each CLI case with")
	reportPath := fs.String("report", "", "also write a report: .md, .json or .html")
	webhooks := fs.String("webhooks", "", "capture and compare the outbound calls: 127.0.0.1:<port> (same port as recording)")
	dbPath := fs.String("db", "", "SQLite database to watch for row changes (read-only; same tables as recording)")
	dbTables := fs.String("tables", "", "comma-separated tables to watch with -db")
	dbTenant := fs.String("tenant", "", "tenant id for PostgreSQL row-level security (sets app.current_tenant_id per snapshot)")
	mailAddr := fs.String("mail", "", "capture the mail the program sends: 127.0.0.1:<port> (point the program's SMTP server here)")
	dbReset := fs.String("db-reset", "", "SQL file run against -db before EVERY case (destructive; needs -allow-db-write; scratch databases only)")
	dbDump := fs.String("db-dump", "", "write the final state of -tables to this JSON file (read-only)")
	allowWrite := fs.Bool("allow-db-write", false, "allow -db-reset to change data")
	caseName := fs.String("case", "", "replay only the recorded case with this name (to reproduce one failure)")
	compareOpts := compareFlags(fs)
	pos, err := parseInterspersed(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return errors.New("app replay: exactly one app recording file is required")
	}
	opt, err := compareOpts("app replay")
	if err != nil {
		return err
	}
	rec, err := store.LoadApp(pos[0])
	if err != nil {
		return err
	}
	if *caseName != "" {
		if rec, err = usecase.SelectCase(rec, *caseName); err != nil {
			return fmt.Errorf("app replay: %s: %w", pos[0], err)
		}
	}
	runner, err := appRunner(*base, *cli)
	if err != nil {
		return err
	}
	src, closeSrc, err := combinedSource(*webhooks, *mailAddr, *dbPath, *dbTables, *dbTenant)
	if err != nil {
		return err
	}
	defer closeSrc()
	life, err := setupLifecycle(*dbPath, *dbTables, *dbTenant, *dbReset, *dbDump, *allowWrite)
	if err != nil {
		return err
	}
	defer life.close()
	v, err := usecase.AppReplayFull(rec, runner, usecase.ReplayOptions{Src: src, Before: life.before, Compare: opt})
	if err != nil {
		return err
	}
	if err := life.dump(); err != nil {
		return err
	}
	if *reportPath != "" {
		kind := "app-cli"
		if *base != "" {
			kind = "app-http"
		}
		rewrite := domain.RedactCommand([]string{*cli})
		if *base != "" {
			rewrite = domain.RedactCommand([]string{*base})
		}
		if err := writeReport(*reportPath, usecase.AppReport(v, kind), rewrite, pos[0], opt); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "report written to %s\n", *reportPath)
	}
	// Show where this run actually sent the cases, not the target the recording was made against.
	target := *base
	if target == "" {
		target = *cli
	}
	fmt.Fprintf(stdout, "%d/%d app cases match (replayed against %s; compare: %s)\n", v.Matched, v.Total, target, opt.Describe())
	for _, m := range v.Mismatches {
		printAppMismatch(stdout, m)
	}
	if !v.Passed() {
		return fmt.Errorf("app replay: %d mismatch(es)", len(v.Mismatches))
	}
	return nil
}

// dbSource opens a SQLite database and watches the named tables. With no path it returns nil.
// The returned cleanup closes the database.
func dbSource(path, tables, tenant string) (usecase.EffectSource, func(), error) {
	if path == "" {
		return nil, func() {}, nil
	}
	if tables == "" {
		return nil, func() {}, errors.New("-db needs -tables <table,...> naming the tables to watch")
	}
	var s *dbsnap.Snapshotter
	var err error
	if dbsnap.IsPostgresAddress(path) {
		// The address may carry a password. It is used for the connection and never printed.
		s, err = dbsnap.OpenPostgres(path, tenantSetting, tenant)
	} else {
		if tenant != "" {
			return nil, func() {}, errors.New("-tenant applies to PostgreSQL only (row-level security); SQLite has none")
		}
		s, err = dbsnap.Open(path)
	}
	if err != nil {
		return nil, func() {}, err
	}
	names := strings.Split(tables, ",")
	for i := range names {
		names[i] = strings.TrimSpace(names[i])
	}
	w, err := dbsnap.NewWatcher(s, names)
	if err != nil {
		s.Close()
		return nil, func() {}, err
	}
	return w, func() { s.Close() }, nil
}

// tenantSetting is the session setting PostgreSQL row-level security policies read the tenant from.
const tenantSetting = "app.current_tenant_id"

// dbLifecycle holds the optional database steps around each case: a reset before every case and a
// dump of the final state. Both are off unless asked for, and the reset is refused without consent.
type dbLifecycle struct {
	before  func() error
	dumpTo  string
	tables  []string
	addr    string
	tenant  string
	closers []func()
}

func setupLifecycle(addr, tables, tenant, reset, dumpTo string, allowWrite bool) (*dbLifecycle, error) {
	life := &dbLifecycle{dumpTo: dumpTo, addr: addr, tenant: tenant}
	if reset == "" && dumpTo == "" {
		return life, nil
	}
	if (reset != "" || dumpTo != "") && addr == "" {
		return nil, errors.New("-db-reset and -db-dump need -db <file|postgres://...> and -tables")
	}
	if dumpTo != "" {
		if tables == "" {
			return nil, errors.New("-db-dump needs -tables")
		}
		life.tables = splitNames(tables)
	}
	if reset != "" {
		if !allowWrite {
			return nil, errors.New("-db-reset changes data; add -allow-db-write to confirm, and use a scratch database")
		}
		script, err := os.ReadFile(reset)
		if err != nil {
			return nil, fmt.Errorf("-db-reset: read %s: %w", reset, err)
		}
		// Do not print the address: it may carry a password.
		fmt.Fprintf(os.Stderr, "WARNING: %s runs before every case on the database given by -db. Use a scratch database only.\n", reset)
		var w *dbsnap.Snapshotter
		if dbsnap.IsPostgresAddress(addr) {
			w, err = dbsnap.OpenPostgresWritable(addr)
		} else {
			w, err = dbsnap.OpenSQLiteWritable(addr)
		}
		if err != nil {
			return nil, err
		}
		life.closers = append(life.closers, func() { w.Close() })
		life.before = func() error { return w.ExecScript(string(script)) }
	}
	return life, nil
}

// dump writes the final state of the watched tables, if a dump file was asked for.
func (l *dbLifecycle) dump() error {
	if l.dumpTo == "" {
		return nil
	}
	var s *dbsnap.Snapshotter
	var err error
	if dbsnap.IsPostgresAddress(l.addr) {
		s, err = dbsnap.OpenPostgres(l.addr, tenantSetting, l.tenant)
	} else {
		s, err = dbsnap.Open(l.addr)
	}
	if err != nil {
		return err
	}
	defer s.Close()
	state, err := s.Dump(l.tables)
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("db-dump: encode: %w", err)
	}
	if err := os.WriteFile(l.dumpTo, append(data, '\n'), 0o600); err != nil {
		return fmt.Errorf("db-dump: write %s: %w", l.dumpTo, err)
	}
	return nil
}

func (l *dbLifecycle) close() {
	for _, c := range l.closers {
		c()
	}
}

func splitNames(list string) []string {
	parts := strings.Split(list, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// effectSource starts the webhook receiver on addr when one is given, and reports its URL so the
// program under test can be pointed at it. With no address it returns nil and a no-op cleanup.
func effectSource(addr string) (usecase.EffectSource, func(), error) {
	if addr == "" {
		return nil, func() {}, nil
	}
	r, err := capture.StartAt(addr)
	if err != nil {
		return nil, func() {}, err
	}
	fmt.Fprintf(os.Stderr, "webhook receiver listening at %s (point the program here)\n", r.URL)
	return r, func() { r.Close() }, nil
}

// combinedSource builds every requested effect source (webhooks and database rows) into one.
// With none requested it returns nil, so the case runs without effect capture.
func combinedSource(webhookAddr, mailAddr, dbPath, dbTables, tenant string) (usecase.EffectSource, func(), error) {
	var sources usecase.MultiSource
	var cleanups []func()
	cleanup := func() {
		for _, c := range cleanups {
			c()
		}
	}
	wsrc, wclose, err := effectSource(webhookAddr)
	if err != nil {
		return nil, func() {}, err
	}
	cleanups = append(cleanups, wclose)
	if wsrc != nil {
		sources = append(sources, wsrc)
	}
	if mailAddr != "" {
		rcv, err := mailcap.Start(mailAddr)
		if err != nil {
			cleanup()
			return nil, func() {}, err
		}
		fmt.Fprintf(os.Stderr, "mail receiver listening at %s (point the program's SMTP server here)\n", rcv.Addr)
		cleanups = append(cleanups, func() { rcv.Close() })
		sources = append(sources, rcv)
	}
	dsrc, dclose, err := dbSource(dbPath, dbTables, tenant)
	if err != nil {
		cleanup()
		return nil, func() {}, err
	}
	cleanups = append(cleanups, dclose)
	if dsrc != nil {
		sources = append(sources, dsrc)
	}
	if len(sources) == 0 {
		return nil, cleanup, nil
	}
	return sources, cleanup, nil
}

// subjectName labels a whole-app recording by what it ran against.
func subjectName(base, cli string) string {
	if base != "" {
		return base
	}
	return cli
}

// routeFlags collects repeated -route "METHOD:prefix=target" values.
type routeFlags []string

func (r *routeFlags) String() string     { return strings.Join(*r, ",") }
func (r *routeFlags) Set(v string) error { *r = append(*r, v); return nil }

// parseRoute reads "METHOD:prefix=target" (METHOD may be * or omitted with a leading /).
func parseRoute(raw string) (domain.Route, error) {
	spec, target, ok := strings.Cut(raw, "=")
	if !ok {
		return domain.Route{}, fmt.Errorf("route %q: want METHOD:prefix=target", raw)
	}
	method, prefix := "*", spec
	if m, p, found := strings.Cut(spec, ":"); found {
		method, prefix = m, p
	}
	return domain.Route{Method: method, PathPrefix: prefix, Target: target}, nil
}

func cmdFrontDoor(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("frontdoor", flag.ContinueOnError)
	listen := fs.String("listen", "127.0.0.1:8080", "address to serve on (loopback by default)")
	original := fs.String("original", "", "URL of the existing program")
	rewrite := fs.String("rewrite", "", "URL of the rewrite")
	var routes routeFlags
	fs.Var(&routes, "route", "METHOD:prefix=rewrite|original (repeatable)")
	routesFile := fs.String("routes-file", "", "routing file to follow; edits (see `wire`) take effect without a restart")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *original == "" {
		return errors.New("frontdoor: -original is required")
	}
	if *routesFile != "" {
		if len(routes) > 0 {
			return errors.New("frontdoor: give -routes-file or -route, not both")
		}
		h, err := frontdoor.NewFromFile(*routesFile, *original, *rewrite)
		if err != nil {
			return err
		}
		fmt.Fprintf(stdout, "front door on %s following %s; unlisted requests go to %s\n", *listen, *routesFile, *original)
		srv := &http.Server{Addr: *listen, Handler: h, ReadHeaderTimeout: 10 * time.Second}
		return srv.ListenAndServe()
	}
	rules := make([]domain.Route, 0, len(routes))
	for _, r := range routes {
		rt, err := parseRoute(r)
		if err != nil {
			return err
		}
		rules = append(rules, rt)
	}
	h, err := frontdoor.New(rules, *original, *rewrite)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "front door on %s: %d route(s); unlisted requests go to %s\n", *listen, len(rules), *original)
	srv := &http.Server{Addr: *listen, Handler: h, ReadHeaderTimeout: 10 * time.Second}
	return srv.ListenAndServe()
}

func cmdFuzz(args []string, stdout io.Writer) error {
	flagsPart, orig, rew, err := splitTwo(args)
	if err != nil {
		return err
	}
	fs := flag.NewFlagSet("fuzz", flag.ContinueOnError)
	n := fs.Int("n", 200, "fresh inputs to try")
	seed := fs.Uint64("seed", 0, "seed to generate the inputs from (0 = pick one; it is printed)")
	timeout := fs.Duration("timeout", 30*time.Second, "per-input timeout")
	if err := fs.Parse(flagsPart); err != nil {
		return err
	}
	if fs.NArg() != 0 || *n < 1 {
		return errors.New("fuzz: no positional arguments; -n must be at least 1")
	}
	s := *seed
	if s == 0 {
		s = uint64(time.Now().UnixNano())
	}
	inputs := domain.FuzzInputs(s, *n)
	rep := usecase.Fuzz(subject.Exec{Argv: orig, Timeout: *timeout}, subject.Exec{Argv: rew, Timeout: *timeout}, inputs, s)
	fmt.Fprintf(stdout, "fuzz: %d fresh inputs, seed %d (replay with -seed %d)\n", rep.Total, rep.Seed, rep.Seed)
	for _, m := range rep.Mismatches {
		fmt.Fprintf(stdout, "  %s: input %s\n    original %s\n    rewrite  %s\n", m.Detail, shortQuote(m.Input), shortQuote(m.Original), shortQuote(m.Rewrite))
	}
	fmt.Fprintf(stdout, "fuzz: %d disagreement(s)\n", len(rep.Mismatches))
	if !rep.Passed() {
		return errors.New("fuzz: the rewrite disagrees with the original")
	}
	return nil
}

// shortQuote quotes a value for display and cuts long ones, so one long fuzz input cannot bury the report.
func shortQuote(s string) string {
	const max = 120
	if len(s) <= max {
		return fmt.Sprintf("%q", s)
	}
	return fmt.Sprintf("%q... (%d bytes)", s[:max], len(s))
}

type unitSpec struct {
	Recording string   `json:"recording"`
	Source    string   `json:"source"`
	Command   []string `json:"command"`
}

func cmdLift(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("lift", flag.ContinueOnError)
	appPath := fs.String("app", "", "app recording to replay")
	base := fs.String("base", "", "URL of the rewrite the recording is replayed against")
	original := fs.String("original", "", "URL of the original; lift refuses when it equals -base")
	root := fs.String("root", ".", "Go module root for the gate")
	unitsPath := fs.String("units", "", "JSON list of units: {recording, source, command}")
	skipTests := fs.Bool("skip-tests", false, "skip go test in the gate")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *appPath == "" || *base == "" {
		return errors.New("lift: -app and -base are required")
	}
	var blockers []string
	if *original != "" && *original == *base {
		blockers = append(blockers, "replay target is the original itself: point -base at the rewrite")
	}
	rec, err := store.LoadApp(*appPath)
	if err != nil {
		return err
	}
	v, err := usecase.AppReplay(rec, app.Runner{BaseURL: *base})
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "replay: %d of %d app cases match (%s)\n", v.Matched, v.Total, *base)

	var units []usecase.LiftUnit
	if *unitsPath != "" {
		data, err := os.ReadFile(*unitsPath)
		if err != nil {
			return fmt.Errorf("lift: read units: %w", err)
		}
		var specs []unitSpec
		if err := json.Unmarshal(data, &specs); err != nil {
			return fmt.Errorf("lift: parse units: %w", err)
		}
		for _, sp := range specs {
			if len(sp.Command) == 0 {
				return fmt.Errorf("lift: unit %s has no command", sp.Recording)
			}
			uv, err := runVerify(sp.Recording, subject.Exec{Argv: sp.Command}, sp.Source)
			if err != nil {
				return fmt.Errorf("lift: unit %s: %w", sp.Recording, err)
			}
			name := filepath.Base(sp.Recording)
			units = append(units, usecase.LiftUnit{Name: name, Passed: uv.Passed(), Stale: uv.Stale, Mismatch: len(uv.Mismatches)})
			fmt.Fprintf(stdout, "unit %s: %d of %d match, stale=%t\n", name, uv.Matched, uv.Total, uv.Stale)
		}
	}

	checks := gatecheck.Run(gatecheck.Options{Root: *root, SkipTests: *skipTests})
	for _, c := range checks {
		mark := "ok  "
		if !c.Passed {
			mark = "FAIL"
		}
		fmt.Fprintf(stdout, "[%s] %-40s %s\n", mark, c.Name, c.Detail)
	}
	blockers = append(blockers, usecase.LiftBlockers(usecase.LiftInput{
		ReplayTotal: v.Total, ReplayMatched: v.Matched, Units: units, GatePassed: domain.GatePassed(checks),
	})...)
	if len(blockers) > 0 {
		fmt.Fprintln(stdout, "lift: not ready")
		for _, b := range blockers {
			fmt.Fprintf(stdout, "  - %s\n", b)
		}
		return fmt.Errorf("lift: %d blocker(s)", len(blockers))
	}
	fmt.Fprintln(stdout, "lift: ready. No blockers were found. This does not prove the rewrite never calls the original internally.")
	return nil
}

// cmdWireVerify builds the program, sends one unit to its rewrite, and replays the recorded cases of that
// unit against the program. For a command-line unit the built binary runs each case; for an HTTP unit the
// program is started on the address in its main.go and the cases are sent to it. The program is stopped
// afterwards.
func cmdWireVerify(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("wire verify", flag.ContinueOnError)
	cases := fs.String("cases", "", "app recording (required)")
	originalCLI := fs.String("original-cli", "", "command-line unit: the original program (GA_ORIGINAL_CLI)")
	originalURL := fs.String("original-url", "", "HTTP unit: the original service (GA_ORIGINAL_URL)")
	mainFile := fs.String("main", "main.go", "file holding the program's main (relative to the module root)")
	compareOpts := compareFlags(fs)
	pos, err := parseInterspersed(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 2 && len(pos) != 3 {
		return errors.New("wire verify: usage: wire verify <module root> <unit> [prefix] -cases app.json [-original-cli BIN | -original-url URL] [-json-body] [-float-tolerance N]")
	}
	opt, err := compareOpts("wire verify")
	if err != nil {
		return err
	}
	if *cases == "" {
		return errors.New("wire verify: -cases is required")
	}
	root, unit := pos[0], pos[1]
	rec, err := store.LoadApp(*cases)
	if err != nil {
		return err
	}
	dir, err := os.MkdirTemp("", "go-anything-verify-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	bin := filepath.Join(dir, "app")
	if err := wiregen.BuildApp(root, bin); err != nil {
		return err
	}
	env := wiregen.UnitEnv(unit, *originalCLI, *originalURL)

	var verdict usecase.AppVerdict
	if len(pos) == 2 {
		subset := domain.AppRecording{Subject: rec.Subject, Cases: usecase.CasesForCommand(rec, unit)}
		if len(subset.Cases) == 0 {
			return fmt.Errorf("wire verify: %s has no recorded cases for command %q", *cases, unit)
		}
		for _, kv := range env {
			k, v, _ := strings.Cut(kv, "=")
			os.Setenv(k, v)
		}
		defer func() {
			for _, kv := range env {
				k, _, _ := strings.Cut(kv, "=")
				os.Unsetenv(k)
			}
		}()
		verdict, err = usecase.AppReplayFull(subset, app.Runner{Argv0: bin}, usecase.ReplayOptions{Compare: opt})
	} else {
		prefix := pos[2]
		subset := domain.AppRecording{Subject: rec.Subject, Cases: usecase.CasesFor(rec, prefix)}
		if len(subset.Cases) == 0 {
			return fmt.Errorf("wire verify: %s has no recorded http cases under %s", *cases, prefix)
		}
		addr, err := wiregen.ListenAddress(root, *mainFile)
		if err != nil {
			return err
		}
		stop, err := wiregen.StartApp(bin, env, addr)
		if err != nil {
			return err
		}
		verdict, err = usecase.AppReplayFull(subset, app.Runner{BaseURL: "http://" + addr}, usecase.ReplayOptions{Compare: opt})
		stop()
	}
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "wire verify: %d of %d recorded case(s) match for unit %s (compare: %s)\n", verdict.Matched, verdict.Total, unit, opt.Describe())
	for _, m := range verdict.Mismatches {
		printAppMismatch(stdout, m)
	}
	if !verdict.Passed() {
		return fmt.Errorf("wire verify: %d mismatch(es); the unit does not yet match the recording", len(verdict.Mismatches))
	}
	return nil
}

// cmdWireRefine closes the gap between the recording and the original: it probes the rewrite with
// fresh inputs, records every input where the original answers differently, and has the agent fix it.
func cmdWireRefine(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("wire refine", flag.ContinueOnError)
	cases := fs.String("cases", "", "app recording of this unit (required); new cases are added to it")
	originalCLI := fs.String("original-cli", "", "command-line unit: the original program")
	originalURL := fs.String("original-url", "", "HTTP unit: the original service URL")
	rounds := fs.Int("rounds", 4, "most probe rounds")
	probes := fs.Int("probes", 40, "values tried per round")
	maxNew := fs.Int("max-new", 20, "most new cases recorded per round")
	seed := fs.Uint64("seed", 1, "seed for the probe values")
	agentCmd := fs.String("agent", "", "shell command of a coding agent that fixes rewrite.go (reads the brief on stdin)")
	attempts := fs.Int("attempts", 3, "agent attempts per round")
	defectPolicy := fs.String("defect-policy", "", "what an original crash means: defect (report, do not record) or expected (record it as the behavior to match); overrides the settings file")
	rbacPrincipals := fs.String("rbac-principals", "", "HTTP unit: principal file (JSON array); with -rbac-policy, convergence also needs a clean authorization check")
	rbacPolicy := fs.String("rbac-policy", "", "HTTP unit: declared access policy file (JSON array); needs -rbac-principals")
	extraInputs := fs.String("extra-inputs", "", "file of values outside the probe generator (one per line, or a JSON array of strings); probed on both sides after convergence, and any disagreement fails the run")
	compareOpts := compareFlags(fs)
	pos, err := parseInterspersed(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 2 && len(pos) != 3 {
		return errors.New("wire refine: usage: wire refine <module root> <unit> [prefix] -cases app.json (-original-cli BIN | -original-url URL) [-agent CMD]")
	}
	prefix := ""
	if len(pos) == 3 {
		prefix = pos[2]
	}
	if *cases == "" {
		return errors.New("wire refine: -cases is required")
	}
	rbac, err := rbacOptionsFromFlags(*rbacPrincipals, *rbacPolicy, prefix != "")
	if err != nil {
		return err
	}
	cmpOpt, err := compareOpts("wire refine")
	if err != nil {
		return err
	}
	// The flag has no default so that "not given" can be told apart from "defect": only a flag the operator
	// typed overrides the settings file. fs.Visit reports that, and it is only valid after Parse.
	var flagPolicy *usecase.DefectPolicy
	flagGiven := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "defect-policy" {
			flagGiven = true
		}
	})
	if flagGiven {
		if *defectPolicy == "" {
			return errors.New("wire refine: -defect-policy needs defect or expected")
		}
		p, err := usecase.ParseDefectPolicy(*defectPolicy)
		if err != nil {
			return err
		}
		flagPolicy = &p
	}
	var extra []string
	if *extraInputs != "" {
		info, err := os.Stat(*extraInputs)
		if err != nil {
			return fmt.Errorf("wire refine: -extra-inputs: %w", err)
		}
		if info.Size() > 1<<20 {
			return fmt.Errorf("wire refine: -extra-inputs %s is %d bytes; at most 1 MiB is read", *extraInputs, info.Size())
		}
		data, err := os.ReadFile(*extraInputs)
		if err != nil {
			return fmt.Errorf("wire refine: -extra-inputs: %w", err)
		}
		if extra, err = usecase.ParseExtraInputs(data); err != nil {
			return fmt.Errorf("wire refine: -extra-inputs %s: %w", *extraInputs, err)
		}
	}
	cfg, err := settings.Load(pos[0])
	if err != nil {
		return err
	}
	policy, source := usecase.ResolveDefectPolicy(pos[1], flagPolicy, cfg)
	fmt.Fprintf(stdout, "wire refine: %s defect policy %s (from %s)\n", pos[1], policy, policySourceText(source, settings.Path(pos[0])))
	ok, err := wiregen.Refine(wiregen.RefineOptions{
		Policy: policy,
		Root:   pos[0], Unit: pos[1], Prefix: prefix, CasesPath: *cases,
		OriginalCLI: *originalCLI, OriginalURL: *originalURL,
		Rounds: *rounds, Probes: *probes, MaxNew: *maxNew, Seed: *seed,
		Agent: *agentCmd, Attempts: *attempts, Log: stdout,
		RBAC: rbac, ExtraInputs: extra, Compare: cmpOpt,
	})
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("wire refine: not converged")
	}
	return nil
}

// policySourceText says, for the operator, which setting decided a defect policy.
func policySourceText(src usecase.PolicySource, path string) string {
	switch src {
	case usecase.SourceFlag:
		return "-defect-policy flag"
	case usecase.SourceUnit:
		return "unit_defect_policy in " + path
	case usecase.SourceFile:
		return "defect_policy in " + path
	}
	return "built-in default"
}

func cmdWire(args []string, stdout io.Writer) error {
	if len(args) < 2 {
		return errors.New("wire: usage: wire status <routes.json> | wire set <routes.json> <unit> <rewrite|original>")
	}
	switch args[0] {
	case "gen":
		fs := flag.NewFlagSet("wire gen", flag.ContinueOnError)
		mainFile := fs.String("main", "main.go", "file holding the program's http.ListenAndServe call (relative to the module root)")
		noMain := fs.Bool("no-main", false, "do not edit main.go; only write the unit files and print the mount lines")
		casesPath := fs.String("cases", "", "app recording; its cases for this unit become units/<unit>/cases_test.go")
		agentCmd := fs.String("agent", "", "shell command of a coding agent that writes rewrite.go from the recorded cases (reads the brief on stdin)")
		attempts := fs.Int("attempts", 3, "how many times the agent may try before giving up")
		compareOpts := compareFlags(fs)
		pos, err := parseInterspersed(fs, args[1:])
		if err != nil {
			return err
		}
		cmpOpt, err := compareOpts("wire gen")
		if err != nil {
			return err
		}
		if len(pos) != 2 && len(pos) != 3 {
			return errors.New("wire gen: usage: wire gen <module root> <unit> [url prefix, e.g. /orders/]  (no prefix = command-line unit)")
		}
		root, unit := pos[0], pos[1]
		prefix := ""
		kind := "cli"
		if len(pos) == 3 {
			prefix = pos[2]
			kind = "http"
		}
		written, err := wiregen.Gen(root, unit, prefix)
		for _, p := range written {
			fmt.Fprintf(stdout, "wrote %s\n", p)
		}
		if err != nil {
			return err
		}
		// The recorded cases and the agent run before main.go is touched: a module whose main.go is
		// missing or fails to wire still gets a testable unit (go test ./units/<unit>/).
		if *casesPath != "" {
			rec, err := store.LoadApp(*casesPath)
			if err != nil {
				return err
			}
			var cases []domain.AppCase
			var data []byte
			if kind == "http" {
				cases = usecase.CasesFor(rec, prefix)
				if len(cases) == 0 {
					return fmt.Errorf("wire gen: %s has no recorded http cases under %s", *casesPath, prefix)
				}
				data, err = usecase.CasesTest(unit, cases, cmpOpt)
			} else {
				cases = usecase.CasesForCommand(rec, unit)
				if len(cases) == 0 {
					return fmt.Errorf("wire gen: %s has no recorded cli cases whose first argument is %q", *casesPath, unit)
				}
				data, err = usecase.CLICasesTest(unit, cases, cmpOpt)
			}
			if err != nil {
				return err
			}
			p, err := wiregen.WriteCases(root, unit, data)
			if err != nil {
				return err
			}
			fmt.Fprintf(stdout, "wrote %s (%d recorded case(s))\n", p, len(cases))
			if *agentCmd != "" {
				fmt.Fprintf(stdout, "asking the agent to write units/%s/rewrite.go from the recorded behavior (clean room: no original source)...\n", unit)
				res, err := wiregen.RunAgent(root, unit, *agentCmd, *attempts, func(prev string) string {
					if kind == "cli" {
						return usecase.AgentPromptCLI(unit, cases, prev)
					}
					return usecase.AgentPrompt(unit, prefix, cases, prev)
				})
				if err != nil {
					return err
				}
				fmt.Fprintf(stdout, "rewrite: %s\n", res)
			}
		} else if *agentCmd != "" {
			return errors.New("wire gen: -agent needs -cases, so the agent has a recorded behavior to match")
		}
		if *noMain {
			fmt.Fprintf(stdout, "\nmain.go not edited (-no-main). Add this before the server starts:\n    if err := units.Mount(mux, os.Getenv(\"GA_ORIGINAL_URL\")); err != nil { log.Fatal(err) }\n")
		} else {
			module, err := wiregen.ReadModule(root)
			if err != nil {
				return err
			}
			changed, err := wiregen.WireMain(root, *mainFile, module, kind)
			if err != nil {
				return fmt.Errorf("%w\nthe unit files and the recorded cases were written; only main.go was left unchanged", err)
			}
			if changed {
				fmt.Fprintf(stdout, "main.go: mount added and the module builds (go build ./... passed)\n")
			} else {
				fmt.Fprintf(stdout, "main.go: mount already present\n")
			}
		}
		// A front door written by frontdoor scaffold starts the original and sets GA_ORIGINAL_URL or
		// GA_ORIGINAL_CLI itself, so the run line must not ask the operator for them.
		scaffolded := wiregen.ScaffoldedFrontDoor(root, *mainFile)
		if kind == "cli" && scaffolded {
			fmt.Fprintf(stdout, `
unit %[1]s is wired as the command "%[1]s" and runs the original by default.
  run the program:  go run %[2]s %[1]s ...  (the scaffolded front door runs the original and sets GA_ORIGINAL_CLI itself)
  switch the unit:  GA_UNIT_%[3]s=rewrite  (only once units/%[1]s/rewrite.go is written)
  check:            go-anything gate -root %[2]s
`, unit, root, strings.ToUpper(unit))
			return nil
		}
		if kind == "cli" {
			fmt.Fprintf(stdout, `
unit %[1]s is wired as the command "%[1]s" and runs the original by default.
  run the program:  GA_ORIGINAL_CLI=<original program> go run %[2]s %[1]s ...
  switch the unit:  GA_UNIT_%[3]s=rewrite  (only once units/%[1]s/rewrite.go is written)
  check:            go-anything gate -root %[2]s
`, unit, root, strings.ToUpper(unit))
			return nil
		}
		runApp := "GA_ORIGINAL_URL=<original service URL> go run " + root
		if scaffolded {
			runApp = "go run " + root + "  (the scaffolded front door starts the original and sets GA_ORIGINAL_URL itself)"
		}
		fmt.Fprintf(stdout, `
unit %[1]s is wired at %[2]s and serves the original by default.
  run the app:      %[5]s
  switch the unit:  GA_UNIT_%[4]s=rewrite  (only once units/%[1]s/rewrite.go is written)
  check:            go-anything gate -root %[3]s
`, unit, prefix, root, strings.ToUpper(unit), runApp)
		return nil
	case "refine":
		return cmdWireRefine(args[1:], stdout)
	case "verify":
		return cmdWireVerify(args[1:], stdout)
	case "status":
		fs := flag.NewFlagSet("wire status", flag.ContinueOnError)
		root := fs.String("root", "", "module root whose .go-anything/config.json sets each unit's defect policy")
		pos, err := parseInterspersed(fs, args[1:])
		if err != nil {
			return err
		}
		if len(pos) != 1 {
			return errors.New("wire status: usage: wire status <routes.json> [-root <module root>]")
		}
		routes, err := routesfile.Load(pos[0])
		if err != nil {
			return err
		}
		var cfg usecase.PolicySettings
		if *root != "" {
			if cfg, err = settings.Load(*root); err != nil {
				return err
			}
		}
		units := routesfile.Units(routes)
		names := make([]string, 0, len(units))
		for n := range units {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			if *root == "" {
				fmt.Fprintf(stdout, "%-20s %s\n", n, units[n])
				continue
			}
			p, src := usecase.ResolveDefectPolicy(n, nil, cfg)
			fmt.Fprintf(stdout, "%-20s %-10s policy %s (%s)\n", n, units[n], p, policySourceText(src, settings.Path(*root)))
		}
		if len(names) == 0 {
			fmt.Fprintln(stdout, "no unit names in this routing file")
		}
		return nil
	case "set":
		if len(args) != 4 {
			return errors.New("wire set: usage: wire set <routes.json> <unit> <rewrite|original>")
		}
		n, err := routesfile.Set(args[1], args[2], args[3])
		if err != nil {
			return err
		}
		fmt.Fprintf(stdout, "unit %s: %d route(s) now go to %s\n", args[2], n, args[3])
		return nil
	}
	return fmt.Errorf("wire: unknown subcommand %q (want status or set)", args[0])
}

func cmdGate(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("gate", flag.ContinueOnError)
	root := fs.String("root", ".", "Go module root to check")
	skipTests := fs.Bool("skip-tests", false, "skip go test (static checks only)")
	appPath := fs.String("app", "", "app recording whose behavior must still replay")
	base := fs.String("base", "", "base URL the -app recording replays against")
	security := fs.Bool("security", false, "opt-in: also run the security function confirmation checklist; mandatory failures fail the gate")
	if err := fs.Parse(args); err != nil {
		return err
	}
	checks := gatecheck.Run(gatecheck.Options{Root: *root, SkipTests: *skipTests})
	if *security {
		rep, err := runSecure(*root, false, "")
		if err != nil {
			return err
		}
		n := len(rep.MandatoryFailures())
		c := domain.GateCheck{Name: "security checklist", Passed: n == 0,
			Detail: fmt.Sprintf("%d mandatory failure(s), %d manual item(s) left for review", n, rep.CountsPerStatus()[domain.SecureStatusManual])}
		if n > 0 {
			ids := make([]string, 0, n)
			for _, r := range rep.MandatoryFailures() {
				ids = append(ids, r.Item)
			}
			c.Detail += ": " + strings.Join(ids, ", ")
		}
		checks = append(checks, c)
	}
	if *appPath != "" {
		if *base == "" {
			return errors.New("gate: -app needs -base <url>")
		}
		rec, err := store.LoadApp(*appPath)
		if err != nil {
			return err
		}
		v, err := usecase.AppReplay(rec, app.Runner{BaseURL: *base})
		if err != nil {
			return err
		}
		c := domain.GateCheck{Name: "recorded behavior (app)", Passed: v.Passed(),
			Detail: fmt.Sprintf("%d of %d cases match", v.Matched, v.Total)}
		checks = append(checks, c)
	}
	for _, c := range checks {
		mark := "ok  "
		if !c.Passed {
			mark = "FAIL"
		}
		fmt.Fprintf(stdout, "[%s] %-40s %s\n", mark, c.Name, c.Detail)
	}
	if !domain.GatePassed(checks) {
		return errors.New("gate: not complete; see the failed checks above")
	}
	fmt.Fprintln(stdout, "gate: passed")
	return nil
}

func cmdDoctor(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	project := fs.String("project", ".", "project folder: its go.mod sets the required Go version and where reports go")
	if err := fs.Parse(args); err != nil {
		return err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("doctor: home directory: %w", err)
	}
	env := doctor.Gather(*project, *project, home)
	report := usecase.Doctor(env)
	for _, c := range report.Checks {
		fmt.Fprintf(stdout, "[%-4s] %-26s %s\n", c.Level, c.Name, c.Detail)
		if c.Fix != "" {
			fmt.Fprintf(stdout, "        fix: %s\n", c.Fix)
		}
	}
	n := report.Counts()
	fmt.Fprintf(stdout, "\ndoctor: %d ok, %d warn, %d fail\n", n[domain.LevelOK], n[domain.LevelWarn], n[domain.LevelFail])
	if !report.Healthy() {
		return errors.New("doctor: something is broken; see the fix lines above")
	}
	return nil
}

func cmdCoverage(args []string, stdout io.Writer) error {
	if len(args) != 1 {
		return errors.New("coverage: exactly one coverprofile file is required")
	}
	f, err := os.Open(args[0])
	if err != nil {
		return fmt.Errorf("coverage: %w", err)
	}
	defer f.Close()
	c, err := domain.ParseCoverProfile(f)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(c)
}

// analysisOutput is the analyze result: the analysis itself, plus the porting order computed from it.
type analysisOutput struct {
	domain.Analysis
	PortOrder []domain.PortRow `json:"port_order"`
}

func cmdAnalyze(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("analyze", flag.ContinueOnError)
	lang := fs.String("lang", analyze.LangAuto, "language of the source: auto (go.mod means go, else the largest of python, js, c and java), go, python, js, c or java")
	// Flags may come before or after the root, so parse again after each positional argument.
	var roots []string
	for rest := args; ; {
		if err := fs.Parse(rest); err != nil {
			return err
		}
		if fs.NArg() == 0 {
			break
		}
		roots = append(roots, fs.Arg(0))
		rest = fs.Args()[1:]
	}
	if len(roots) > 1 {
		return errors.New("analyze: at most one root directory")
	}
	root := "."
	if len(roots) == 1 {
		root = roots[0]
	}
	switch *lang {
	case analyze.LangAuto, analyze.LangGo, analyze.LangPython, analyze.LangJS, analyze.LangC, analyze.LangJava:
	default:
		return fmt.Errorf("analyze: unknown -lang %q; supported: auto, %s", *lang, analyze.SupportedLanguages)
	}
	res, err := usecase.Analyze(analyze.Source{Lang: *lang}, root)
	if err != nil {
		return err
	}
	// Every analysis leaves an HTML report behind. Each run gets a new version folder, so an
	// earlier report is never overwritten and a later run can be compared with it.
	dir, err := writeAnalysisReport(res)
	if err != nil {
		return err
	}
	// The notice goes to stderr so stdout stays one JSON document that `| jq` can read.
	fmt.Fprintf(os.Stderr, "analysis report written to %s\n", dir)
	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(analysisOutput{Analysis: res, PortOrder: domain.PortPlan(res)})
}

// writeAnalysisReport saves the analysis as the next version under the analyzed project.
// It returns the folder it wrote into.
func writeAnalysisReport(res domain.Analysis) (string, error) {
	subject := subjectFolder(res.Module)
	n, err := usecase.NextVersion(filepath.Join(res.Root, usecase.ReportRoot, subject))
	if err != nil {
		return "", err
	}
	dir, err := usecase.VersionDir(res.Root, subject, n)
	if err != nil {
		return "", err
	}
	rep := domain.NewAnalysisReport(res, n)
	rep.GeneratedAt = time.Now().UTC().Format(time.RFC3339)
	if prev, pv, ok := previousAnalysis(filepath.Join(res.Root, usecase.ReportRoot, subject), n); ok {
		rep = rep.WithPrevious(prev, pv)
	}
	html, err := rep.HTML()
	if err != nil {
		return "", err
	}
	js, err := json.MarshalIndent(res, "", "  ")
	if err != nil {
		return "", fmt.Errorf("analyze: encode analysis: %w", err)
	}
	// analysis.json is what the next run compares against; the HTML alone cannot be diffed.
	if err := os.WriteFile(filepath.Join(dir, "analysis.json"), js, 0o600); err != nil {
		return "", fmt.Errorf("analyze: write analysis.json: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "analysis.html"), html, 0o600); err != nil {
		return "", fmt.Errorf("analyze: write report: %w", err)
	}
	return dir, nil
}

// previousAnalysis finds the newest earlier version that saved analysis.json. Versions written before
// the JSON was saved are skipped, so the comparison resumes as soon as one exists. An unreadable file is
// reported and skipped: a report without a delta is better than no report.
func previousAnalysis(subjectDir string, current int) (domain.Analysis, int, bool) {
	for v := current - 1; v >= 1; v-- {
		path := filepath.Join(subjectDir, fmt.Sprintf("v%d", v), "analysis.json")
		b, err := os.ReadFile(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "analyze: cannot read %s, no comparison: %v\n", path, err)
			return domain.Analysis{}, 0, false
		}
		var prev domain.Analysis
		if err := json.Unmarshal(b, &prev); err != nil {
			fmt.Fprintf(os.Stderr, "analyze: %s is not a valid analysis, no comparison: %v\n", path, err)
			return domain.Analysis{}, 0, false
		}
		return prev, v, true
	}
	return domain.Analysis{}, 0, false
}

// subjectFolder turns a module path into a safe single folder name.
// "github.com/x/y" becomes "github.com_x_y".
func subjectFolder(module string) string {
	var b strings.Builder
	for _, r := range module {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	name := strings.Trim(b.String(), "._")
	if name == "" {
		return "module"
	}
	return name
}

// runVerify loads a recording, replays it, and checks drift when a source directory is given.
// Without src the recording is not checked for drift, and the verdict says so.
func runVerify(path string, rewrite usecase.Subject, src string) (usecase.Verdict, error) {
	return runVerifyWith(path, rewrite, src, domain.CompareOptions{})
}

// runVerifyWith is runVerify with outputs compared under opt.
func runVerifyWith(path string, rewrite usecase.Subject, src string, opt domain.CompareOptions) (usecase.Verdict, error) {
	var s usecase.RecordingStore = store.FileStore{}
	rec, err := s.Load(path)
	if err != nil {
		return usecase.Verdict{}, err
	}
	v, err := usecase.VerifyWith(rec, rewrite, opt)
	if err != nil {
		return v, err
	}
	if src != "" {
		now, err := domain.FingerprintDir(src)
		if err != nil {
			return v, err
		}
		v.DriftChecked = true
		v.Stale = usecase.CheckDrift(rec, now, true)
	}
	return v, nil
}

func finishVerify(w io.Writer, v usecase.Verdict) error {
	report(w, v)
	return harness.Gate(v)
}

// serveMCP exposes the use cases as tools. Each tool is a thin adapter over a use case.
func serveMCP(in io.Reader, out io.Writer) error {
	srv := mcp.NewServer("go-anything", version)
	srv.Register(mcp.Tool{
		Name:        "analyze",
		Description: "Report packages, imports and functions of a Go module (no code is executed).",
		Schema: map[string]any{"type": "object", "properties": map[string]any{
			"root": map[string]any{"type": "string", "description": "Module root directory (default .)"},
		}},
		Call: func(args json.RawMessage) (string, error) {
			var a struct {
				Root string `json:"root"`
			}
			if len(args) > 0 {
				if err := json.Unmarshal(args, &a); err != nil {
					return "", err
				}
			}
			if a.Root == "" {
				a.Root = "."
			}
			res, err := usecase.Analyze(analyze.GoSource{}, a.Root)
			if err != nil {
				return "", err
			}
			b, err := json.MarshalIndent(analysisOutput{Analysis: res, PortOrder: domain.PortPlan(res)}, "", "  ")
			return string(b), err
		},
	})
	srv.Register(mcp.Tool{
		Name:        "verify",
		Description: "Replay a recording against a command. Fails unless every case matches.",
		Schema: map[string]any{"type": "object", "required": []string{"recording", "command"}, "properties": map[string]any{
			"recording": map[string]any{"type": "string"},
			"command":   map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
		}},
		Call: func(args json.RawMessage) (string, error) {
			var a struct {
				Recording string   `json:"recording"`
				Command   []string `json:"command"`
				Src       string   `json:"src"`
			}
			if err := json.Unmarshal(args, &a); err != nil {
				return "", err
			}
			v, err := runVerify(a.Recording, subject.Exec{Argv: a.Command}, a.Src)
			if err != nil {
				return "", err
			}
			var b strings.Builder
			report(&b, v)
			if err := harness.Gate(v); err != nil {
				return b.String(), err
			}
			return b.String(), nil
		},
	})
	registerExtraTools(srv)
	registerRecordTools(srv)
	registerLiftTool(srv)
	return srv.Serve(in, out)
}

func report(w io.Writer, v usecase.Verdict) {
	fmt.Fprintf(w, "%d/%d cases match (%s)\n", v.Matched, v.Total, v.Subject)
	if !v.DriftChecked {
		fmt.Fprintln(w, "  note: drift not checked (pass -src to fail when the original changed)")
	}
	if v.Stale {
		fmt.Fprintln(w, "  STALE: the original's source changed since the recording was made (or was not fingerprinted); record again")
	}
	for _, m := range v.Mismatches {
		if m.Detail != "" {
			// The first difference (a JSON path or a line) instead of both whole outputs.
			fmt.Fprintf(w, "  %s [%s]: differs at %s\n", m.Case, m.Status.Describe(), m.Detail)
			continue
		}
		fmt.Fprintf(w, "  %s [%s]: expected %q, got %q", m.Case, m.Status.Describe(), m.Expected, m.Got)
		if m.Err != nil {
			fmt.Fprintf(w, " err=%v", m.Err)
		}
		fmt.Fprintln(w)
	}
}

// readInputs reads one case per non-empty line. Names are the 1-based line number.
func readInputs(r io.Reader) (map[string]string, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("read inputs: %w", err)
	}
	inputs := map[string]string{}
	for i, line := range strings.Split(strings.TrimRight(string(data), "\n"), "\n") {
		if line == "" {
			continue
		}
		inputs[fmt.Sprintf("line-%d", i+1)] = domain.Normalize(line)
	}
	return inputs, nil
}
