package domain

import (
	"html"
	"strings"
	"testing"
	"time"
)

// secureTestTime is fixed so the RFC3339 line is stable across runs.
var secureTestTime = time.Date(2025, 3, 14, 9, 26, 53, 0, time.UTC)

// secureTestReport builds a report over the full checklist. Auto and tool items pass by default,
// manual items need a person, and overrides replace the result of single items.
func secureTestReport(overrides map[string]SecureResult) SecureReport {
	items := SecurityChecklist()
	rep := SecureReport{
		Root:         "/work/module",
		GeneratedAt:  secureTestTime,
		ToolVersion:  "test-1",
		Items:        items,
		GoVersion:    "go1.99.0",
		ModulePath:   "example.com/mod",
		ScannedFiles: 12,
	}
	for _, it := range items {
		res := SecureResult{Item: it.ID, Status: SecurePass}
		if it.Method == SecureManual {
			res.Status = SecureStatusManual
		}
		if ov, ok := overrides[it.ID]; ok {
			res = ov
		}
		rep.Results = append(rep.Results, res)
	}
	return rep
}

// hasPairAlongside reports whether some occurrence of ko has en within window bytes after it.
// That is how "Korean first, English right underneath" looks in the markup: the pair may sit in
// one badge, one card, or two stacked lines.
func hasPairAlongside(got, ko, en string, window int) bool {
	for i := 0; ; {
		j := strings.Index(got[i:], ko)
		if j < 0 {
			return false
		}
		at := i + j
		end := at + len(ko) + window
		if end > len(got) {
			end = len(got)
		}
		if strings.Contains(got[at:end], en) {
			return true
		}
		i = at + len(ko)
	}
}

