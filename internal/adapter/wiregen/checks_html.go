package wiregen

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/go-anything/go-anything/internal/domain"
	"github.com/go-anything/go-anything/internal/usecase"
)

// RefineReport is everything the RSI loop page shows. It is derived from the round records and the
// defects file on disk, so it can be rebuilt at any time and never holds state of its own.
type RefineReport struct {
	Unit        string
	Rounds      []RoundCheck // sorted by round
	Converged   bool
	ConvergedAt int // the round that converged, 0 when the run has not converged
	Defects     []usecase.OriginalDefect
	GeneratedAt string
	// IgnoredRounds counts round records that cannot belong to this run: files older than this run's
	// round 1, or rounds after the one that converged. The page says how many were left out.
	IgnoredRounds int
	// ChainFiles are the base names of the chain transcript files in the checks directory (chain-round-*.json,
	// written by -chains runs). The page links to them; their bodies never enter the page.
	ChainFiles []string
}

// BuildRefineReport assembles the report from decoded round records and defects. It is pure: the caller
// reads the files and supplies the time.
func BuildRefineReport(unit string, rounds []RoundCheck, defects []usecase.OriginalDefect, generatedAt string, ignored int) RefineReport {
	rs := append([]RoundCheck(nil), rounds...)
	sort.SliceStable(rs, func(i, j int) bool { return rs[i].Round < rs[j].Round })
	r := RefineReport{Unit: unit, Defects: append([]usecase.OriginalDefect(nil), defects...), GeneratedAt: generatedAt, IgnoredRounds: ignored}
	// Refine returns as soon as a round converges, so a later round on disk is from another run.
	for i, c := range rs {
		if c.Converged {
			r.Converged, r.ConvergedAt = true, c.Round
			r.IgnoredRounds += len(rs) - i - 1
			rs = rs[:i+1]
			break
		}
	}
	r.Rounds = rs
	return r
}

// DefectNote says what the listed original defects mean under the policy the run used. The page must not
// claim crashes were left out of the recording when the run recorded them.
func DefectNote(policy string) string {
	switch policy {
	case string(usecase.PolicyExpected):
		return "This run used -defect-policy expected: original crashes were recorded as behavior the rewrite must match, so they are not listed here. Entries below are other original defects, such as authorization violations."
	case string(usecase.PolicyDefect):
		return "This run used the default defect policy: these inputs are reported and NOT recorded as expected behavior, so the rewrite is not asked to copy them."
	default:
		return "These round records do not name the defect policy (they were written before it was recorded). Under the default policy these inputs are reported and not recorded as expected behavior."
	}
}

// Bi is domain.Bi under its short name, for the bilingual banner wording below: every fixed
// sentence of the banner is English first, Korean second.
var Bi = domain.Bi

