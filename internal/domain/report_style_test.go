package domain

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestClipForReportKeepsShortValues(t *testing.T) {
	if got := ClipForReport("hello", 10); got != "hello" {
		t.Fatalf("a short value must be unchanged, got %q", got)
	}
}

func TestClipForReportCutsOnARuneBoundaryAndSaysHowMuchIsHidden(t *testing.T) {
	s := strings.Repeat("가", 10) // 30 bytes
	got := ClipForReport(s, 7)
	if !utf8.ValidString(got) {
		t.Fatalf("clipped value must stay valid UTF-8: %q", got)
	}
	if !strings.HasPrefix(got, "가가 [") || !strings.Contains(got, "24 more bytes not shown") {
		t.Fatalf("clip must keep whole runes and count the hidden bytes, got %q", got)
	}
}

// koreanFor returns the Korean half of the Bi pair whose English half is exactly en, or "".
func koreanFor(s, en string) string {
	prefix := `<span class="bi-en">` + en + `</span><span class="bi-ko" lang="ko">`
	i := strings.Index(s, prefix)
	if i < 0 {
		return ""
	}
	rest := s[i+len(prefix):]
	if j := strings.Index(rest, "</span>"); j >= 0 {
		return rest[:j]
	}
	return ""
}

func TestBiRendersEnglishFirstThenKorean(t *testing.T) {
	got := Bi("Hello", "안녕하세요")
	want := `<span class="bi-en">Hello</span><span class="bi-ko" lang="ko">안녕하세요</span>`
	if got != want {
		t.Fatalf("Bi = %q, want %q", got, want)
	}
	if strings.Index(got, "Hello") > strings.Index(got, "안녕하세요") {
		t.Fatal("the English text must come before the Korean text")
	}
}

func TestBiEscapesBothHalves(t *testing.T) {
	got := Bi("<b>x</b>", "<i>y</i>")
	if strings.Contains(got, "<b>") || strings.Contains(got, "<i>") {
		t.Fatalf("Bi must escape both halves, got %q", got)
	}
}

func TestReportCSSPutsKoreanOnItsOwnMutedLine(t *testing.T) {
	for _, want := range []string{
		".bi-en { display: inline; }",
		".bi-ko { display: block;",
		"color: var(--muted)",
	} {
		if !strings.Contains(ReportCSS, want) {
			t.Errorf("ReportCSS must style the bilingual pair, missing %q", want)
		}
	}
}

func bilingualPassReport() Report {
	r := Report{Subject: "svc", Kind: "unit", Total: 3, Matched: 3, DriftChecked: true,
		Items: []ReportItem{{Name: "a", Passed: true}, {Name: "b", Passed: true}, {Name: "c", Passed: true}}}
	r.Finalize()
	return r
}

func bilingualFailReport() Report {
	r := Report{Subject: "svc", Kind: "unit", Total: 3, Matched: 1, DriftChecked: true,
		Items: []ReportItem{{Name: "a", Passed: true}, {Name: "b"}, {Name: "c"}},
		Diffs: []ReportDiff{{Case: "b", What: "output", Input: "4", Want: "40", Got: "41"}}}
	r.Finalize()
	return r
}

// The verdict banner must carry both languages, on the pass and the fail side.
func TestVerificationReportBannerIsBilingual(t *testing.T) {
	tests := []struct {
		name   string
		r      Report
		en, ko string
	}{
		{"pass", bilingualPassReport(), "Verdict: PASS", "판정: 통과"},
		{"fail", bilingualFailReport(), "Verdict: FAIL", "판정: 실패"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			b, err := tc.r.HTML()
			if err != nil {
				t.Fatal(err)
			}
			s := string(b)
			if !strings.Contains(s, tc.en) {
				t.Fatalf("the English verdict must stay primary, missing %q", tc.en)
			}
			if got := koreanFor(s, tc.en); got != tc.ko {
				t.Fatalf("verdict Korean = %q, want %q", got, tc.ko)
			}
		})
	}
}

func TestVerificationReportFooterIsBilingual(t *testing.T) {
	b, err := bilingualPassReport().HTML()
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	if !strings.Contains(s, "Share this report with care") || !strings.Contains(s, "나눌 때 주의하세요") {
		t.Fatal("the footer privacy note must appear in English and in Korean")
	}
}

