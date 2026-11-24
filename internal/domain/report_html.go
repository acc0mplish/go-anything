package domain

import (
	"bytes"
	"fmt"
	"html/template"
	"strings"
)

// HTML renders the report as one self-contained page: no scripts, no external assets.
//
// The page puts the decision first: verdict, reasons and next steps, then the counts, then the
// mismatches with their input, and only then the matched cases, collapsed.
//
// html/template escapes every value, so recorded responses, file names and error text are shown
// as text even when they contain markup. That matters here: a recorded body is untrusted input.
// The only template.HTML values are built by markValue, which escapes every recorded segment first.
func (r Report) HTML() ([]byte, error) {
	var buf bytes.Buffer
	if err := reportTemplate.Execute(&buf, newHTMLView(r)); err != nil {
		return nil, fmt.Errorf("report: html: %w", err)
	}
	return buf.Bytes(), nil
}

type htmlView struct {
	Report
	Verdict       template.HTML
	VerdictCls    string
	Headline      template.HTML
	Steps         []template.HTML
	MatchRate     string
	MatchCls      string
	MismatchCases int
	StaleText     template.HTML
	StaleShort    template.HTML
	StaleCls      string
	CoverageTxt   string
	ByField       []FieldCount
	Rows          []htmlDiffRow
	MatchedItems  []ReportItem
	OtherFailed   []ReportItem
	Limit         int
}

type htmlDiffRow struct {
	Case  string
	What  string
	Input template.HTML
	Want  template.HTML
	Got   template.HTML
}

func newHTMLView(r Report) htmlView {
	v := htmlView{Report: r, VerdictCls: "pass", Limit: ReportValueLimit, MatchCls: "ok"}
	v.MismatchCases = r.Total - r.Matched
	switch {
	case r.Passed:
		v.Verdict = biHTML("Verdict: PASS", "판정: 통과")
		v.Headline = biHTML(
			fmt.Sprintf("All %d recorded case(s) matched. The rewrite is acceptable for what was recorded.", r.Total),
			fmt.Sprintf("기록된 %d개 케이스가 모두 일치했습니다. 이 재작성은 기록된 범위 안에서 사용할 수 있습니다.", r.Total))
	default:
		v.Verdict, v.VerdictCls, v.MatchCls = biHTML("Verdict: FAIL", "판정: 실패"), "fail", "bad"
		v.Headline = biHTML("The rewrite is not acceptable yet.", "이 재작성은 아직 사용할 수 없습니다.")
	}
	for _, s := range r.NextSteps() {
		v.Steps = append(v.Steps, biStep(s))
	}
	v.MatchRate = fmt.Sprintf("%.1f%%", r.MatchRate())
	switch {
	case r.Stale:
		v.StaleShort, v.StaleText, v.StaleCls = biHTML("yes", "예"),
			biHTML("the source changed since the recording", "기록 이후 소스가 변경되었습니다"), "bad"
	case r.DriftChecked:
		v.StaleShort, v.StaleText, v.StaleCls = biHTML("no", "아니요"),
			biHTML("the source still matches the recording", "소스가 기록과 여전히 일치합니다"), "ok"
	case r.Kind == "unit":
		v.StaleShort, v.StaleText, v.StaleCls = biHTML("not checked", "확인 안 함"),
			biHTML("not checked (no source directory given)", "확인 안 함(소스 디렉터리를 지정하지 않음)"), "warn"
	default:
		v.StaleShort, v.StaleText, v.StaleCls = biHTML("not checked", "확인 안 함"),
			biHTML("app replay does not check the source", "앱 재실행은 소스를 확인하지 않습니다"), "warn"
	}
	if r.Coverage != nil {
		v.CoverageTxt = fmt.Sprintf("%.1f%%", *r.Coverage)
	}
	v.ByField = r.FailuresByField()
	hasDiff := map[string]bool{}
	for _, d := range r.Diffs {
		hasDiff[d.Case] = true
		want, got := markDiff(d.Want, d.Got)
		v.Rows = append(v.Rows, htmlDiffRow{
			Case:  ClipForReport(d.Case, ReportValueLimit),
			What:  d.What,
			Input: markValue(ClipForReport(d.Input, ReportValueLimit), 0, 0),
			Want:  want,
			Got:   got,
		})
	}
	for _, it := range r.Items {
		switch {
		case it.Passed:
			v.MatchedItems = append(v.MatchedItems, it)
		case !hasDiff[it.Name]:
			// A failure with no diff row: its Detail is the only explanation, so keep it.
			v.OtherFailed = append(v.OtherFailed, it)
		}
	}
	return v
}

// stepVerbs maps the leading verb of every next-step sentence to the Korean line that mirrors it.
// The verb is the fixed head of a step; the rest of the sentence carries data (counts, fields,
// flags) and stays exactly as NextSteps wrote it.
var stepVerbs = map[string]string{
	"Record":    "기록",
	"Re-record": "다시 기록",
	"Fix":       "수정",
	"Run":       "실행",
	"No":        "조치 필요 없음",
}