// refineBanner is the verdict line of the page: its style class, the verdict, the detail sentence, and
// the next steps for the operator. Every fixed sentence is bilingual (Bi: English first, Korean under
// it); the numbers, seeds and unit paths inside them stay exactly as recorded. It is pure so the
// wording can be table-tested.
func refineBanner(r RefineReport) (class, verdict, detail string, next []string) {
	if len(r.Rounds) == 0 {
		return "info",
			Bi("No round recorded yet", "아직 기록된 라운드가 없습니다"),
			Bi("The loop has not written a round record for this unit.", "이 루프는 이 유닛의 라운드 기록을 아직 쓰지 않았습니다."),
			[]string{Bi("Run go-anything wire refine for this unit.", "이 유닛에 대해 go-anything wire refine를 실행하세요.")}
	}
	last := r.Rounds[len(r.Rounds)-1]
	defectStep := ""
	if n := defectCount(r); n > 0 {
		defectStep = Bi(
			fmt.Sprintf("Read the %d original defect(s) below. They are reported and not counted as disagreements.", n),
			fmt.Sprintf("아래 원본 결함 %d개를 읽으세요. 이들은 보고되며 불일치로 세지 않습니다.", n))
	}
	addDefect := func(steps []string) []string {
		if defectStep != "" {
			steps = append(steps, defectStep)
		}
		return steps
	}
	if r.Converged {
		n := max(last.CleanStreak, 1)
		seeds := seedsText(r.Rounds, n)
		if n == 1 {
			return "warn",
				Bi("1 clean round ("+seeds+"), not yet converged", "1개 라운드 통과 ("+seeds+"), 아직 일치 완료 단계가 아닙니다"),
				Bi("The rewrite agreed with the original on the sampled inputs of one seed only. Convergence needs 2 consecutive clean rounds on different seeds.", "재작성이 한 시드의 표본 입력에서만 원본과 일치했습니다. 일치 완료에는 서로 다른 시드에서 연속 2개의 통과 라운드가 필요합니다."),
				addDefect([]string{Bi("Rerun with -rounds 2 or more so a second fresh seed is checked.", "두 번째 새 시드가 확인되도록 -rounds 2 이상으로 다시 실행하세요.")})
		}
		detail := Bi(
			fmt.Sprintf("The rewrite agreed with the original on every probe in each of those rounds (%d probes in the last round). This is sampled agreement, not proof for every input: see Not checked.", last.Requests),
			fmt.Sprintf("재작성이 해당 라운드 각각의 모든 점검 요청에서 원본과 일치했습니다 (마지막 라운드에 점검 요청 %d개). 이것은 표본 일치이지 모든 입력에 대한 증명이 아닙니다: 확인하지 않은 항목을 보세요.", last.Requests))
		// A green banner over untried input classes overstates the evidence, so missing classes lower it.
		if miss := len(last.MissingClasses); miss > 0 {
			detail = Bi(
				fmt.Sprintf("The rewrite agreed on every probe, but %d of %d input classes got no probe value (%s), so the agreement says nothing about them.", miss, len(last.ValueClasses), strings.Join(last.MissingClasses, ", ")),
				fmt.Sprintf("재작성이 모든 점검 요청에서 일치했지만 입력 클래스 %d개 중 %d개에 점검 요청 값이 없습니다 (%s). 따라서 그 클래스에 대해서는 이 일치가 아무것도 말해 주지 않습니다.", len(last.ValueClasses), miss, strings.Join(last.MissingClasses, ", ")))
			return "warn",
				Bi(fmt.Sprintf("Converged after %d consecutive clean rounds (%s), with untried input classes", n, seeds),
					fmt.Sprintf("연속 %d개 통과 라운드 후 일치 완료 (%s), 시도하지 않은 입력 클래스가 있습니다", n, seeds)),
				detail, addDefect([]string{Bi("Rerun with a larger -probes so every input class gets a value.", "모든 입력 클래스가 값을 갖도록 -probes를 크게 하여 다시 실행하세요.")})
		}
		return "pass",
			Bi(fmt.Sprintf("Converged after %d consecutive clean rounds (%s)", n, seeds),
				fmt.Sprintf("연속 %d개 통과 라운드 후 일치 완료 (%s)", n, seeds)),
			detail, addDefect(nil)
	}
	if n := len(last.ExtraDisagreements); n > 0 {
		return "fail",
			Bi(fmt.Sprintf("Not converged: %d extra input request(s) disagree after the probes converged in round %d", n, last.Round),
				fmt.Sprintf("불일치 잔존: 점검 요청은 라운드 %d까지 일치했지만 추가 입력 요청 %d개가 불일치합니다", last.Round, n)),
			Bi(fmt.Sprintf("The probes agreed for %d consecutive round(s), but values from -extra-inputs, outside the probe generator, found a difference. See Extra inputs.", last.CleanStreak),
				fmt.Sprintf("점검 요청은 %d개 라운드 연속으로 일치했지만, 점검 요청 생성기 밖의 -extra-inputs 값이 차이를 찾았습니다. 추가 입력을 보세요.", last.CleanStreak)),
			addDefect([]string{Bi("Read the extra input disagreements below. Record them as cases or fix units/"+r.Unit+"/rewrite.go, then rerun refine.",
				"아래 추가 입력 불일치를 읽으세요. 케이스로 기록하거나 units/"+r.Unit+"/rewrite.go를 수정한 뒤 refine을 다시 실행하세요.")})
	}
	if last.Disagreements > 0 {
		steps := []string{agentStep(last, r.Unit)}
		return "fail",
			Bi(fmt.Sprintf("Not converged: %d disagreement(s) with the original in round %d", last.Disagreements, last.Round),
				fmt.Sprintf("불일치 잔존: 라운드 %d에서 원본과 %d건이 불일치합니다", last.Round, last.Disagreements)),
			Bi(fmt.Sprintf("%d new case(s) were recorded in that round. The clean streak is 0 of 2.", len(last.NewCases)),
				fmt.Sprintf("그 라운드에서 새 케이스 %d개가 기록되었습니다. 연속 통과는 2회 중 0회입니다.", len(last.NewCases))),
			addDefect(steps)
	}
	if c := criticalCount(last.RBACFindings); c > 0 {
		return "fail",
			Bi(fmt.Sprintf("Not converged: %d critical authorization finding(s) in round %d", c, last.Round),
				fmt.Sprintf("불일치 잔존: 라운드 %d에서 치명적 권한 검사 결과가 %d건 있습니다", last.Round, c)),
			Bi("The probes agreed, but the authorization check found the rewrite answering against the declared policy.", "점검 요청은 일치했지만, 권한 검사가 재작성이 선언된 정책에 어긋나게 응답함을 찾았습니다."),
			addDefect([]string{Bi("Read the authorization findings below.", "아래 권한 검사 결과를 읽으세요."), agentStep(last, r.Unit)})
	}
	return "warn",
		Bi(fmt.Sprintf("Not converged yet: clean streak %d of 2 after round %d", last.CleanStreak, last.Round),
			fmt.Sprintf("아직 불일치 잔존: 라운드 %d 이후 연속 통과는 2회 중 %d회입니다", last.Round, last.CleanStreak)),
		Bi("The last round was clean, but convergence needs two consecutive clean rounds on fresh seeds.", "마지막 라운드는 통과했지만, 일치 완료에는 새 시드에서 연속으로 두 라운드를 통과해야 합니다."),
		addDefect([]string{Bi("Run more rounds (-rounds) so another fresh seed is checked. If the run is still going, reload this page after the next round.",
			"다른 새 시드가 확인되도록 라운드를 더 실행하세요 (-rounds). 실행이 계속되고 있다면 다음 라운드 뒤에 이 페이지를 다시 여세요.")})
}