func TestSecureReportHTML(t *testing.T) {
	cases := []struct {
		name   string
		report SecureReport
		check  func(t *testing.T, got string)
	}{
		{
			name: "mandatory failure reddens banner and shows the finding",
			report: secureTestReport(map[string]SecureResult{
				"SF-1.1": {Item: "SF-1.1", Status: SecureFail, Findings: []SecureFinding{
					{Item: "SF-1.1", Pos: "auth.go:42", Detail: "hardcoded password"},
				}},
			}),
			check: func(t *testing.T, got string) {
				if !strings.Contains(got, "필수 항목 미충족") {
					t.Error("Korean fail headline missing")
				}
				if !strings.Contains(got, "Mandatory items failed") {
					t.Error("English fail headline missing")
				}
				if !strings.Contains(got, `class="sc-banner sc-bad"`) {
					t.Error("banner is not red")
				}
				if strings.Contains(got, "필수 항목 충족") {
					t.Error("green headline must not appear on a failing report")
				}
				if !strings.Contains(got, "SF-1.1") || !strings.Contains(got, "auth.go:42") || !strings.Contains(got, "hardcoded password") {
					t.Error("failing row or its finding missing")
				}
				if !strings.Contains(got, `class="sc-badge sc-fail"`) {
					t.Error("fail badge missing")
				}
			},
		},
		{
			name:   "all pass renders the green banner",
			report: secureTestReport(nil),
			check: func(t *testing.T, got string) {
				if !strings.Contains(got, "필수 항목 충족") {
					t.Error("Korean pass headline missing")
				}
				if !strings.Contains(got, "All mandatory items satisfied") {
					t.Error("English pass headline missing")
				}
				if !strings.Contains(got, `class="sc-banner sc-ok"`) {
					t.Error("banner is not green")
				}
				if strings.Contains(got, `class="sc-banner sc-bad"`) {
					t.Error("banner must not be red when no mandatory item failed")
				}
				if !strings.Contains(got, `class="sc-badge sc-manual"`) {
					t.Error("manual items must show the manual badge")
				}
			},
		},
		{
			name: "skip never reddens the banner",
			report: secureTestReport(map[string]SecureResult{
				"SF-7.1": {Item: "SF-7.1", Status: SecureSkip, Note: "govulncheck not installed"},
			}),
			check: func(t *testing.T, got string) {
				if !strings.Contains(got, `class="sc-banner sc-ok"`) {
					t.Error("a skipped mandatory item must keep the banner green")
				}
				if !strings.Contains(got, `class="sc-badge sc-skip"`) {
					t.Error("skip badge missing")
				}
				if !strings.Contains(got, "건너뜀") {
					t.Error("Korean skip label missing")
				}
				if !strings.Contains(got, "govulncheck not installed") {
					t.Error("skip note missing")
				}
				if !hasPairAlongside(got, secureNoteKO, secureNoteEN, 200) {
					t.Error("note label is not bilingual")
				}
			},
		},
		{
			name: "escapes hostile data",
			report: func() SecureReport {
				rep := secureTestReport(map[string]SecureResult{
					"SF-3.1": {Item: "SF-3.1", Status: SecureFail, Findings: []SecureFinding{
						{Item: "SF-3.1", Pos: "cry<pt>.go:7", Detail: `<script>alert("pwn")</script>`},
					}},
				})
				rep.ModulePath = `bad"mod<path>`
				return rep
			}(),
			check: func(t *testing.T, got string) {
				if !strings.Contains(got, "&lt;script&gt;alert(&#34;pwn&#34;)&lt;/script&gt;") {
					t.Error("finding detail is not escaped")
				}
				if strings.Contains(got, "<script") {
					t.Error("raw script tag leaked into the document")
				}
				if !strings.Contains(got, "bad&#34;mod&lt;path&gt;") {
					t.Error("module path is not escaped")
				}
				if strings.Contains(got, `bad"mod`) {
					t.Error("raw quote from module path leaked")
				}
				if !strings.Contains(got, "cry&lt;pt&gt;.go:7") {
					t.Error("finding position is not escaped")
				}
			},
		},
		{
			name:   "provenance block carries both languages",
			report: secureTestReport(nil),
			check: func(t *testing.T, got string) {
				// The page escapes the constants; compare against the escaped form.
				koAt := strings.Index(got, html.EscapeString(SecureProvenance))
				enAt := strings.Index(got, html.EscapeString(SecureProvenanceEN))
				if koAt < 0 || enAt < 0 {
					t.Fatal("provenance constants missing from the page")
				}
				if koAt > enAt {
					t.Error("Korean provenance must come first")
				}
				if !strings.Contains(got, secureOfflineKO) || !strings.Contains(got, secureOfflineEN) {
					t.Error("offline note pair missing from the footer")
				}
				if !strings.Contains(got, secureFootProvKO) || !strings.Contains(got, secureFootProvEN) {
					t.Error("short-form provenance pair missing from the footer")
				}
			},
		},
		{
			name:   "document shape and metadata",
			report: secureTestReport(nil),
			check: func(t *testing.T, got string) {
				if !strings.HasPrefix(got, "<!DOCTYPE html>") {
					t.Error("not a complete HTML5 document")
				}
				if !strings.Contains(got, `<html lang="ko">`) {
					t.Error("html lang is not ko")
				}
				if !strings.HasSuffix(strings.TrimSpace(got), "</html>") {
					t.Error("document is not closed")
				}
				if !strings.Contains(got, secureTestTime.Format(time.RFC3339)) {
					t.Error("generated time is not RFC3339")
				}
				if !strings.Contains(got, "example.com/mod") || !strings.Contains(got, "go1.99.0") || !strings.Contains(got, "test-1") {
					t.Error("module path, go version or tool version missing")
				}
				// The manual item shows Korean guidance first, English guidance under it.
				it, _ := SecureItemByID(SecurityChecklist(), "SF-1.2")
				koAt, enAt := strings.Index(got, it.Guidance), strings.Index(got, it.GuidanceEN)
				if koAt < 0 || enAt < 0 || koAt > enAt {
					t.Error("manual guidance must appear Korean first, English second")
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := SecureReportHTML(tc.report)
			tc.check(t, got)
		})
	}
}

// TestSecureReportHTMLBilingualPairs spot-checks that fixed Korean strings are emitted with their
// English pair alongside, Korean first. The pairs come from the summary cards, badges, banner
// note and guidance lines of a default report.
func TestSecureReportHTMLBilingualPairs(t *testing.T) {
	got := SecureReportHTML(secureTestReport(nil))
	pairs := []struct{ ko, en string }{
		{secureCardScannedKO, secureCardScannedEN},
		{secureCardModuleKO, secureCardModuleEN},
		{secureCardGenKO, secureCardGenEN},
		{secureCardToolVerKO, secureCardToolVerEN},
		{secureCardGoVerKO, secureCardGoVerEN},
		{secureCardPassKO, secureCardPassEN},
		{secureCardFailKO, secureCardFailEN},
		{secureCardManualKO, secureCardManualEN},
		{secureCardSkipKO, secureCardSkipEN},
		{secureSevMandKO, secureSevMandEN},
		{secureSevRecKO, secureSevRecEN},
		{secureMethodAutoKO, secureMethodAutoEN},
		{secureMethodToolKO, secureMethodToolEN},
		{secureMethodManualKO, secureMethodManualEN},
		{secureGuideKO, secureGuideEN},
		{secureColReqKO, secureColReqEN},
		{secureColStatusKO, secureColStatusEN},
		{secureColEvKO, secureColEvEN},
		{secureProvHeadKO, secureProvHeadEN},
		{secureTableHeadKO, secureTableHeadEN},
		{secureSummaryHeadKO, secureSummaryHeadEN},
		{secureTitleKO, secureTitleEN},
		{secureBannerNoteKO, secureBannerNoteEN},
		{secureOfflineKO, secureOfflineEN},
	}
	for _, p := range pairs {
		if !strings.Contains(got, p.ko) || !strings.Contains(got, p.en) {
			t.Errorf("pair %q / %q: one side missing", p.ko, p.en)
			continue
		}
		if !hasPairAlongside(got, p.ko, p.en, 200) {
			t.Errorf("pair %q / %q: English pair not alongside its Korean string", p.ko, p.en)
		}
	}
}