// biStep renders one next step with its leading verb in both languages, English first. A step
// whose leading verb is not in the table is shown as it is, escaped.
func biStep(step string) template.HTML {
	word, rest, _ := strings.Cut(step, " ")
	ko, ok := stepVerbs[word]
	if !ok {
		return template.HTML(template.HTMLEscapeString(step))
	}
	return template.HTML(Bi(word, ko) + template.HTMLEscapeString(" "+rest))
}

// glyph is one rune as it is shown: whitespace that would be invisible gets a visible symbol.
type glyph struct {
	text string // what to show, plain text
	ws   bool   // a whitespace symbol, shown grey
	nl   bool   // a newline: the symbol is followed by a real line break in HTML
}

// visibleRunes maps every rune of s to what a report shows for it: a newline as ↵, a carriage
// return as ␍, a tab as →, and a space at the end of a line as ·. Other runes are shown as they are.
func visibleRunes(s string) []glyph {
	rs := []rune(s)
	out := make([]glyph, len(rs))
	trailing := true // scanning backwards: true while only spaces follow up to a line end
	for i := len(rs) - 1; i >= 0; i-- {
		switch rs[i] {
		case '\n':
			out[i] = glyph{text: "↵", ws: true, nl: true}
			trailing = true
		case '\r':
			out[i] = glyph{text: "␍", ws: true}
			trailing = true
		case '\t':
			out[i] = glyph{text: "→", ws: true}
		case ' ':
			if trailing {
				out[i] = glyph{text: "·", ws: true}
			} else {
				out[i] = glyph{text: " "}
			}
		default:
			out[i] = glyph{text: string(rs[i])}
			trailing = false
		}
	}
	return out
}

// markDiff renders expected and actual with visible whitespace and marks where they differ: the
// common prefix and common suffix stay plain, the differing middle of each side is marked.
// Both values are clipped to the report limit first.
func markDiff(want, got string) (template.HTML, template.HTML) {
	want, got = ClipForReport(want, ReportValueLimit), ClipForReport(got, ReportValueLimit)
	w, g := []rune(want), []rune(got)
	p := 0
	for p < len(w) && p < len(g) && w[p] == g[p] {
		p++
	}
	q := 0
	for q < len(w)-p && q < len(g)-p && w[len(w)-1-q] == g[len(g)-1-q] {
		q++
	}
	return markValue(want, p, len(w)-q), markValue(got, p, len(g)-q)
}

// markValue renders s as HTML with visible whitespace and wraps runes [from, to) in
// <mark class="diff">. Every piece of s is escaped with template.HTMLEscapeString before it is
// joined, so recorded markup can never reach the page as markup.
func markValue(s string, from, to int) template.HTML {
	var b strings.Builder
	for i, gl := range visibleRunes(s) {
		if i == from && from < to {
			b.WriteString(`<mark class="diff">`)
		}
		text := template.HTMLEscapeString(gl.text)
		if gl.ws {
			b.WriteString(`<span class="ws">` + text + `</span>`)
		} else {
			b.WriteString(text)
		}
		if gl.nl {
			b.WriteString("\n")
		}
		if i == to-1 && from < to {
			b.WriteString(`</mark>`)
		}
	}
	return template.HTML(b.String()) // #nosec G203 -- every segment above is escaped
}

