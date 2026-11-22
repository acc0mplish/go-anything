package wiregen

import (
	"fmt"
	"io"
	"path/filepath"

	"github.com/go-anything/go-anything/internal/adapter/store"
	"github.com/go-anything/go-anything/internal/domain"
	"github.com/go-anything/go-anything/internal/usecase"
)

// RefineOptions drives the loop that closes the gap between the recorded cases and the original.
type RefineOptions struct {
	Root        string
	Unit        string
	Prefix      string // empty for a command-line unit
	CasesPath   string // the app recording; new cases are added to it
	OriginalCLI string // command-line unit: the original program
	OriginalURL string // HTTP unit: the original service
	Rounds      int
	Probes      int // values tried per round
	MaxNew      int // most new cases recorded per round
	Seed        uint64
	Agent       string
	Attempts    int
	// Policy decides what an original crash means: a defect to report (the default) or behavior to match.
	Policy usecase.DefectPolicy
	Log    io.Writer
	// RBAC, when set, adds the authorization check to convergence. HTTP units only.
	RBAC *RBACOptions
	// ExtraInputs are values outside the probe generator, probed once on both sides after the loop
	// converges (same request construction as the probes). A disagreement makes the run not converged.
	ExtraInputs []string
	// Compare is how answers are compared (JSON semantics, number tolerance); the zero value is exact.
	Compare domain.CompareOptions
}