// agentStep says what to do about a round that needs a fix, from what the agent did in that round.
func agentStep(c RoundCheck, unit string) string {
	switch {
	case c.AgentOutcome == "":
		return Bi("No agent outcome is recorded for this round. If the run stopped here, rerun refine with -agent, or edit units/"+unit+"/rewrite.go.",
			"이 라운드에는 에이전트 결과가 기록되지 않았습니다. 실행이 여기서 멈췄다면 -agent와 함께 refine을 다시 실행하거나 units/"+unit+"/rewrite.go를 수정하세요.")
	case strings.HasPrefix(c.AgentOutcome, "not run"):
		return Bi("No agent ran. Rerun refine with -agent, or edit units/"+unit+"/rewrite.go to match the recorded cases, then run refine again.",
			"에이전트가 실행되지 않았습니다. -agent와 함께 refine을 다시 실행하거나, units/"+unit+"/rewrite.go를 기록된 케이스에 맞게 수정한 뒤 refine을 다시 실행하세요.")
	case strings.HasPrefix(c.AgentOutcome, "did not pass"):
		return Bi("Check the agent outcome in the rounds table: the agent did not pass the recorded cases.",
			"라운드 표의 에이전트 결과를 확인하세요: 에이전트가 기록된 케이스를 통과하지 못했습니다.")
	default:
		return Bi("The agent passed the recorded cases. Run more rounds so a fresh seed checks the fix.",
			"에이전트가 기록된 케이스를 통과했습니다. 새 시드가 수정을 확인하도록 라운드를 더 실행하세요.")
	}
}

// seedsText names the seeds of the last n rounds, as the refine log does.
func seedsText(rounds []RoundCheck, n int) string {
	if n > len(rounds) {
		n = len(rounds)
	}
	tail := rounds[len(rounds)-n:]
	seeds := make([]string, len(tail))
	for i, c := range tail {
		seeds[i] = fmt.Sprint(c.Seed)
	}
	switch len(seeds) {
	case 0:
		return "no seed recorded"
	case 1:
		return "seed " + seeds[0]
	case 2:
		return "seeds " + seeds[0] + " and " + seeds[1]
	default:
		return "seeds " + strings.Join(seeds[:len(seeds)-1], ", ") + " and " + seeds[len(seeds)-1]
	}
}

// defectCount is the number of original defects: the defects file when present, else the last round's count.
func defectCount(r RefineReport) int {
	if len(r.Defects) > 0 || len(r.Rounds) == 0 {
		return len(r.Defects)
	}
	return r.Rounds[len(r.Rounds)-1].OriginalDefects
}

// defectReason says why an entry is a defect, from the evidence shape that DefectLog writes.
func defectReason(d usecase.OriginalDefect) string {
	switch {
	case strings.HasPrefix(d.Evidence, "exit "):
		return "The original crashed: it exited non-zero with a Python traceback or a Go panic on stderr. A crash is not behavior the rewrite should copy."
	case strings.HasPrefix(d.Evidence, "status "):
		return "The original answered 5xx with a traceback or panic in the body. A crash is not behavior the rewrite should copy."
	default:
		return "The authorization check found the original answering against the declared access policy for this principal."
	}
}

type pageCard struct {
	Key template.HTML
	// Href, when set, turns the card's value into a link to a file that sits next to this page in the
	// checks directory (a relative link, so the page stays self-contained apart from its own files).
	Href              string
	Value, Sub, Class string
}

type pageRound struct {
	Round                  int
	Seed                   uint64
	Requests, Values       int
	Boundary, Generated    int
	Shapes                 int
	Disagreements, BarPct  int
	NewCases               int
	NewCaseNames           string
	Defects, Streak        int
	Converged              bool
	Agent, Confidence      string
	ConfidenceText         string
	AgentLabel             string
	AgentClass, RowVerdict string
}

type pageClass struct {
	Name    string
	Count   int
	Missing bool
}

type pageDefect struct {
	Input, Did, Why string
}

type pageFinding struct {
	Severity, Class, Case, Principal, Detail string
}

type pageExtra struct {
	Value, Request, Want, Got string
}

type refinePage struct {
	ExtraChecked, ExtraRequests int
	Extra                       []pageExtra
	Unit, GeneratedAt           string
	RoundsRun, Ignored          int
	BannerClass                 string
	Verdict, Detail             template.HTML
	Next                        []template.HTML
	Cards                       []pageCard
	Rounds                      []pageRound
	Trend                       string
	HasLast                     bool
	LastRound                   int
	Classes                     []pageClass
	Missing                     []string
	Defects                     []pageDefect
	DefectCount                 int
	DefectFileMissing           bool
	RBACChecked                 bool
	RBACPrincipals              int
	Findings                    []pageFinding
	NotChecked, Derived         []string
	ValueLimit                  int
	DefectNote                  string
	CSS                         template.CSS
	ConfidenceNotes             []string
}

