package domain

import (
	"fmt"
	"html"
	"strings"
	"time"
)

// Fixed bilingual strings of the secure checklist report. Korean is always the primary line and
// English mirrors it right underneath; data (IDs, paths, findings) is never translated.
const (
	secureTitleKO = "보안기능 확인 체크리스트"
	secureTitleEN = "Security function confirmation checklist"

	secureProvHeadKO = "출처 및 범위"
	secureProvHeadEN = "Provenance and scope"

	secureBannerFailKO = "필수 항목 미충족"
	secureBannerFailEN = "Mandatory items failed"
	secureBannerPassKO = "필수 항목 충족"
	secureBannerPassEN = "All mandatory items satisfied"
	secureBannerNoteKO = "건너뜀(skip)과 사람 확인(manual) 결과는 이 배너를 빨간색으로 만들지 않는다."
	secureBannerNoteEN = "Skip and manual results never turn this banner red."

	secureSummaryHeadKO = "요약"
	secureSummaryHeadEN = "Summary"

	secureCardPassKO    = "통과"
	secureCardPassEN    = "pass"
	secureCardFailKO    = "미충족"
	secureCardFailEN    = "fail"
	secureCardManualKO  = "사람 확인"
	secureCardManualEN  = "manual"
	secureCardSkipKO    = "건너뜀"
	secureCardSkipEN    = "skip"
	secureCardScannedKO = "스캔 파일"
	secureCardScannedEN = "scanned files"
	secureCardModuleKO  = "모듈 경로"
	secureCardModuleEN  = "module path"
	secureCardGoVerKO   = "Go 버전"
	secureCardGoVerEN   = "Go version"
	secureCardToolVerKO = "도구 버전"
	secureCardToolVerEN = "tool version"
	secureCardGenKO     = "생성 시각"
	secureCardGenEN     = "generated"
	secureRootLabelKO   = "대상 경로"
	secureRootLabelEN   = "root"

	secureTableHeadKO = "체크리스트 결과"
	secureTableHeadEN = "Checklist results"
	secureColReqKO    = "확인 사항"
	secureColReqEN    = "requirement"
	secureColMethodKO = "방법"
	secureColMethodEN = "method"
	secureColSevKO    = "등급"
	secureColSevEN    = "severity"
	secureColStatusKO = "결과"
	secureColStatusEN = "status"
	secureColEvKO     = "근거"
	secureColEvEN     = "findings"

	secureMethodAutoKO   = "자동"
	secureMethodAutoEN   = "auto"
	secureMethodToolKO   = "도구"
	secureMethodToolEN   = "tool"
	secureMethodManualKO = "수동"
	secureMethodManualEN = "manual"

	secureSevMandKO = "필수"
	secureSevMandEN = "mandatory"
	secureSevRecKO  = "권고"
	secureSevRecEN  = "recommended"

	secureGuideKO = "확인 안내"
	secureGuideEN = "guidance"
	secureNoteKO  = "비고"
	secureNoteEN  = "note"
	secureNoResKO = "결과 없음"
	secureNoResEN = "no result"

	// Short-form provenance for the footer; the full pair sits near the top of the page.
	secureFootProvKO = "요약: 이 체크리스트는 공식 원문을 대체하지 않으며, manual 항목은 공식 문서와 대비하여 사람이 확인해야 한다."
	secureFootProvEN = "In short: this checklist does not replace the official text; manual items must be verified by a person against it."
	secureOfflineKO  = "오프라인 산출물: 이 문서의 값은 어디로도 전송되지 않는다."
	secureOfflineEN  = "Offline artifact: values in this document are not transmitted anywhere."
)