// Refine probes the rewrite against the original with fresh inputs. Every input where the two disagree
// is recorded with the original's answer, the unit's tests are regenerated from the larger recording,
// and the agent (if configured) is asked to fix the rewrite. It returns true once the convergence rule
// holds: two consecutive clean rounds on fresh seeds (one round when Rounds is 1, with a warning).
// A clean round has no disagreement, no new case, and no critical authorization finding.
func Refine(opts RefineOptions) (bool, error) {
	// The probe program is built with the module root as the working directory, so every path derived from
	// the root must be absolute: a relative root would put the binary under root/root and miss it.
	if opts.Root != "" {
		abs, err := filepath.Abs(opts.Root)
		if err != nil {
			return false, fmt.Errorf("refine: module root %s: %w", opts.Root, err)
		}
		opts.Root = abs
	}
	kind := "cli"
	if opts.Prefix != "" {
		kind = "http"
	}
	if kind == "cli" && opts.OriginalCLI == "" {
		return false, fmt.Errorf("refine: a command-line unit needs the original program (-original-cli)")
	}
	if kind == "http" && opts.OriginalURL == "" {
		return false, fmt.Errorf("refine: an HTTP unit needs the original service URL (-original-url)")
	}
	if opts.RBAC != nil && kind == "cli" {
		return false, fmt.Errorf("refine: the authorization check (RBAC) applies to HTTP units only; unit %s is a command-line unit", opts.Unit)
	}
	if opts.RBAC != nil && len(opts.RBAC.Principals) == 0 {
		return false, fmt.Errorf("refine: the authorization check needs at least one principal")
	}
	module, err := ReadModule(opts.Root)
	if err != nil {
		return false, err
	}
	rec, err := store.LoadApp(opts.CasesPath)
	if err != nil {
		return false, err
	}
	logf := func(format string, args ...any) {
		if opts.Log != nil {
			fmt.Fprintf(opts.Log, format, args...)
		}
	}
	// One log for the whole run: the same crashing request found again in a later round is one defect.
	defects := usecase.NewDefectLog()
	// streak counts the consecutive clean rounds ending at the last round run.
	streak := 0
	// page rebuilds the RSI loop HTML page from the files on disk. It never fails the run.
	page := func() { writeRefinePage(opts.Root, opts.Unit, opts.CasesPath+".defects.json", logf) }
	for round := 1; round <= max(opts.Rounds, 1); round++ {
		values := domain.ProbeValues(opts.Seed+uint64(round), opts.Probes)
		probes := usecase.ProbeCases(kind, opts.Unit, opts.Prefix, values)
		shapes := 0
		if kind == "http" {
			// A few values per round are also sent in other request shapes. -probes still counts values.
			extra := usecase.RequestShapeProbes(opts.Prefix, values, usecase.ShapeValuesPerRound)
			probes, shapes = append(probes, extra...), len(extra)
		}
		got, err := runProbes(opts, kind, module, probes)
		if err != nil {
			return false, err
		}

		recorded := map[string]bool{}
		for _, c := range rec.Cases {
			recorded[usecase.CaseKey(c)] = true
		}
		disagree, added := 0, 0
		var newCaseNames []string
		for i, p := range probes {
			want, err := AskOriginal(kind, opts.OriginalCLI, opts.OriginalURL, p)
			if err != nil {
				return false, err
			}
			// Under the defect policy an original crash is a defect in the original, not something the rewrite
			// must copy: it is reported separately and never recorded. Under the expected policy it is behavior
			// to match by exit code (or status) and stdout; JudgeProbe marks the crash text unstable.
			outcome, want := usecase.JudgeProbeWith(want, got[i], opts.Policy.RecordsCrash(), opts.Compare)
			switch outcome {
			case usecase.ProbeAgree:
				continue
			case usecase.ProbeOriginalDefect:
				defects.Add(p, want)
				continue
			}
			disagree++
			if recorded[usecase.CaseKey(p)] || added >= max(opts.MaxNew, 1) {
				continue
			}
			name := uniqueName(usedNames(rec), fmt.Sprintf("probe-round%d-%d", round, i))
			rec.Cases = append(rec.Cases, usecase.RecordAnswer(p, want, name))
			newCaseNames = append(newCaseNames, name)
			recorded[usecase.CaseKey(p)] = true
			added++
		}
		// The authorization check runs only where the probes agree: that is where refine would declare
		// convergence, and the check must agree too. It starts the rewrite, so it is skipped on disagreement.
		rbacFindings := []usecase.Finding{}
		rbacChecked, rbacPrincipals := false, 0
		if disagree == 0 && opts.RBAC != nil {
			report, err := CheckRBAC(opts.Root, opts.Unit, casesForUnit(rec, opts.Unit, opts.Prefix, kind), *opts.RBAC, opts.OriginalURL)
			if err != nil {
				return false, err
			}
			rbacChecked, rbacPrincipals = true, report.Principals
			rbacFindings = append(rbacFindings, report.Findings...)
			for _, f := range report.OriginalDefects {
				defects.AddText(f.Case+" as "+f.Principal, f.Detail)
			}
			logf("rbac: %d principal(s) asked each case on the rewrite and the original: %d finding(s), %d original defect(s)\n",
				report.Principals, len(report.Findings), len(report.OriginalDefects))
		}

		shapeNote := ""
		if kind == "http" {
			shapeNote = fmt.Sprintf(" (%d of them request-shape variants)", shapes)
		}
		logf("round %d: %d probes%s, %d disagree with the original, %d new case(s) recorded, %d original defect(s) found\n", round, len(probes), shapeNote, disagree, added, len(defects.List()))

		if err := usecase.SaveDefects(opts.CasesPath+".defects.json", defects.List()); err != nil {
			return false, err
		}
		clean := roundConverged(disagree, rbacFindings) && len(newCaseNames) == 0
		streak = nextCleanStreak(streak, clean)
		converged, single := convergence(streak, max(opts.Rounds, 1))
		check := newRoundCheck(round, opts.Seed+uint64(round), values, len(probes), disagree, newCaseNames, len(defects.List()), converged)
		check.DefectPolicy = string(opts.Policy)
		if check.DefectPolicy == "" {
			check.DefectPolicy = string(usecase.PolicyDefect)
		}
		check.CleanStreak = streak
		check.ShapeRequests = shapes
		check.RBACChecked = rbacChecked
		check.RBACPrincipals = rbacPrincipals
		check.RBACFindings = rbacFindings
		if converged {
			if len(opts.ExtraInputs) > 0 {
				found, requests, err := crossCheck(opts, kind, module, defects, logf)
				if err != nil {
					return false, err
				}
				check.ExtraChecked, check.ExtraRequests, check.ExtraDisagreements = len(opts.ExtraInputs), requests, found
				check.OriginalDefects = len(defects.List())
				if err := usecase.SaveDefects(opts.CasesPath+".defects.json", defects.List()); err != nil {
					return false, err
				}
				if len(found) > 0 {
					check.Converged = false
					check.Confidence = "extra inputs disagree: the probes converged, but values outside the probe generator found a difference"
					if _, err := WriteRoundCheck(opts.Root, opts.Unit, check); err != nil {
						return false, err
					}
					page()
					return false, fmt.Errorf("refine: not converged. The probes agreed for %d consecutive round(s), but %d of %d request(s) built from the %d extra input(s) disagree with the original (listed above and as extra_disagreements in round-%02d.json). Record them as cases or fix units/%s/rewrite.go, then rerun",
						streak, len(found), requests, len(opts.ExtraInputs), round, opts.Unit)
				}
			}
			if _, err := WriteRoundCheck(opts.Root, opts.Unit, check); err != nil {
				return false, err
			}
			page()
			authz := "authorization not checked (no -rbac-principals and -rbac-policy were given)"
			if rbacChecked {
				authz = fmt.Sprintf("authorization checked as %d principal(s): no critical finding", rbacPrincipals)
			}
			if single {
				logf("warning: a single round is not a convergence proof. Rerun with -rounds 2 or more to check a second fresh seed.\n")
				logf("1 clean round (%s), not yet converged: the rewrite agrees with the original on all %d probes of one seed; %s. Not checked: %v\n",
					seedsOf(opts.Seed, round, 1), len(probes), authz, check.NotChecked)
			} else {
				logf("converged after %d consecutive clean rounds (%s). The rewrite agrees with the original on all %d probes in each round; %s. Not checked: %v\n",
					streak, seedsOf(opts.Seed, round, streak), len(probes), authz, check.NotChecked)
			}
			return true, nil
		}
		if clean {
			// Nothing to fix, so the agent is not asked. Record the clean round and probe again on a fresh seed.
			if _, err := WriteRoundCheck(opts.Root, opts.Unit, check); err != nil {
				return false, err
			}
			page()
			logf("round %d is clean (streak %d of 2). One more clean round on a fresh seed is needed.\n", round, streak)
			continue
		}
		if disagree == 0 {
			logf("rbac: %d critical authorization finding(s) block convergence\n", criticalCount(rbacFindings))
		}
		if added > 0 {
			if err := store.SaveApp(opts.CasesPath, rec); err != nil {
				return false, err
			}
		}
		if err := regenerateTests(opts, rec, kind); err != nil {
			return false, err
		}
		if opts.Agent == "" {
			check.AgentOutcome = "not run: no agent command was given"
			if _, err := WriteRoundCheck(opts.Root, opts.Unit, check); err != nil {
				return false, err
			}
			page()
			if disagree == 0 {
				return false, fmt.Errorf("refine: %d critical authorization finding(s); the rewrite needs updating (run with -agent, or edit units/%s/rewrite.go)", criticalCount(rbacFindings), opts.Unit)
			}
			return false, fmt.Errorf("refine: %d probe(s) disagree; the rewrite needs updating (run with -agent, or edit units/%s/rewrite.go)", disagree, opts.Unit)
		}
		// The failing authorization requests go into the brief, so the agent sees them in its next attempt.
		rbacBrief := usecase.RBACBrief(rbacFindings, casesForUnit(rec, opts.Unit, opts.Prefix, kind))
		brief := func(prev string) string {
			cases := casesForUnit(rec, opts.Unit, opts.Prefix, kind)
			if kind == "cli" {
				return usecase.AgentPromptCLI(opts.Unit, cases, prev)
			}
			return usecase.AgentPrompt(opts.Unit, opts.Prefix, cases, prev) + rbacBrief
		}
		logf("asking the agent to fix units/%s/rewrite.go against %d recorded case(s)...\n", opts.Unit, len(casesForUnit(rec, opts.Unit, opts.Prefix, kind)))
		outcome := "passed the recorded cases"
		if _, err := RunAgent(opts.Root, opts.Unit, opts.Agent, opts.Attempts, brief); err != nil {
			outcome = "did not pass: " + err.Error()
			check.AgentOutcome = outcome
			WriteRoundCheck(opts.Root, opts.Unit, check)
			page()
			return false, err
		}
		check.AgentOutcome = outcome
		if _, err := WriteRoundCheck(opts.Root, opts.Unit, check); err != nil {
			return false, err
		}
		page()
	}
	return false, fmt.Errorf("refine: not converged after %d round(s); the last clean streak is %d. Widen -probes or record more cases", max(opts.Rounds, 1), streak)
}