var reportTemplate = template.Must(template.New("report").Funcs(template.FuncMap{"bi": biHTML}).Parse(`<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Verification report: {{.Subject}}</title>
<style>` + ReportCSS + `
  table.diffs { table-layout: fixed; }
  table.diffs col.c-case { width: 12%; } table.diffs col.c-field { width: 14%; }
  table.diffs col.c-input { width: 20%; } table.diffs col.c-val { width: 27%; }
  table.diffs td.field { word-break: normal; overflow-wrap: anywhere; }
</style>
</head>
<body>
<h1>Verification report</h1>
{{/* The English title line stays bare; the Korean line mirrors it underneath. */}}
{{bi "" "검증 보고서"}}
<p class="meta"><span class="mono">{{.Subject}}</span> · {{.Kind}}</p>
{{- with .Run}}
<p class="meta">
  {{- if .Rewrite}}Rewrite <code>{{.Rewrite}}</code> · {{end}}
  {{- if .Recording}}Recording <code>{{.Recording}}</code> · {{end}}
  {{- if .GeneratedAt}}Generated {{.GeneratedAt}} · {{end}}
  {{- if .Compare}}Compare {{.Compare}} · {{end}}
  {{- if .ToolVersion}}go-anything {{.ToolVersion}}{{end -}}
</p>
{{- end}}

<div class="banner {{.VerdictCls}}">
  <strong class="verdict {{.VerdictCls}}">Verdict: {{.Verdict}}</strong> <span>{{.Headline}}</span>
  {{- if .Reasons}}
  <p>{{bi "Why:" "이유:"}}</p>
  <ul class="reasons">
  {{- range .Reasons}}<li>{{.}}</li>{{end}}
  </ul>
  {{- end}}
  <p>{{bi "Next steps:" "다음 단계:"}}</p>
  <ol class="actions">
  {{- range .Steps}}<li>{{.}}</li>{{end}}
  </ol>
</div>

<div class="cards">
  <div class="card {{.MatchCls}}"><div class="k">{{bi "Matched" "일치"}}</div><div class="v">{{.Matched}}/{{.Total}}</div><div class="s">{{.Matched}} of {{.Total}} ({{.MatchRate}})</div></div>
  <div class="card {{if .MismatchCases}}bad{{else}}ok{{end}}"><div class="k">{{bi "Mismatching cases" "불일치 케이스"}}</div><div class="v">{{.MismatchCases}}</div><div class="s">{{len .Diffs}} differing field(s)</div></div>
  <div class="card {{.StaleCls}}"><div class="k">{{bi "Stale recording" "오래된 기록"}}</div><div class="v">{{.StaleShort}}</div><div class="s">{{.StaleText}}</div></div>
  {{- if .CoverageTxt}}
  <div class="card"><div class="k">{{bi "Original statement coverage" "원본 문장 커버리지"}}</div><div class="v">{{.CoverageTxt}}</div><div class="s">{{bi "of the original's statements reached by the cases" "케이스가 도달한 원본 문장의 비율"}}</div></div>
  {{- end}}
</div>

{{- if .ByField}}
<h2 id="by-field">{{bi "Failures by field" "필드별 실패"}}</h2>
<table style="width:auto">
  <tr><th>{{bi "Field" "필드"}}</th><th class="num">{{bi "Count" "건수"}}</th></tr>
  {{- range .ByField}}
  <tr><td class="val">{{.Field}}</td><td class="num">{{.Count}}</td></tr>
  {{- end}}
</table>
{{- end}}

{{- if .Rows}}
<h2 id="mismatches">Mismatches ({{len .Rows}})</h2>
{{bi "" (printf "불일치 (%d)" (len .Rows))}}
<p class="note">Send the input to the rewrite to reproduce a row. The marked part is where expected and actual first differ up to where they agree again. ↵ is a newline, → a tab, · a trailing space.</p>
<table class="diffs">
  <colgroup><col class="c-case"><col class="c-field"><col class="c-input"><col class="c-val"><col class="c-val"></colgroup>
  <tr><th>{{bi "Case" "케이스"}}</th><th>{{bi "Field" "필드"}}</th><th>{{bi "Input" "입력"}}</th><th>{{bi "Expected" "기대값"}}</th><th>{{bi "Actual" "실제값"}}</th></tr>
  {{- range .Rows}}
  <tr>
    <td class="val">{{.Case}}</td>
    <td class="val field">{{.What}}</td>
    <td><pre class="val">{{.Input}}</pre></td>
    <td><pre class="val">{{.Want}}</pre></td>
    <td><pre class="val">{{.Got}}</pre></td>
  </tr>
  {{- end}}
</table>
{{- end}}

{{- if .OtherFailed}}
<h2 id="other-failures">Other failed items ({{len .OtherFailed}})</h2>
{{bi "" (printf "기타 실패 항목 (%d개)" (len .OtherFailed))}}
<table>
  <tr><th>{{bi "Item" "항목"}}</th><th>{{bi "Detail" "상세"}}</th></tr>
  {{- range .OtherFailed}}
  <tr><td class="val">{{.Name}}</td><td><pre class="val">{{.Detail}}</pre></td></tr>
  {{- end}}
</table>
{{- end}}

<h2 id="matched">{{bi "Matched" "일치"}}</h2>
{{- if .MatchedItems}}
<details>
<summary>{{bi (printf "%d matched cases" (len .MatchedItems)) (printf "일치한 케이스 %d개" (len .MatchedItems))}}</summary>
<table>
  <tr><th>{{bi "Case" "케이스"}}</th><th>{{bi "Detail" "상세"}}</th></tr>
  {{- range .MatchedItems}}
  <tr><td class="val">{{.Name}}</td><td>{{if .Detail}}{{.Detail}}{{else}}match{{end}}</td></tr>
  {{- end}}
</table>
</details>
{{- else}}
<p class="note">{{bi "No case matched." "일치한 케이스가 없습니다."}}</p>
{{- end}}

<footer>{{bi (printf "Generated by go-anything. Values are shown as plain text, clipped at %d bytes, and may contain personal data. Share this report with care." .Limit) (printf "go-anything가 생성했습니다. 값은 일반 텍스트로 표시되며 %d바이트에서 잘리고, 개인 데이터를 담을 수 있습니다. 이 보고서를 나눌 때 주의하세요." .Limit)}}</footer>
</body>
</html>
`))