// secureCSS is the stylesheet of this page. It is defined here, in this file, and uses only its
// own sc-* classes: the report is a standalone artifact and shares no stylesheet with the other
// go-anything reports. The look echoes them (dark-on-light cards, monospace evidence).
const secureCSS = `
  :root { --sc-fg:#1d1d1f; --sc-muted:#57606a; --sc-line:#d0d7de; --sc-head:#f6f8fa; --sc-ok:#1a7f37;
          --sc-bad:#cf222e; --sc-warn:#9a6700; --sc-info:#0969da; --sc-dim:#6e7781; --sc-okbg:#dafbe1;
          --sc-badbg:#ffebe9; --sc-warnbg:#fff8c5; --sc-infobg:#ddf4ff; --sc-graybg:#eaeef2; }
  * { box-sizing: border-box; }
  body { font-family: system-ui, sans-serif; margin: 2rem auto; max-width: 1080px; color: var(--sc-fg);
         padding: 0 1rem; line-height: 1.45; background: #fff; }
  h1 { font-size: 1.5rem; margin: 0 0 .25rem; }
  h2 { font-size: 1.15rem; margin-top: 2rem; border-bottom: 1px solid var(--sc-line); padding-bottom: .25rem; }
  .sc-en-block { display: block; color: var(--sc-muted); font-size: .7em; font-weight: 500; margin-top: .15rem; }
  .sc-ken { color: var(--sc-muted); font-size: .85em; }
  .sc-meta { color: var(--sc-muted); font-size: .85rem; margin: 0 0 1rem; }
  .sc-meta code { font-family: ui-monospace, SFMono-Regular, Menlo, monospace; font-size: .8rem; }
  .sc-prov { border: 1px solid var(--sc-line); border-left: 4px solid var(--sc-dim); border-radius: 8px;
             padding: .6rem 1rem; margin: .5rem 0 1rem; }
  .sc-prov h2 { margin: 0 0 .4rem; border: 0; font-size: 1rem; padding: 0; }
  .sc-prov-ko { margin: .2rem 0; }
  .sc-prov-en { margin: .2rem 0; color: var(--sc-muted); font-size: .9em; }
  .sc-banner { border-radius: 8px; padding: .8rem 1rem; margin: .75rem 0 1rem; border: 1px solid var(--sc-line); }
  .sc-banner.sc-ok { background: var(--sc-okbg); border-color: var(--sc-ok); }
  .sc-banner.sc-bad { background: var(--sc-badbg); border-color: var(--sc-bad); }
  .sc-verdict { margin: 0; }
  .sc-verdict strong { font-size: 1.15rem; }
  .sc-note-line { margin: .3rem 0 0; font-size: .88rem; }
  .sc-cards { display: grid; grid-template-columns: repeat(auto-fit, minmax(150px, 1fr)); gap: .6rem;
              margin: .75rem 0 1rem; }
  .sc-card { border: 1px solid var(--sc-line); border-radius: 8px; padding: .55rem .75rem; min-width: 0; }
  .sc-card .sc-k { color: var(--sc-muted); font-size: .78rem; }
  .sc-card .sc-v { font-size: 1.35rem; font-weight: 700; font-variant-numeric: tabular-nums;
                   word-break: break-all; }
  .sc-card .sc-val { font-size: .9rem; font-weight: 600; font-family: ui-monospace, SFMono-Regular, Menlo, monospace; }
  .sc-card.sc-ok-t .sc-v { color: var(--sc-ok); }
  .sc-card.sc-bad-t .sc-v { color: var(--sc-bad); }
  .sc-card.sc-warn-t .sc-v { color: var(--sc-warn); }
  .sc-card.sc-dim-t .sc-v { color: var(--sc-dim); }
  .sc-mono { font-family: ui-monospace, SFMono-Regular, Menlo, monospace; font-size: .82rem; word-break: break-all; }
  table.sc-table { border-collapse: collapse; width: 100%; margin: .5rem 0 1.25rem; }
  .sc-table th, .sc-table td { border: 1px solid var(--sc-line); padding: .35rem .55rem; text-align: left;
                               vertical-align: top; }
  .sc-table th { background: var(--sc-head); font-size: .85rem; white-space: nowrap; }
  .sc-table .sc-ref {\n\tcolor: #6b7280;\n\tfont-size: 11px;\n}\n.sc-id { white-space: nowrap; }
  .sc-cat td { background: var(--sc-head); font-weight: 700; }
  .sc-cat .sc-ken { margin-left: .35rem; font-weight: 400; }
  .sc-req-ko { font-weight: 600; }
  .sc-req-en { color: var(--sc-muted); font-size: .85em; margin-top: .15rem; }
  .sc-guide-ko { margin-top: .35rem; font-size: .88rem; }
  .sc-guide-ko .sc-k { color: var(--sc-muted); }
  .sc-guide-en { color: var(--sc-muted); font-size: .82em; margin-top: .1rem; }
  .sc-badge { display: inline-block; font-size: .72rem; padding: .05rem .45rem; border-radius: 999px;
              border: 1px solid var(--sc-line); white-space: nowrap; }
  .sc-badge .sc-ken { margin-left: .25rem; }
  .sc-badge.sc-pass { background: var(--sc-okbg); color: var(--sc-ok); border-color: var(--sc-ok); }
  .sc-badge.sc-fail { background: var(--sc-badbg); color: var(--sc-bad); border-color: var(--sc-bad); }
  .sc-badge.sc-manual { background: var(--sc-warnbg); color: var(--sc-warn); border-color: var(--sc-warn); }
  .sc-badge.sc-skip { background: var(--sc-graybg); color: var(--sc-muted); border-color: var(--sc-muted); }
  .sc-badge.sc-plain { background: var(--sc-head); color: var(--sc-muted); }
  .sc-badge.sc-mand { background: var(--sc-badbg); color: var(--sc-bad); border-color: var(--sc-bad); }
  .sc-badge.sc-rec { background: var(--sc-infobg); color: var(--sc-info); border-color: var(--sc-info); }
  .sc-fpos { font-family: ui-monospace, SFMono-Regular, Menlo, monospace; font-size: .8rem;
             word-break: break-all; }
  .sc-fdet { font-family: ui-monospace, SFMono-Regular, Menlo, monospace; font-size: .8rem;
             word-break: break-all; color: var(--sc-muted); margin-bottom: .25rem; }
  .sc-ev .sc-k { color: var(--sc-muted); font-size: .78rem; }
  .sc-row-bad td { background: #fff7f6; }
  footer { color: var(--sc-dim); font-size: .8rem; margin-top: 2.5rem; border-top: 1px solid var(--sc-line);
           padding-top: .5rem; }
  footer p { margin: .25rem 0; }
  @media (max-width: 720px) {
    body { margin: 1rem auto; padding: 0 .75rem; font-size: .95rem; }
    .sc-cards { grid-template-columns: repeat(2, 1fr); gap: .5rem; }
    table.sc-table { display: block; overflow-x: auto; -webkit-overflow-scrolling: touch; }
    th, td { font-size: .82rem; padding: .3rem .45rem; }
  }
  @media print { body { margin: 0 auto; } }
`