// nextCleanStreak returns the number of consecutive clean rounds ending at this round. A round that is not
// clean (a disagreement or a blocking finding) resets the streak to 0.
func nextCleanStreak(prev int, clean bool) int {
	if !clean {
		return 0
	}
	return prev + 1
}

// convergence decides whether a run has converged, given the clean streak ending at the current round and
// the number of rounds allowed. Two consecutive clean rounds on fresh seeds are the proof. A single-round run
// converges on one clean round; single is then true so the caller can warn that one round proves little.
func convergence(streak, rounds int) (converged, single bool) {
	if streak >= 2 {
		return true, false
	}
	if rounds <= 1 && streak >= 1 {
		return true, true
	}
	return false, false
}

// seedsOf names the probe seeds of the last n rounds ending at round, for the convergence log line.
// Round r uses seed base+r.
func seedsOf(base uint64, round, n int) string {
	first := round - n + 1
	switch n {
	case 1:
		return fmt.Sprintf("seed %d", base+uint64(first))
	case 2:
		return fmt.Sprintf("seeds %d and %d", base+uint64(first), base+uint64(round))
	default:
		return fmt.Sprintf("seeds %d to %d", base+uint64(first), base+uint64(round))
	}
}

// usedNames lists the case names already in the recording. Names must be unique, and a new round
// can reuse an index from an earlier run, so names are checked rather than assumed.
func usedNames(rec domain.AppRecording) map[string]bool {
	out := map[string]bool{}
	for _, c := range rec.Cases {
		out[c.Name] = true
	}
	return out
}