// confidenceLabel is the short head of a confidence sentence ("sampled agreement"), for the table.
func confidenceLabel(s string) string {
	if i := strings.Index(s, ":"); i > 0 {
		return clip(s[:i])
	}
	return clip(s)
}

func clip(s string) string { return domain.ClipForReport(s, domain.ReportValueLimit) }

// biTitle is a card title in both languages: English on the title line, Korean as a muted line under
// it. The value and sub lines carry recorded data and stay exactly as they are.
func biTitle(en, ko string) template.HTML { return template.HTML(domain.Bi(en, ko)) }

// biText marks one prebuilt bilingual string (already made with domain.Bi) safe for the template.
func biText(s string) template.HTML { return template.HTML(s) }

func buildPage(r RefineReport) refinePage {
	p := refinePage{Unit: r.Unit, GeneratedAt: r.GeneratedAt, RoundsRun: len(r.Rounds), Ignored: r.IgnoredRounds,
		CSS: template.CSS(domain.ReportCSS), ValueLimit: domain.ReportValueLimit}
	if len(r.Rounds) > 0 {
		p.DefectNote = DefectNote(r.Rounds[len(r.Rounds)-1].DefectPolicy)
	} else {
		p.DefectNote = DefectNote("")
	}
	class, verdict, detail, next := refineBanner(r)
	p.BannerClass = class
	p.Verdict, p.Detail = biText(verdict), biText(detail)
	for _, s := range next {
		p.Next = append(p.Next, biText(s))
	}

	maxD, totalNew := 0, 0
	var trend []string
	for _, c := range r.Rounds {
		maxD = max(maxD, c.Disagreements)
		totalNew += len(c.NewCases)
		trend = append(trend, fmt.Sprint(c.Disagreements))
	}
	p.Trend = strings.Join(trend, " → ")
	seenNote := map[string]bool{}
	for _, c := range r.Rounds {
		if c.Confidence != "" && !seenNote[c.Confidence] {
			seenNote[c.Confidence] = true
			p.ConfidenceNotes = append(p.ConfidenceNotes, clip(c.Confidence))
		}
	}
	for _, c := range r.Rounds {
		pct := 0
		if maxD > 0 {
			pct = c.Disagreements * 100 / maxD
		}
		// A short badge for scanning, and the recorded outcome in full (clipped) under it.
		agent, agentLabel, agentClass := c.AgentOutcome, "", ""
		switch {
		case agent == "" && c.Disagreements == 0 && criticalCount(c.RBACFindings) == 0:
			agentLabel, agentClass = "not needed", "info"
			agent = "clean round"
		case agent == "":
			agentLabel, agentClass = "not recorded", "warn"
		case strings.HasPrefix(agent, "did not pass"):
			agentLabel, agentClass = "failed", "bad"
		case strings.HasPrefix(agent, "not run"):
			agentLabel, agentClass = "not run", "warn"
		default:
			agentLabel, agentClass = "passed", "ok"
		}
		verdict := "clean"
		if c.Disagreements > 0 || criticalCount(c.RBACFindings) > 0 || len(c.ExtraDisagreements) > 0 {
			verdict = "dirty"
		}
		p.Rounds = append(p.Rounds, pageRound{Round: c.Round, Seed: c.Seed, Requests: c.Requests, Values: c.Values,
			Boundary: c.BoundaryValues, Generated: c.GeneratedValues, Shapes: c.ShapeRequests, Disagreements: c.Disagreements, BarPct: pct,
			NewCases: len(c.NewCases), NewCaseNames: clip(strings.Join(c.NewCases, ", ")), Defects: c.OriginalDefects,
			Streak: c.CleanStreak, Converged: c.Converged, Agent: clip(agent), AgentLabel: agentLabel, AgentClass: agentClass,
			Confidence: confidenceLabel(c.Confidence), ConfidenceText: clip(c.Confidence), RowVerdict: verdict})
	}

	p.DefectCount = defectCount(r)
	for _, d := range r.Defects {
		p.Defects = append(p.Defects, pageDefect{Input: clip(d.Request), Did: clip(d.Evidence), Why: defectReason(d)})
	}

	if len(r.Rounds) == 0 {
		p.Cards = []pageCard{{Key: biTitle("Rounds", "라운드"), Value: "0", Sub: "nothing recorded", Class: "warn"}}
		p.NotChecked = notCheckedCommon
		return p
	}
	last := r.Rounds[len(r.Rounds)-1]
	p.HasLast, p.LastRound = true, last.Round
	p.DefectFileMissing = len(r.Defects) == 0 && last.OriginalDefects > 0

	streakClass := "warn"
	if last.CleanStreak >= 2 {
		streakClass = "ok"
	} else if last.CleanStreak == 0 {
		streakClass = "bad"
	}
	disClass := "ok"
	if last.Disagreements > 0 {
		disClass = "bad"
	}
	defClass := "ok"
	if p.DefectCount > 0 {
		defClass = "warn"
	}
	rbacCard := pageCard{Key: biTitle("RBAC checked", "RBAC 확인"), Value: "no", Sub: "no -rbac-principals and -rbac-policy (HTTP units only)", Class: "warn"}
	if last.Disagreements > 0 {
		rbacCard.Sub = "skipped or not configured: it runs only where the probes agree"
	}
	if last.RBACChecked {
		rbacCard = pageCard{Key: biTitle("RBAC checked", "RBAC 확인"), Value: "yes", Sub: fmt.Sprintf("%d principal(s), %d finding(s)", last.RBACPrincipals, len(last.RBACFindings)), Class: "ok"}
		if criticalCount(last.RBACFindings) > 0 {
			rbacCard.Class = "bad"
		}
	}
	p.Cards = []pageCard{
		{Key: biTitle("Rounds", "라운드"), Value: fmt.Sprint(len(r.Rounds)), Sub: "round records of this run"},
		{Key: biTitle("Clean streak", "연속 통과"), Value: fmt.Sprintf("%d of 2", last.CleanStreak), Sub: "consecutive clean rounds", Class: streakClass},
		{Key: biTitle("Disagreements", "불일치"), Value: fmt.Sprint(last.Disagreements), Sub: fmt.Sprintf("in round %d", last.Round), Class: disClass},
		{Key: biTitle("New cases", "새 케이스"), Value: fmt.Sprint(totalNew), Sub: "recorded in all rounds"},
		{Key: biTitle("Original defects", "원본 결함"), Value: fmt.Sprint(p.DefectCount), Sub: "reported, not recorded", Class: defClass},
		rbacCard,
	}
	// The concurrent check shows one count card, and only for a round that ran it: a run without
	// -concurrency keeps the page it always had. Counts and case names only — no response body from any
	// racing request reaches the page.
	if last.ConcurrentChecked > 0 {
		class := "ok"
		if last.ConcurrentDiverges > 0 {
			class = "bad"
		} else if len(last.ConcurrentPending) > 0 {
			class = "warn"
		}
		p.Cards = append(p.Cards, pageCard{Key: biTitle("Concurrent check", "동시 요청 점검"),
			Value: fmt.Sprint(last.ConcurrentChecked),
			Sub:   fmt.Sprintf("%d case(s) asked, %d diverge, %d pending", last.ConcurrentChecked, last.ConcurrentDiverges, len(last.ConcurrentPending)),
			Class: class})
	}
	// The chain check shows one count card, and only for a round that ran it. It links to the round's
	// chain transcript file — counts and file names only, never a request or response body: the
	// transcripts stay in the files the page links to.
	if last.ChainsChecked > 0 {
		class := "ok"
		if last.ChainDiverges > 0 {
			class = "bad"
		} else if len(last.ChainPending) > 0 {
			class = "warn"
		}
		main := fmt.Sprintf("chain-round-%02d.json", last.Round)
		extra := len(r.ChainFiles)
		for _, f := range r.ChainFiles {
			if f == main {
				extra--
			}
		}
		sub := fmt.Sprintf("%d target(s), %d request(s) per side, %d diverge, %d pending — transcript %s", last.ChainsChecked, last.ChainRequests, last.ChainDiverges, len(last.ChainPending), main)
		if extra > 0 {
			sub += fmt.Sprintf(" (+%d per-target file(s))", extra)
		}
		card := pageCard{Key: biTitle("Chain probes", "연속 요청 점검"), Value: fmt.Sprint(last.ChainsChecked), Sub: sub, Class: class}
		for _, f := range r.ChainFiles {
			if f == main {
				card.Href = f
			}
		}
		p.Cards = append(p.Cards, card)
	}

	// Classes in ProbeClasses order, then any other recorded class. A class with no value is missing.
	missing := map[string]bool{}
	for _, m := range last.MissingClasses {
		missing[m] = true
	}
	names := domain.ProbeClasses()
	known := map[string]bool{}
	for _, n := range names {
		known[n] = true
	}
	var extra []string
	for n := range last.ValueClasses {
		if !known[n] {
			extra, known[n] = append(extra, n), true
		}
	}
	for n := range missing {
		if !known[n] {
			extra, known[n] = append(extra, n), true
		}
	}
	sort.Strings(extra)
	for _, n := range append(names, extra...) {
		count, recorded := last.ValueClasses[n]
		if !recorded && !missing[n] {
			continue
		}
		p.Classes = append(p.Classes, pageClass{n, count, missing[n] || count == 0})
	}
	for _, c := range p.Classes {
		if c.Missing {
			p.Missing = append(p.Missing, c.Name)
		}
	}

	p.ExtraChecked, p.ExtraRequests = last.ExtraChecked, last.ExtraRequests
	for _, d := range last.ExtraDisagreements {
		p.Extra = append(p.Extra, pageExtra{clip(d.Value), clip(d.Request), clip(d.Want), clip(d.Got)})
	}
	if last.ExtraChecked == 0 {
		p.Derived = append(p.Derived, "values outside the probe generator: no -extra-inputs were cross-checked after convergence")
	}
	p.RBACChecked, p.RBACPrincipals = last.RBACChecked, last.RBACPrincipals
	for _, f := range last.RBACFindings {
		class := "info"
		switch f.Severity {
		case usecase.SeverityCritical:
			class = "bad"
		case usecase.SeverityHigh:
			class = "warn"
		}
		p.Findings = append(p.Findings, pageFinding{string(f.Severity), class, clip(f.Case), clip(f.Principal), clip(f.Detail)})
	}

	for _, n := range last.NotChecked {
		p.NotChecked = append(p.NotChecked, clip(n))
	}
	if !last.RBACChecked {
		p.Derived = append(p.Derived, "authorization (RBAC): not checked in the last round")
	}
	if len(p.Missing) > 0 {
		p.Derived = append(p.Derived, "input classes with no probe value in the last round: "+strings.Join(p.Missing, ", "))
	}
	if r.Converged && last.CleanStreak < 2 {
		p.Derived = append(p.Derived, "a second fresh seed: the run converged on a single round")
	}
	return p
}