// secureEsc escapes one dynamic string for HTML text content. Every value that comes from report
// data (paths, requirements, guidance, findings, notes, versions) goes through it, so evidence
// text is always shown as text and never becomes markup.
func secureEsc(s string) string { return html.EscapeString(s) }

// SecureReportHTML renders one checklist run as a complete standalone HTML5 document: no scripts,
// no external assets, one inline stylesheet. The page is a bilingual compliance surface for the
// Korean security function confirmation checklist, so every fixed string appears in Korean first
// with its English pair underneath, and only mandatory failures turn the banner red.
func SecureReportHTML(r SecureReport) string {
	counts := r.CountsPerStatus()
	red := len(r.MandatoryFailures()) > 0

	resultByItem := make(map[string]SecureResult, len(r.Results))
	for _, res := range r.Results {
		resultByItem[res.Item] = res
	}

	var b strings.Builder
	b.WriteString("<!DOCTYPE html>\n<html lang=\"ko\">\n<head>\n")
	b.WriteString("<meta charset=\"utf-8\">\n")
	b.WriteString("<meta name=\"viewport\" content=\"width=device-width, initial-scale=1\">\n")
	fmt.Fprintf(&b, "<title>%s · %s</title>\n", secureEsc(secureTitleKO), secureEsc(secureTitleEN))
	fmt.Fprintf(&b, "<style>%s</style>\n</head>\n<body>\n", secureCSS)

	// Heading and module root.
	fmt.Fprintf(&b, "<h1>%s<span class=\"sc-en-block\">%s</span></h1>\n",
		secureEsc(secureTitleKO), secureEsc(secureTitleEN))
	fmt.Fprintf(&b, "<p class=\"sc-meta\">%s <span class=\"sc-ken\">%s</span>: <code>%s</code></p>\n",
		secureEsc(secureRootLabelKO), secureEsc(secureRootLabelEN), secureEsc(r.Root))

	// Provenance block: the Korean statement first, the English statement under it.
	fmt.Fprintf(&b, "<section class=\"sc-prov\">\n<h2>%s<span class=\"sc-en-block\">%s</span></h2>\n",
		secureEsc(secureProvHeadKO), secureEsc(secureProvHeadEN))
	fmt.Fprintf(&b, "<p class=\"sc-prov-ko\">%s</p>\n<p class=\"sc-prov-en\">%s</p>\n</section>\n",
		secureEsc(SecureProvenance), secureEsc(SecureProvenanceEN))

	// Banner. Only mandatory failures redden it; the note says so in both languages.
	ko, en := secureBannerPassKO, secureBannerPassEN
	cls := "sc-ok"
	if red {
		ko, en, cls = secureBannerFailKO, secureBannerFailEN, "sc-bad"
	}
	fmt.Fprintf(&b, "<div class=\"sc-banner %s\">\n<p class=\"sc-verdict\"><strong>%s</strong>"+
		"<span class=\"sc-en-block\">%s</span></p>\n", cls, secureEsc(ko), secureEsc(en))
	fmt.Fprintf(&b, "<p class=\"sc-note-line\">%s</p>\n<p class=\"sc-note-line\">%s</p>\n</div>\n",
		secureEsc(secureBannerNoteKO), secureEsc(secureBannerNoteEN))

	// Summary cards: status counts first, then the run facts.
	fmt.Fprintf(&b, "<h2>%s<span class=\"sc-en-block\">%s</span></h2>\n<div class=\"sc-cards\">\n",
		secureEsc(secureSummaryHeadKO), secureEsc(secureSummaryHeadEN))
	secureCountCard(&b, secureCardPassKO, secureCardPassEN, counts[SecurePass], "sc-ok-t")
	secureCountCard(&b, secureCardFailKO, secureCardFailEN, counts[SecureFail], "sc-bad-t")
	secureCountCard(&b, secureCardManualKO, secureCardManualEN, counts[SecureStatusManual], "sc-warn-t")
	secureCountCard(&b, secureCardSkipKO, secureCardSkipEN, counts[SecureSkip], "sc-dim-t")
	secureCountCard(&b, secureCardScannedKO, secureCardScannedEN, r.ScannedFiles, "")
	secureTextCard(&b, secureCardModuleKO, secureCardModuleEN, r.ModulePath)
	secureTextCard(&b, secureCardGoVerKO, secureCardGoVerEN, r.GoVersion)
	secureTextCard(&b, secureCardToolVerKO, secureCardToolVerEN, r.ToolVersion)
	secureTextCard(&b, secureCardGenKO, secureCardGenEN, r.GeneratedAt.Format(time.RFC3339))
	b.WriteString("</div>\n")

	// Checklist table grouped by category, in checklist display order.
	fmt.Fprintf(&b, "<h2>%s<span class=\"sc-en-block\">%s</span></h2>\n",
		secureEsc(secureTableHeadKO), secureEsc(secureTableHeadEN))
	b.WriteString("<table class=\"sc-table\">\n<thead><tr>\n<th>ID</th>\n")
	fmt.Fprintf(&b, "<th>%s <span class=\"sc-ken\">%s</span></th>\n", secureEsc(secureColReqKO), secureEsc(secureColReqEN))
	fmt.Fprintf(&b, "<th>%s <span class=\"sc-ken\">%s</span></th>\n", secureEsc(secureColMethodKO), secureEsc(secureColMethodEN))
	fmt.Fprintf(&b, "<th>%s <span class=\"sc-ken\">%s</span></th>\n", secureEsc(secureColSevKO), secureEsc(secureColSevEN))
	fmt.Fprintf(&b, "<th>%s <span class=\"sc-ken\">%s</span></th>\n", secureEsc(secureColStatusKO), secureEsc(secureColStatusEN))
	fmt.Fprintf(&b, "<th>%s <span class=\"sc-ken\">%s</span></th>\n</tr>\n</thead>\n<tbody>\n",
		secureEsc(secureColEvKO), secureEsc(secureColEvEN))

	prevCategory := ""
	for _, it := range r.Items {
		if it.Category != prevCategory {
			prevCategory = it.Category
			fmt.Fprintf(&b, "<tr class=\"sc-cat\"><td colspan=\"6\">%s <span class=\"sc-ken\">%s</span></td></tr>\n",
				secureEsc(it.Category), secureEsc(it.CategoryEN))
		}
		res, hasResult := resultByItem[it.ID]

		rowCls := ""
		if hasResult && res.Status == SecureFail {
			rowCls = " class=\"sc-row-bad\""
		}
		idCell := it.ID
		if it.Ref != "" {
			idCell += "<br><span class=\"sc-ref\">" + secureEsc(it.Ref) + "</span>"
		}
		fmt.Fprintf(&b, "<tr%s>\n<td class=\"sc-mono sc-id\">%s</td>\n", rowCls, idCell)

		// Requirement: Korean primary, English muted underneath; manual items add guidance.
		b.WriteString("<td>\n")
		fmt.Fprintf(&b, "<div class=\"sc-req-ko\">%s</div>\n<div class=\"sc-req-en\">%s</div>\n",
			secureEsc(it.Requirement), secureEsc(it.RequirementEN))
		if it.Method == SecureManual && (it.Guidance != "" || it.GuidanceEN != "") {
			fmt.Fprintf(&b, "<div class=\"sc-guide-ko\"><span class=\"sc-k\">%s <span class=\"sc-ken\">%s</span></span> %s</div>\n",
				secureEsc(secureGuideKO), secureEsc(secureGuideEN), secureEsc(it.Guidance))
			fmt.Fprintf(&b, "<div class=\"sc-guide-en\">%s</div>\n", secureEsc(it.GuidanceEN))
		}
		b.WriteString("</td>\n")

		b.WriteString("<td>\n")
		switch it.Method {
		case SecureAuto:
			secureBadge(&b, secureMethodAutoKO, secureMethodAutoEN, "sc-plain")
		case SecureTool:
			secureBadge(&b, secureMethodToolKO, secureMethodToolEN, "sc-plain")
		case SecureManual:
			secureBadge(&b, secureMethodManualKO, secureMethodManualEN, "sc-plain")
		}
		b.WriteString("\n</td>\n<td>\n")
		if it.Severity == SecureMandatory {
			secureBadge(&b, secureSevMandKO, secureSevMandEN, "sc-mand")
		} else {
			secureBadge(&b, secureSevRecKO, secureSevRecEN, "sc-rec")
		}
		b.WriteString("\n</td>\n<td>\n")
		if !hasResult {
			secureBadge(&b, secureNoResKO, secureNoResEN, "sc-plain")
		} else {
			ko, en, cls := secureStatusStrings(res.Status)
			secureBadge(&b, ko, en, cls)
		}
		b.WriteString("\n</td>\n")

		// Evidence: findings as file:line plus detail lines, monospace; the note last.
		b.WriteString("<td class=\"sc-ev\">\n")
		if hasResult {
			for _, f := range res.Findings {
				fmt.Fprintf(&b, "<div class=\"sc-fpos\">%s</div>\n", secureEsc(f.Pos))
				if f.Detail != "" {
					fmt.Fprintf(&b, "<div class=\"sc-fdet\">%s</div>\n", secureEsc(f.Detail))
				}
			}
			if res.Note != "" {
				fmt.Fprintf(&b, "<div class=\"sc-note-line\"><span class=\"sc-k\">%s <span class=\"sc-ken\">%s</span></span> %s</div>\n",
					secureEsc(secureNoteKO), secureEsc(secureNoteEN), secureEsc(res.Note))
			}
		}
		b.WriteString("</td>\n</tr>\n")
	}
	b.WriteString("</tbody>\n</table>\n")

	// Footer: the provenance pair in short form and the offline note.
	fmt.Fprintf(&b, "<footer>\n<p>%s</p>\n<p>%s</p>\n", secureEsc(secureFootProvKO), secureEsc(secureFootProvEN))
	fmt.Fprintf(&b, "<p>%s</p>\n<p>%s</p>\n</footer>\n", secureEsc(secureOfflineKO), secureEsc(secureOfflineEN))
	b.WriteString("</body>\n</html>\n")
	return b.String()
}