func uniqueName(used map[string]bool, base string) string {
	name := base
	for n := 2; used[name]; n++ {
		name = fmt.Sprintf("%s-%d", base, n)
	}
	return name
}

func casesForUnit(rec domain.AppRecording, unit, prefix, kind string) []domain.AppCase {
	if kind == "cli" {
		return usecase.CasesForCommand(rec, unit)
	}
	return usecase.CasesFor(rec, prefix)
}

// regenerateTests rewrites the unit's behavior test from the whole recording, so the agent and the
// test always see the same cases.
func regenerateTests(opts RefineOptions, rec domain.AppRecording, kind string) error {
	cases := casesForUnit(rec, opts.Unit, opts.Prefix, kind)
	var data []byte
	var err error
	if kind == "cli" {
		data, err = usecase.CLICasesTest(opts.Unit, cases, opts.Compare)
	} else {
		data, err = usecase.CasesTest(opts.Unit, cases, opts.Compare)
	}
	if err != nil {
		return err
	}
	_, err = WriteCases(opts.Root, opts.Unit, data)
	return err
}

// crossCheck probes the extra inputs once on the rewrite and the original, after the loop converged. The
// requests are built like the round probes (usecase.ProbeCases), and the defect policy applies as in a
// round: an original crash is a defect (added to the log) or, under the expected policy, compared by exit
// code or status only. It returns the disagreements and the number of requests sent. Nothing is recorded.
func crossCheck(opts RefineOptions, kind, module string, defects *usecase.DefectLog, logf func(string, ...any)) ([]ExtraDisagreement, int, error) {
	var probes []domain.AppCase
	var valueOf []string
	for i, v := range opts.ExtraInputs {
		for _, p := range usecase.ProbeCases(kind, opts.Unit, opts.Prefix, []string{v}) {
			p.Name = fmt.Sprintf("extra-%d-%s", i, p.Name)
			probes = append(probes, p)
			valueOf = append(valueOf, v)
		}
	}
	got, err := runProbes(opts, kind, module, probes)
	if err != nil {
		return nil, 0, err
	}
	var found []ExtraDisagreement
	newDefects := 0
	for i, p := range probes {
		want, err := AskOriginal(kind, opts.OriginalCLI, opts.OriginalURL, p)
		if err != nil {
			return nil, 0, err
		}
		outcome, want := usecase.JudgeProbeWith(want, got[i], opts.Policy.RecordsCrash(), opts.Compare)
		switch outcome {
		case usecase.ProbeAgree:
			continue
		case usecase.ProbeOriginalDefect:
			if defects.Add(p, want) {
				newDefects++
			}
			continue
		}
		d := ExtraDisagreement{Value: domain.ClipForReport(valueOf[i], 200), Request: domain.ClipForReport(describeProbe(p), 300),
			Want: usecase.AnswerText(want), Got: usecase.AnswerText(got[i])}
		found = append(found, d)
		logf("extra input %q disagrees: %s\n  want (original): %s\n  got  (rewrite):  %s\n", d.Value, d.Request, d.Want, d.Got)
	}
	logf("extra inputs: %d value(s) as %d request(s) on both sides: %d disagree, %d new original defect(s)\n",
		len(opts.ExtraInputs), len(probes), len(found), newDefects)
	return found, len(probes), nil
}

// describeProbe is the request of a probe as a reader would type it.
func describeProbe(p domain.AppCase) string {
	if p.Kind == "cli" {
		if p.Stdin != "" {
			return fmt.Sprintf("args=%q stdin=%q", p.Argv, p.Stdin)
		}
		return fmt.Sprintf("args=%q", p.Argv)
	}
	return p.Method + " " + p.Path
}

// runProbes sends one round's probes to the rewrite. A command-line unit runs the throwaway harness
// binary; an HTTP unit is probed through the assembled application (RunHTTPProbesLive), so main.go's
// routing, middleware and authentication are part of what convergence checks.
func runProbes(opts RefineOptions, kind, module string, probes []domain.AppCase) ([]domain.AppCase, error) {
	if kind == "http" {
		return RunHTTPProbesLive(opts.Root, opts.Unit, opts.OriginalURL, probes)
	}
	bin, cleanup, err := BuildProbe(opts.Root, opts.Unit, kind, module)
	if err != nil {
		return nil, err
	}
	defer cleanup()
	return RunProbe(bin, kind, probes)
}
