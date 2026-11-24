package wiregen

import (
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"

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
	// Delay, Retries and RetryDelay pace and retry the probes sent to the ORIGINAL side only (HTTP units;
	// see WireOptions). The rewrite side sends each probe once, as before. The zero value sends each
	// probe once, immediately.
	Delay      time.Duration
	Retries    int
	RetryDelay time.Duration
	// Concurrency, when > 0, fires each of the unit's recorded cases this many times at once at both
	// sides after the round's probes agree (HTTP units only, next to the authorization check). A rewrite
	// that answers identical concurrent requests differently makes the round not converged (a
	// go-anything extension); an uncertain verdict does not block but is recorded. The requests are not
	// recorded as cases. 0 (the default) skips the check.
	Concurrency int
	// Chains, when > 0, runs the consecutive-request check after the round's probes agree (HTTP units
	// only): it derives collection targets from the unit's recorded cases and, for each of at most N,
	// creates a resource under a reserved chain-rN name and walks it through create, read, mutate and
	// delete, comparing the two sides step by step. A divergence or an uncertain verdict makes the
	// round not converged (the ported contract; an uncertain verdict keeps the round from being
	// clean). The requests are not recorded as cases. 0 (the default) skips the check.
	Chains int
	// ServeOriginal, when set, is a shell command that starts a fresh copy of the operator's original
	// at the top of every round (HTTP units only); the copy is stopped when the round ends, whatever
	// the round concluded, so state the original accumulated in earlier rounds never colors this
	// round's verdicts and the two sides stay symmetric (the rewrite restarts every round already).
	// {{port}} in the command is replaced by the port this tool picks. It replaces OriginalURL, which
	// must then be empty; the extra-inputs cross-check that runs after convergence uses the same
	// round-scoped copy the same way.
	ServeOriginal string
	// OriginalStartTimeout is how long a -serve-original copy gets to answer its first HTTP request
	// before the start fails. The zero value means 60 seconds.
	OriginalStartTimeout time.Duration
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
	if kind == "http" && opts.OriginalURL == "" && opts.ServeOriginal == "" {
		return false, fmt.Errorf("refine: an HTTP unit needs the original service URL (-original-url, or -serve-original to start it per round)")
	}
	if opts.RBAC != nil && kind == "cli" {
		return false, fmt.Errorf("refine: the authorization check (RBAC) applies to HTTP units only; unit %s is a command-line unit", opts.Unit)
	}
	if opts.RBAC != nil && len(opts.RBAC.Principals) == 0 {
		return false, fmt.Errorf("refine: the authorization check needs at least one principal")
	}
	if opts.Concurrency < 0 {
		return false, fmt.Errorf("refine: -concurrency must be 0 or more, got %d", opts.Concurrency)
	}
	if opts.Concurrency > 0 && kind == "cli" {
		return false, fmt.Errorf("refine: the concurrent-request check (-concurrency) applies to HTTP units only; unit %s is a command-line unit", opts.Unit)
	}
	if opts.Chains < 0 {
		return false, fmt.Errorf("refine: -chains must be 0 or more, got %d", opts.Chains)
	}
	if opts.Chains > 0 && kind == "cli" {
		return false, fmt.Errorf("refine: the consecutive-request check (-chains) applies to HTTP units only; unit %s is a command-line unit", opts.Unit)
	}
	if opts.ServeOriginal != "" && opts.OriginalURL != "" {
		return false, fmt.Errorf("refine: give one of -original-url or -serve-original")
	}
	if opts.ServeOriginal != "" && kind == "cli" {
		return false, fmt.Errorf("refine: -serve-original applies to HTTP units only; unit %s is a command-line unit", opts.Unit)
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
	// chainUncertainSeen dedupes a target whose chain stays unjudgeable: its repeated pending verdicts
	// count once as a new finding, while the pending itself keeps breaking the round's clean verdict
	// every round (the ported contract).
	chainUncertainSeen := map[string]bool{}
	// chainPendingNames and chainPendingReasons carry the last round's unjudgeable targets to the final
	// error, so running out of rounds never looks like a quiet budget exhaustion.
	var chainPendingNames, chainPendingReasons []string
	// The missing-placeholder warning is about the operator's command, not a round: say it once.
	if kind == "http" && opts.ServeOriginal != "" && !serveCommandNeedsPort(opts.ServeOriginal) {
		logf("warning: -serve-original has no {{port}} placeholder — the original cannot bind the port this tool picks; the command is used as written\n")
	}
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
		// This round's original: the fixed -original-url, or a fresh copy of the operator's program
		// that the round's closure stops when the round ends (-serve-original). Starting it fresh every
		// round is the point: state the original accumulated in earlier rounds must not color this
		// round's verdicts, and the rewrite restarts every round already, so the two sides stay
		// symmetric. A start that fails is a tool error, not a round verdict: the loop stops.
		origURL := opts.OriginalURL
		stopOriginal := func() {}
		if kind == "http" && opts.ServeOriginal != "" {
			srv, err := StartOriginal(opts.ServeOriginal, opts.OriginalStartTimeout)
			if err != nil {
				return false, err
			}
			origURL, stopOriginal = srv.URL, srv.Stop
		}
		// The round body is a closure so its cleanup is one defer: the fresh original must come down on
		// every exit, and the round has many. The loop's own state (rec, defects, streak, the chain
		// pending lists) stays in scope and is updated through the captures. Results: next goes on with
		// the next round; otherwise ok and err are Refine's result, unchanged.
		next, ok, err := func() (bool, bool, error) {
			defer stopOriginal()
			// Every check below asks this round's original through this copy of the options; the
			// original options stay untouched for the later rounds.
			opts := opts
			opts.OriginalURL = origURL
			got, err := runProbes(opts, kind, module, probes)
			if err != nil {
				return false, false, err
			}

			recorded := map[string]bool{}
			for _, c := range rec.Cases {
				recorded[usecase.CaseKey(c)] = true
			}
			disagree, added := 0, 0
			var newCaseNames []string
			for i, p := range probes {
				want, err := AskOriginal(kind, opts.OriginalCLI, opts.OriginalURL, p, wireOpts(opts))
				if err != nil {
					return false, false, err
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
					return false, false, err
				}
				rbacChecked, rbacPrincipals = true, report.Principals
				rbacFindings = append(rbacFindings, report.Findings...)
				for _, f := range report.OriginalDefects {
					defects.AddText(f.Case+" as "+f.Principal, f.Detail)
				}
				logf("rbac: %d principal(s) asked each case on the rewrite and the original: %d finding(s), %d original defect(s)\n",
					report.Principals, len(report.Findings), len(report.OriginalDefects))
			}
			// The concurrent-request check runs where the probes agree, next to the authorization check: that is
			// where refine would declare convergence. The rewrite starts once per round and both the
			// concurrent check and the chain check share that one session, so a round that runs both starts
			// the program once. The requests are not recorded as cases, so the operator's recording and the
			// generated tests stay clean.
			concurrentOn := false
			var concurrent []ConcurrentVerdict
			var concurrentPending []string
			concurrentDiverges := 0
			// Per-round chain state. The pending lists are reset by the chain block itself; the vars they
			// feed outlive the loop for the final error.
			chainOn := false
			var chainTranscripts []chainTranscript
			var chainBriefCases []usecase.ChainBriefCase
			chainDiverges, chainsChecked, chainRequests, chainFirstDivergence := 0, 0, 0, 0
			if disagree == 0 && kind == "http" && (opts.Concurrency > 0 || opts.Chains > 0) {
				cases := casesForUnit(rec, opts.Unit, opts.Prefix, kind)
				var liveAddr string
				var liveStop func()
				var liveExited <-chan struct{}
				liveUp := false
				begin := func() (string, <-chan struct{}, error) {
					if liveUp {
						return liveAddr, liveExited, nil
					}
					addr, stop, exited, err := startLiveApp(opts.Root, opts.Unit, opts.OriginalURL)
					if err != nil {
						return "", nil, err
					}
					liveAddr, liveStop, liveExited, liveUp = addr, stop, exited, true
					return liveAddr, exited, nil
				}
				finish := func() {
					if liveUp {
						liveStop()
						liveUp = false
					}
				}
				if opts.Concurrency > 0 {
					// The narrowed not_checked line must rest on a check that actually ran: the option
					// being on and the probes agreeing is not enough. With no recorded case nothing was
					// fired, so the round keeps the common line instead of claiming coverage.
					concurrentOn = len(cases) > 0
					var liveErr error
					if len(cases) > 0 {
						var addr string
						var exited <-chan struct{}
						addr, exited, liveErr = begin()
						if liveErr == nil {
							concurrent, liveErr = ConcurrentVerdicts(opts.OriginalURL, "http://"+addr, cases, opts.Concurrency, opts.Compare, exited)
						}
					}
					if liveErr != nil {
						finish()
						return false, false, liveErr
					}
					for _, cv := range concurrent {
						switch cv.Verdict {
						case "diverge":
							concurrentDiverges++
						case "uncertain":
							concurrentPending = append(concurrentPending, cv.Case)
						}
					}
					logf("concurrency: %d recorded case(s) fired %d time(s) at once on both sides: %d diverge, %d pending\n",
						len(concurrent), opts.Concurrency, concurrentDiverges, len(concurrentPending))
					for _, cv := range concurrent {
						if cv.Verdict == "diverge" {
							logf("concurrency: case %s diverged: %s\n", cv.Case, cv.Note)
						}
					}
				}
				if opts.Chains > 0 {
					chainPendingNames, chainPendingReasons = nil, nil
					targets := chainTargets(cases, opts.Chains)
					chainsChecked = len(targets)
					// Same evidence rule: with no target no chain step was sent anywhere, so the state
					// line keeps the common wording.
					chainOn = chainsChecked > 0
					if chainsChecked == 0 {
						logf("chains: the recording yields no chain target (no collection with an observed POST) — chain probes skipped\n")
					}
					var exited <-chan struct{}
					var addr string
					var err error
					for _, tg := range targets {
						steps := chainCases(opts.Seed+uint64(round), tg, round)
						chainRequests += len(steps)
						addr, exited, err = begin()
						if err != nil {
							finish()
							return false, false, err
						}
						// Before each chain: a program that exited can no longer answer for itself, so any
						// reply now would come from a different process on the address (issue #17).
						select {
						case <-exited:
							finish()
							return false, false, fmt.Errorf("chains: the program exited before target %s was probed, so its answers do not come from the rewrite", tg.Collection)
						default:
						}
						origOut := runChainAgainst(concurrentClient(), opts.OriginalURL, steps)
						rewOut := runChainAgainst(concurrentClient(), "http://"+addr, steps)
						v := judgeChain(origOut, rewOut, opts.Compare)
						chainTranscripts = append(chainTranscripts, chainTranscript{Round: round, Target: tg.Collection, Name: steps[0].Name,
							Original: origOut, Rewrite: rewOut, Verdict: v})
						switch {
						case v.Divergent:
							chainDiverges++
							if chainDiverges == 1 {
								chainFirstDivergence = v.FirstDivergence + 1
							}
							logf("chains: target %s diverged at step %d\n", tg.Collection, v.FirstDivergence+1) // counted from 1, like the round record
							// Only the requests up to the first difference go into the brief: the later steps
							// repeat on a resource the rewrite already answered differently about.
							brief := usecase.ChainBriefCase{Target: tg.Collection, Name: steps[0].Name, FirstDivergence: v.FirstDivergence}
							for i, s := range steps {
								if i > v.FirstDivergence {
									break
								}
								brief.Steps = append(brief.Steps, usecase.ChainStep{Method: s.Method, Path: s.Path, Body: s.Body})
							}
							for i, o := range origOut {
								if i > v.FirstDivergence {
									break
								}
								brief.Original = append(brief.Original, usecase.ChainObserved{Method: o.Method, Path: o.Path, Status: o.Status, Body: o.Body, Err: o.Err})
							}
							chainBriefCases = append(chainBriefCases, brief)
						case v.Uncertain:
							if !chainUncertainSeen[tg.Collection] {
								chainUncertainSeen[tg.Collection] = true
								logf("chains: target %s cannot be judged this round (first time): %s\n", tg.Collection, v.UncertainReason)
							}
							chainPendingNames = append(chainPendingNames, tg.Collection)
							chainPendingReasons = append(chainPendingReasons, fmt.Sprintf("%s (%s)", tg.Collection, v.UncertainReason))
						}
					}
					if exited != nil {
						select {
						case <-exited:
							finish()
							return false, false, fmt.Errorf("chains: the program exited during the chain probes, so its answers do not come from the rewrite")
						default:
						}
					}
					if len(chainTranscripts) > 0 {
						if err := writeChainRound(filepath.Join(opts.Root, ".go-anything", "checks", opts.Unit), round, chainTranscripts); err != nil {
							finish()
							return false, false, err
						}
					}
					logf("chains: %d target(s), %d step(s) per side, %d diverge, %d pending\n",
						chainsChecked, chainRequests, chainDiverges, len(chainPendingNames))
				}
				finish()
			}

			shapeNote := ""
			if kind == "http" {
				shapeNote = fmt.Sprintf(" (%d of them request-shape variants)", shapes)
			}
			logf("round %d: %d probes%s, %d disagree with the original, %d new case(s) recorded, %d original defect(s) found\n", round, len(probes), shapeNote, disagree, added, len(defects.List()))

			if err := usecase.SaveDefects(opts.CasesPath+".defects.json", defects.List()); err != nil {
				return false, false, err
			}
			clean := roundConverged(disagree, rbacFindings, concurrentDiverges, chainDiverges, len(chainPendingNames)) && len(newCaseNames) == 0
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
			// A round that ran a check narrows that check's line in its unchecked list: what was exercised
			// is said precisely, and what still is not stays on the record.
			check.NotChecked = notCheckedList(concurrentOn, chainOn)
			check.ConcurrentChecked = len(concurrent)
			check.ConcurrentDiverges = concurrentDiverges
			check.ConcurrentPending = concurrentPending
			check.ChainsChecked = chainsChecked
			check.ChainRequests = chainRequests
			check.ChainDiverges = chainDiverges
			check.ChainFirstDivergence = chainFirstDivergence
			check.ChainPending = chainPendingNames
			if converged {
				if len(opts.ExtraInputs) > 0 {
					found, requests, err := crossCheck(opts, kind, module, defects, logf)
					if err != nil {
						return false, false, err
					}
					check.ExtraChecked, check.ExtraRequests, check.ExtraDisagreements = len(opts.ExtraInputs), requests, found
					check.OriginalDefects = len(defects.List())
					if err := usecase.SaveDefects(opts.CasesPath+".defects.json", defects.List()); err != nil {
						return false, false, err
					}
					if len(found) > 0 {
						check.Converged = false
						check.Confidence = "extra inputs disagree: the probes converged, but values outside the probe generator found a difference"
						if _, err := WriteRoundCheck(opts.Root, opts.Unit, check); err != nil {
							return false, false, err
						}
						page()
						return false, false, fmt.Errorf("refine: not converged. The probes agreed for %d consecutive round(s), but %d of %d request(s) built from the %d extra input(s) disagree with the original (listed above and as extra_disagreements in round-%02d.json). Record them as cases or fix units/%s/rewrite.go, then rerun",
							streak, len(found), requests, len(opts.ExtraInputs), round, opts.Unit)
					}
				}
				if _, err := WriteRoundCheck(opts.Root, opts.Unit, check); err != nil {
					return false, false, err
				}
				page()
				authz := "authorization not checked (no -rbac-principals and -rbac-policy were given)"
				if rbacChecked {
					authz = fmt.Sprintf("authorization checked as %d principal(s): no critical finding", rbacPrincipals)
				}
				// A pending verdict never blocks convergence, but it must not quietly disappear either: the
				// summary line says how many cases the concurrent check could not judge.
				pendingNote := ""
				if len(concurrentPending) > 0 {
					pendingNote = fmt.Sprintf(", %d concurrent case(s) left pending", len(concurrentPending))
				}
				if single {
					logf("warning: a single round is not a convergence proof. Rerun with -rounds 2 or more to check a second fresh seed.\n")
					logf("1 clean round (%s), not yet converged: the rewrite agrees with the original on all %d probes of one seed; %s%s. Not checked: %v\n",
						seedsOf(opts.Seed, round, 1), len(probes), authz, pendingNote, check.NotChecked)
				} else {
					logf("converged after %d consecutive clean rounds (%s). The rewrite agrees with the original on all %d probes in each round; %s%s. Not checked: %v\n",
						streak, seedsOf(opts.Seed, round, streak), len(probes), authz, pendingNote, check.NotChecked)
				}
				return false, true, nil
			}
			if clean {
				// Nothing to fix, so the agent is not asked. Record the clean round and probe again on a fresh seed.
				if _, err := WriteRoundCheck(opts.Root, opts.Unit, check); err != nil {
					return false, false, err
				}
				page()
				logf("round %d is clean (streak %d of 2). One more clean round on a fresh seed is needed.\n", round, streak)
				return true, false, nil
			}
			if disagree == 0 {
				logf("rbac: %d critical authorization finding(s) block convergence\n", criticalCount(rbacFindings))
				if chainDiverges > 0 {
					logf("chains: %d diverging chain(s) block convergence\n", chainDiverges)
				}
				if len(chainPendingNames) > 0 {
					logf("chains: %d unjudgeable chain target(s) block convergence\n", len(chainPendingNames))
				}
			}
			if added > 0 {
				if err := store.SaveApp(opts.CasesPath, rec); err != nil {
					return false, false, err
				}
			}
			if err := regenerateTests(opts, rec, kind); err != nil {
				return false, false, err
			}
			if opts.Agent == "" {
				check.AgentOutcome = "not run: no agent command was given"
				if _, err := WriteRoundCheck(opts.Root, opts.Unit, check); err != nil {
					return false, false, err
				}
				page()
				if disagree == 0 {
					// A chain divergence gets its own sentence: the generic authorization phrase would name
					// zero critical findings and leave the operator guessing at what actually blocked the round.
					if chainDiverges > 0 {
						msg := fmt.Sprintf("refine: chain probes diverged on %d collection(s) (%s); the rewrite needs updating (run with -agent, or edit units/%s/rewrite.go)",
							chainDiverges, divergedChainTargets(chainTranscripts), opts.Unit)
						if concurrentDiverges > 0 {
							msg += fmt.Sprintf("; the concurrent check also found %d diverging case(s)", concurrentDiverges)
						}
						return false, false, fmt.Errorf("%s", msg)
					}
					// A concurrent divergence gets its own sentence, for the same reason.
					if concurrentDiverges > 0 && criticalCount(rbacFindings) == 0 {
						return false, false, fmt.Errorf("refine: %d case(s) answered identical concurrent requests differently (%s); the rewrite needs updating (run with -agent, or edit units/%s/rewrite.go)",
							concurrentDiverges, strings.Join(divergeNames(concurrent), ", "), opts.Unit)
					}
					// An unjudgeable chain keeps the round from being clean; with nothing else to name, the
					// error must say so, target by target, or the run would look like it failed for nothing.
					if len(chainPendingNames) > 0 && criticalCount(rbacFindings) == 0 {
						return false, false, fmt.Errorf("refine: the chain probes could not be judged on %d collection(s): %s; an unjudgeable chain keeps the round from being clean (rerun, or widen the recording so the original's answer is stable)", len(chainPendingNames), strings.Join(chainPendingReasons, "; "))
					}
					return false, false, fmt.Errorf("refine: %d critical authorization finding(s); the rewrite needs updating (run with -agent, or edit units/%s/rewrite.go)", criticalCount(rbacFindings), opts.Unit)
				}
				return false, false, fmt.Errorf("refine: %d probe(s) disagree; the rewrite needs updating (run with -agent, or edit units/%s/rewrite.go)", disagree, opts.Unit)
			}
			// The failing authorization requests go into the brief, so the agent sees them in its next attempt.
			// The concurrent divergences go in too: the case names and the fact that the same concurrent requests
			// got different answers, never a response body from one of the racing requests. The diverging chains
			// go in with their request sequences, so the agent sees the new requests the replay collapsed on.
			rbacBrief := usecase.RBACBrief(rbacFindings, casesForUnit(rec, opts.Unit, opts.Prefix, kind))
			concurrentBrief := usecase.ConcurrentBrief(divergeNames(concurrent))
			chainBrief := usecase.ChainBrief(opts.Prefix, chainBriefCases)
			brief := func(prev string) string {
				cases := casesForUnit(rec, opts.Unit, opts.Prefix, kind)
				if kind == "cli" {
					return usecase.AgentPromptCLI(opts.Unit, cases, prev)
				}
				return usecase.AgentPrompt(opts.Unit, opts.Prefix, cases, prev) + rbacBrief + concurrentBrief + chainBrief
			}
			logf("asking the agent to fix units/%s/rewrite.go against %d recorded case(s)...\n", opts.Unit, len(casesForUnit(rec, opts.Unit, opts.Prefix, kind)))
			outcome := "passed the recorded cases"
			if _, err := RunAgent(opts.Root, opts.Unit, opts.Agent, opts.Attempts, brief); err != nil {
				outcome = "did not pass: " + err.Error()
				check.AgentOutcome = outcome
				WriteRoundCheck(opts.Root, opts.Unit, check)
				page()
				return false, false, err
			}
			check.AgentOutcome = outcome
			if _, err := WriteRoundCheck(opts.Root, opts.Unit, check); err != nil {
				return false, false, err
			}
			page()
			// Every round-local exit is covered above; falling off the end of the body goes on with the loop.
			return true, false, nil
		}()
		if err != nil {
			return false, err
		}
		if !next {
			return ok, err
		}
	}
	// Running out of rounds with an unjudgeable chain still pending must name the target and the
	// reason: a budget spent without a cause named is a diagnosis lost.
	if len(chainPendingReasons) > 0 {
		return false, fmt.Errorf("refine: not converged after %d round(s); the last clean streak is %d. Chain probes stayed unjudgeable on: %s. Widen -probes or record more cases",
			max(opts.Rounds, 1), streak, strings.Join(chainPendingReasons, "; "))
	}
	return false, fmt.Errorf("refine: not converged after %d round(s); the last clean streak is %d. Widen -probes or record more cases", max(opts.Rounds, 1), streak)
}

// divergedChainTargets lists the collections whose chains diverged, in target order, for the log and
// the no-agent error.
func divergedChainTargets(trs []chainTranscript) string {
	var out []string
	for _, tr := range trs {
		if tr.Verdict.Divergent {
			out = append(out, tr.Target)
		}
	}
	return strings.Join(out, ", ")
}

// nextCleanStreak returns the number of consecutive clean rounds ending at this round. A round that is not
// clean (a disagreement or a blocking finding) resets the streak to 0.
func nextCleanStreak(prev int, clean bool) int {
	if !clean {
		return 0
	}
	return prev + 1
}

// wireOpts is the original-side pacing and retry settings of a refine run.
func wireOpts(opts RefineOptions) WireOptions {
	return WireOptions{Delay: opts.Delay, Retries: opts.Retries, RetryDelay: opts.RetryDelay}
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
		want, err := AskOriginal(kind, opts.OriginalCLI, opts.OriginalURL, p, wireOpts(opts))
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