// secureStatusStrings maps a status to its Korean label, English pair and badge class.
func secureStatusStrings(s SecureStatus) (ko, en, cls string) {
	switch s {
	case SecurePass:
		return secureCardPassKO, secureCardPassEN, "sc-pass"
	case SecureFail:
		return secureCardFailKO, secureCardFailEN, "sc-fail"
	case SecureStatusManual:
		return secureCardManualKO, secureCardManualEN, "sc-manual"
	case SecureSkip:
		return secureCardSkipKO, secureCardSkipEN, "sc-skip"
	}
	return secureNoResKO, secureNoResEN, "sc-plain"
}

// secureCountCard writes one numeric summary card: a bilingual label and a count.
func secureCountCard(b *strings.Builder, ko, en string, n int, tone string) {
	cls := ""
	if tone != "" {
		cls = " " + tone
	}
	fmt.Fprintf(b, "<div class=\"sc-card%s\"><div class=\"sc-k\">%s <span class=\"sc-ken\">%s</span></div>"+
		"<div class=\"sc-v\">%d</div></div>\n", cls, secureEsc(ko), secureEsc(en), n)
}

// secureTextCard writes one text summary card: a bilingual label and an escaped value.
func secureTextCard(b *strings.Builder, ko, en, val string) {
	fmt.Fprintf(b, "<div class=\"sc-card\"><div class=\"sc-k\">%s <span class=\"sc-ken\">%s</span></div>"+
		"<div class=\"sc-v sc-val\">%s</div></div>\n", secureEsc(ko), secureEsc(en), secureEsc(val))
}

// secureBadge writes one pill with a Korean label and its English pair inside the same pill.
func secureBadge(b *strings.Builder, ko, en, cls string) {
	fmt.Fprintf(b, "<span class=\"sc-badge %s\">%s <span class=\"sc-ken\">%s</span></span>",
		cls, secureEsc(ko), secureEsc(en))
}