// RenderRefineHTML renders the report as one self-contained page: no scripts, no external assets.
func RenderRefineHTML(r RefineReport) ([]byte, error) {
	var buf bytes.Buffer
	if err := refineTemplate.Execute(&buf, buildPage(r)); err != nil {
		return nil, fmt.Errorf("refine page: render: %w", err)
	}
	return buf.Bytes(), nil
}

// LoadRefineReport reads the round records of a unit and the defects file. A record older than this
// run's round 1 is left over from an earlier, longer run: it is not shown, only counted. A missing
// defects file means no defect (refine removes the file when the run found none).
func LoadRefineReport(root, unit, defectsPath string, now time.Time) (RefineReport, error) {
	dir := filepath.Join(root, ".go-anything", "checks", unit)
	paths, err := filepath.Glob(filepath.Join(dir, "round-*.json"))
	if err != nil {
		return RefineReport{}, fmt.Errorf("refine page: list rounds: %w", err)
	}
	type rec struct {
		c   RoundCheck
		mod time.Time
	}
	var recs []rec
	var first time.Time
	for _, p := range paths {
		info, err := os.Stat(p)
		if err != nil {
			return RefineReport{}, fmt.Errorf("refine page: %w", err)
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return RefineReport{}, fmt.Errorf("refine page: %w", err)
		}
		var c RoundCheck
		if err := json.Unmarshal(data, &c); err != nil {
			return RefineReport{}, fmt.Errorf("refine page: decode %s: %w", filepath.Base(p), err)
		}
		recs = append(recs, rec{c, info.ModTime()})
		if c.Round == 1 {
			first = info.ModTime()
		}
	}
	var rounds []RoundCheck
	ignored := 0
	for _, r := range recs {
		if !first.IsZero() && r.mod.Before(first) {
			ignored++
			continue
		}
		rounds = append(rounds, r.c)
	}
	var defects []usecase.OriginalDefect
	if defectsPath != "" {
		data, err := os.ReadFile(defectsPath)
		switch {
		case errors.Is(err, fs.ErrNotExist):
		case err != nil:
			return RefineReport{}, fmt.Errorf("refine page: %w", err)
		default:
			if err := json.Unmarshal(data, &defects); err != nil {
				return RefineReport{}, fmt.Errorf("refine page: decode defects: %w", err)
			}
		}
	}
	r := BuildRefineReport(unit, rounds, defects, now.UTC().Format("2006-01-02 15:04:05 UTC"), ignored)
	r.ChainFiles = loadChainFiles(root, unit)
	return r, nil
}