// Only the leading verb of a step is bilingual; the rest of the sentence keeps its data as it is.
func TestVerificationReportNextStepVerbsAreBilingual(t *testing.T) {
	b, err := bilingualFailReport().HTML()
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	if got := koreanFor(s, "Fix"); got != "수정" {
		t.Fatalf("leading verb Fix must carry its Korean mirror, got %q", got)
	}
	if !strings.Contains(s, "mismatching case(s)") {
		t.Fatal("the body of the step after the verb must stay as NextSteps wrote it")
	}
}

func bilingualAnalysisReport(t *testing.T) AnalysisReport {
	t.Helper()
	a := Analysis{Module: "m", Packages: []Package{
		{ImportPath: "m/a", Tests: 1, Lines: 5, Functions: []FuncInfo{{Name: "F"}},
			RiskSites: []RiskSite{{Kind: RiskGoroutine, Pos: "a.go:2", Detail: "go worker()"}}},
	}}
	return NewAnalysisReport(a, 2).WithPrevious(Analysis{Module: "m"}, 1)
}

func TestAnalysisReportCardsAndHeadingsAreBilingual(t *testing.T) {
	r := bilingualAnalysisReport(t)
	b, err := r.HTML()
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	tests := []struct{ name, en, ko string }{
		{"card title", "Lines", "라인 수"},
		{"start here card", "Start here", "여기서 시작"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if !strings.Contains(s, tc.en) {
				t.Fatalf("English card title missing: %q", tc.en)
			}
			if got := koreanFor(s, tc.en); got != tc.ko {
				t.Fatalf("card title %q Korean = %q, want %q", tc.en, got, tc.ko)
			}
		})
	}
	// The Changes since vN heading keeps its bare English line; the Korean mirror sits under it.
	if !strings.Contains(s, "<h2 id=\"changes\">Changes since v1</h2>") || !strings.Contains(s, "v1 이후 변경 사항") {
		t.Fatal("the Changes since vN heading must show English and its Korean mirror")
	}
	// A risk-kind explanation carries both languages.
	if !strings.Contains(s, RiskWhy(RiskGoroutine)) || !strings.Contains(s, riskWhyKorean[RiskGoroutine]) {
		t.Fatal("risk-kind explanations must appear in English and in Korean")
	}
	if !strings.Contains(s, "How to read this") || !strings.Contains(s, "읽는 법") {
		t.Fatal("the How to read this summary must be bilingual")
	}
}

// Every risk kind that has an English explanation must have its Korean mirror, in both wordings.
func TestRiskWhyKoreanCoversEveryKind(t *testing.T) {
	for k, en := range riskWhy {
		if riskWhyKorean[k] == "" {
			t.Errorf("kind %q explains %q but has no Korean mirror", k, en)
		}
	}
	for k := range originalRiskWhy {
		if originalRiskWhyKorean[k] == "" {
			t.Errorf("non-Go kind %q has no Korean mirror", k)
		}
	}
}

// TestBilingualEvidenceLines renders one report per surface with realistic data and logs one
// English line and its Korean pair, as evidence for the bilingual requirement.
func TestBilingualEvidenceLines(t *testing.T) {
	b, err := bilingualFailReport().HTML()
	if err != nil {
		t.Fatal(err)
	}
	v := string(b)
	a := string(func() string {
		b, err := bilingualAnalysisReport(t).HTML()
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}())
	t.Logf("verification EN line: %s", "Verdict: FAIL")
	t.Logf("verification KO line: %s", koreanFor(v, "Verdict: FAIL"))
	footerEn := fmt.Sprintf("Generated by go-anything. Values are shown as plain text, clipped at %d bytes, and may contain personal data. Share this report with care.", ReportValueLimit)
	t.Logf("verification footer EN line: %s", "Share this report with care")
	t.Logf("verification footer KO line: %s", koreanFor(v, footerEn))
	t.Logf("analysis card EN line: %s", "Lines")
	t.Logf("analysis card KO line: %s", koreanFor(a, "Lines"))
}