// loadChainFiles lists the chain transcript file names in a unit's checks directory, sorted. The page
// links to them by base name; it never opens them, so no recorded answer reaches the page. A directory
// that holds no chain files (an option-off run) yields nil.
func loadChainFiles(root, unit string) []string {
	dir := filepath.Join(root, ".go-anything", "checks", unit)
	paths, err := filepath.Glob(filepath.Join(dir, "chain-round-*.json"))
	if err != nil {
		return nil
	}
	sort.Strings(paths)
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		out = append(out, filepath.Base(p))
	}
	return out
}

// WriteRefineHTML rebuilds <root>/.go-anything/checks/<unit>/index.html from the files on disk. The page
// is written to a temporary file and renamed, so a reader never sees half a page.
func WriteRefineHTML(root, unit, defectsPath string, now time.Time) (string, error) {
	r, err := LoadRefineReport(root, unit, defectsPath, now)
	if err != nil {
		return "", err
	}
	data, err := RenderRefineHTML(r)
	if err != nil {
		return "", err
	}
	dir := filepath.Join(root, ".go-anything", "checks", unit)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("refine page: create %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".index-*.html")
	if err != nil {
		return "", fmt.Errorf("refine page: %w", err)
	}
	defer os.Remove(tmp.Name()) // no-op after a successful rename
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return "", fmt.Errorf("refine page: write: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return "", fmt.Errorf("refine page: write: %w", err)
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		return "", fmt.Errorf("refine page: %w", err)
	}
	p := filepath.Join(dir, "index.html")
	if err := os.Rename(tmp.Name(), p); err != nil {
		return "", fmt.Errorf("refine page: write %s: %w", p, err)
	}
	return p, nil
}

// writeRefinePage is the call refine makes after each round record. The page is derived data: a failure
// is logged in one line and never fails the run.
func writeRefinePage(root, unit, defectsPath string, logf func(string, ...any)) {
	p, err := WriteRefineHTML(root, unit, defectsPath, time.Now())
	if err != nil {
		logf("rsi page not written (the run continues): %v\n", err)
		return
	}
	logf("rsi page: %s\n", p)
}

// refineFuncs holds the bilingual helper the page template marks fixed prose with.
var refineFuncs = template.FuncMap{
	"bi": func(en, ko string) template.HTML { return template.HTML(domain.Bi(en, ko)) },
}

var refineTemplate = template.Must(template.New("refine").Funcs(refineFuncs).Parse(`<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>RSI loop: {{.Unit}}</title>
<style>{{.CSS}}
  td.bar-cell { min-width: 120px; }
  td.bar-cell .bar { display: inline-block; width: 80px; vertical-align: middle; margin-left: .4rem; }
  tr.dirty td:first-child { border-left: 3px solid var(--bad); }
  tr.clean td:first-child { border-left: 3px solid var(--ok); }
  td.num .note { white-space: normal; text-align: right; }
  td.probes { min-width: 120px; }
  table.rounds { font-size: .9rem; }
  .trend { font-family: ui-monospace, SFMono-Regular, Menlo, monospace; }
</style>
</head>
<body>
<h1>RSI loop: {{.Unit}}</h1>
{{/* The English title line stays bare; the Korean line mirrors it underneath. */}}
{{bi "" (printf "RSI 루프: %s" .Unit)}}
<p class="meta">{{.RoundsRun}} round(s) run · generated {{.GeneratedAt}}{{if .Ignored}} · {{.Ignored}} older round record(s) from an earlier run not shown{{end}}</p>
<nav class="toc"><a href="#rounds">{{bi "Rounds" "라운드"}}</a><a href="#classes">{{bi "Value classes" "값 클래스"}}</a><a href="#defects">{{bi "Original defects" "원본 결함"}}</a><a href="#extra">{{bi "Extra inputs" "추가 입력"}}</a><a href="#rbac">{{bi "Authorization" "권한 검사"}}</a><a href="#not-checked">{{bi "Not checked" "확인하지 않은 항목"}}</a></nav>

<div class="banner {{.BannerClass}}">
  <strong class="verdict">{{.Verdict}}</strong>
  <p>{{.Detail}}</p>
  {{if .Next}}<p><b>{{bi "Next:" "다음:"}}</b></p><ol class="actions">{{range .Next}}<li>{{.}}</li>{{end}}</ol>{{end}}
</div>

<div class="cards">
{{range .Cards}}  <div class="card {{.Class}}"><div class="k">{{.Key}}</div><div class="v">{{if .Href}}<a href="{{.Href}}">{{.Value}}</a>{{else}}{{.Value}}{{end}}</div><div class="s">{{.Sub}}</div></div>
{{end}}</div>

<h2 id="rounds">Rounds</h2>
{{if .Rounds}}<p class="note">Disagreements by round: <span class="trend">{{.Trend}}</span>. A clean round has no disagreement and no critical authorization finding. Original defects are counted for the whole run.</p>
<table class="rounds">
<tr><th class="num">{{bi "Round" "라운드"}}</th><th class="num">{{bi "Seed" "시드"}}</th><th class="num">{{bi "Probes" "점검 요청"}}</th><th>{{bi "Disagreements" "불일치"}}</th><th class="num">{{bi "New cases" "새 케이스"}}</th><th class="num">{{bi "Original defects" "원본 결함"}}</th><th class="num">{{bi "Clean streak" "연속 통과"}}</th><th>{{bi "Agent outcome" "에이전트 결과"}}</th><th>{{bi "Confidence" "확신도"}}</th></tr>
{{range .Rounds}}<tr class="{{.RowVerdict}}">
  <td class="num">{{.Round}}{{if .Converged}}<div>{{if ge .Streak 2}}<span class="badge ok">converged</span>{{else}}<span class="badge warn">stopped after 1 round</span>{{end}}</div>{{end}}</td>
  <td class="num">{{.Seed}}</td>
  <td class="num probes">{{.Requests}}<div class="note">{{.Boundary}} boundary, {{.Generated}} generated values{{if .Shapes}}, {{.Shapes}} request-shape{{end}}</div></td>
  <td class="num bar-cell">{{.Disagreements}}<span class="bar low"><span style="width: {{.BarPct}}%"></span></span></td>
  <td class="num">{{.NewCases}}{{if .NewCaseNames}}<div class="note mono">{{.NewCaseNames}}</div>{{end}}</td>
  <td class="num">{{.Defects}}</td>
  <td class="num">{{.Streak}}</td>
  <td><span class="badge {{.AgentClass}}">{{.AgentLabel}}</span>{{if .Agent}}<div class="note">{{.Agent}}</div>{{end}}</td>
  <td title="{{.ConfidenceText}}"><span class="badge {{if eq .RowVerdict "clean"}}ok{{else}}bad{{end}}">{{.Confidence}}</span></td>
</tr>
{{end}}</table>
<p class="note">Confidence, as each round records it: {{range .ConfidenceNotes}}<br>{{.}}{{end}}</p>{{else}}<p class="note">No round record yet.</p>{{end}}

<h2 id="classes">{{if .HasLast}}{{bi (printf "Value classes in round %d" .LastRound) (printf "값 클래스 (라운드 %d)" .LastRound)}}{{else}}{{bi "Value classes" "값 클래스"}}{{end}}</h2>
{{if .Missing}}<p>{{bi "Missing classes:" "누락된 클래스:"}} {{range .Missing}}<span class="badge warn">{{.}}</span> {{end}}</p>
<p class="note">No probe value of these classes was tried, so agreement says nothing about them.</p>{{else if .Classes}}<p class="note">Every input class had at least one probe value.</p>{{end}}
{{if .Classes}}<table>
<tr><th>Class</th><th class="num">Values</th><th>Status</th></tr>
{{range .Classes}}<tr><td class="mono">{{.Name}}</td><td class="num">{{.Count}}</td><td>{{if .Missing}}<span class="badge warn">missing</span>{{else}}<span class="badge ok">covered</span>{{end}}</td></tr>
{{end}}</table>{{else}}<p class="note">No value class was recorded.</p>{{end}}

<h2 id="defects">{{bi "Original defects" "원본 결함"}}</h2>
<p class="note">Inputs where the original itself failed. {{.DefectNote}} Values are clipped to {{.ValueLimit}} bytes.</p>
{{if .Defects}}<table>
<tr><th>Input</th><th>What the original did</th><th>Why it is a defect</th></tr>
{{range .Defects}}<tr><td class="val">{{.Input}}</td><td><pre class="val">{{.Did}}</pre></td><td>{{.Why}}</td></tr>
{{end}}</table>{{else if .DefectFileMissing}}<p><span class="badge warn">defects file missing</span> The last round counts {{.DefectCount}} original defect(s), but the defects file could not be found.</p>
{{else}}<p>No original defect was found in this run.</p>{{end}}

<h2 id="extra">Extra inputs{{if .HasLast}} in round {{.LastRound}}{{end}}</h2>
{{if .ExtraChecked}}<p>{{.ExtraChecked}} value(s) from -extra-inputs, sent as {{.ExtraRequests}} request(s) to the rewrite and the original after the probes converged. {{if .Extra}}<span class="badge bad">{{len .Extra}} disagree</span>{{else}}<span class="badge ok">all agree</span>{{end}}</p>
{{if .Extra}}<table>
<tr><th>Value</th><th>Request</th><th>Original (want)</th><th>Rewrite (got)</th></tr>
{{range .Extra}}<tr><td class="val">{{.Value}}</td><td class="mono">{{.Request}}</td><td><pre class="val">{{.Want}}</pre></td><td><pre class="val">{{.Got}}</pre></td></tr>
{{end}}</table>{{end}}{{else}}<p class="note">No extra input was cross-checked. Give -extra-inputs FILE to probe values outside the generator once the loop converges.</p>{{end}}

<h2 id="rbac">Authorization (RBAC){{if .HasLast}} in round {{.LastRound}}{{end}}</h2>
{{if .RBACChecked}}<p>Checked as {{.RBACPrincipals}} principal(s). {{if not .Findings}}No finding.{{end}}</p>
{{if .Findings}}<table>
<tr><th>Severity</th><th>Case</th><th>Principal</th><th>Detail</th></tr>
{{range .Findings}}<tr><td><span class="badge {{.Class}}">{{.Severity}}</span></td><td class="mono">{{.Case}}</td><td class="mono">{{.Principal}}</td><td>{{.Detail}}</td></tr>
{{end}}</table>{{end}}{{else}}<p><span class="badge warn">not checked</span> The authorization check did not run in this round. It runs only on HTTP units with -rbac-principals and -rbac-policy, and only where the probes agree.</p>{{end}}

<h2 id="not-checked">{{bi "Not checked" "확인하지 않은 항목"}}</h2>
<p class="note">{{bi "The limit of the convergence claim. Agreement on sampled probes does not cover these." "일치 완료 주장의 한계입니다. 표본 점검 요청과의 일치가 이 항목들을 뒷받침하지는 않습니다."}}</p>
{{if .NotChecked}}<ul>{{range .NotChecked}}<li>{{.}}</li>{{end}}</ul>{{else}}<p><span class="badge warn">empty</span> The round record lists nothing here. That does not mean everything was checked.</p>{{end}}
{{if .Derived}}<p class="note">Also not covered, read from the round record by this page:</p><ul>{{range .Derived}}<li>{{.}}</li>{{end}}</ul>{{end}}

<footer>{{bi (printf "Built from .go-anything/checks/%s/round-NN.json and the recording's .defects.json. Rebuilt after every round; safe to delete." .Unit) (printf ".go-anything/checks/%s/round-NN.json과 기록의 .defects.json에서 만든 페이지입니다. 매 라운드 뒤 다시 생성되며 삭제해도 안전합니다." .Unit)}}</footer>
</body>
</html>
`))
