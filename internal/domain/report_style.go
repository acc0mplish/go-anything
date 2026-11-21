package domain

import "html/template"

// ReportCSS is the one stylesheet every go-anything HTML report embeds, so the reports look and read
// the same. Pages stay self-contained: no scripts, no external assets. Collapsing uses <details>.
const ReportCSS = `
  :root { --fg:#1d1d1f; --muted:#57606a; --line:#d0d7de; --head:#f6f8fa; --ok:#1a7f37; --bad:#cf222e;
          --warn:#9a6700; --info:#0969da; --okbg:#dafbe1; --badbg:#ffebe9; --warnbg:#fff8c5; --infobg:#ddf4ff; }
  * { box-sizing: border-box; }
  body { font-family: system-ui, sans-serif; margin: 2rem auto; max-width: 1080px; color: var(--fg); padding: 0 1rem; line-height: 1.45; }
  h1 { font-size: 1.5rem; word-break: break-all; margin-bottom: .25rem; }
  h2 { font-size: 1.15rem; margin-top: 2rem; border-bottom: 1px solid var(--line); padding-bottom: .25rem; }
  .meta { color: var(--muted); font-size: .85rem; margin: 0 0 1rem; }
  .meta code { font-size: .8rem; }
  nav.toc { display: flex; flex-wrap: wrap; gap: .4rem; font-size: .85rem; margin: .5rem 0 1rem; }
  nav.toc a { padding: .25rem .6rem; border: 1px solid #d1d5db; border-radius: 999px; color: var(--info); text-decoration: none; background: #fff; }
  nav.toc a:hover { border-color: var(--info); }
  .banner { border-radius: 8px; padding: .8rem 1rem; margin: .75rem 0 1rem; border: 1px solid var(--line); }
  .banner.pass { background: var(--okbg); border-color: var(--ok); }
  .banner.fail { background: var(--badbg); border-color: var(--bad); }
  .banner.warn { background: var(--warnbg); border-color: var(--warn); }
  .banner.info { background: var(--infobg); border-color: var(--info); }
  .banner strong.verdict { font-size: 1.1rem; }
  .banner p { margin: .35rem 0 0; }
  .cards { display: grid; grid-template-columns: repeat(auto-fit, minmax(150px, 1fr)); gap: .6rem; margin: .75rem 0 1rem; }
  .card { border: 1px solid var(--line); border-radius: 8px; padding: .55rem .75rem; }
  .card .k { color: var(--muted); font-size: .75rem; text-transform: uppercase; letter-spacing: .03em; }
  .card .v { font-size: 1.35rem; font-weight: 700; font-variant-numeric: tabular-nums; }
  .card .s { color: var(--muted); font-size: .8rem; }
  .card.bad .v { color: var(--bad); } .card.ok .v { color: var(--ok); } .card.warn .v { color: var(--warn); }
  table { border-collapse: collapse; width: 100%; margin: .5rem 0 1.25rem; }
  th, td { border: 1px solid var(--line); padding: .35rem .55rem; text-align: left; vertical-align: top; }
  th { background: var(--head); font-size: .85rem; }
  td.num, th.num { text-align: right; font-variant-numeric: tabular-nums; white-space: nowrap; }
  .mono, td.path, td.val { font-family: ui-monospace, SFMono-Regular, Menlo, monospace; font-size: .82rem; word-break: break-all; }
  pre.val { font-family: ui-monospace, SFMono-Regular, Menlo, monospace; font-size: .82rem; margin: 0; white-space: pre-wrap; word-break: break-all; }
  mark.diff { background: #ffd8d3; color: inherit; border-bottom: 2px solid var(--bad); }
  .ws { color: #8c959f; }
  .badge { display: inline-block; font-size: .72rem; padding: .05rem .45rem; border-radius: 999px; border: 1px solid var(--line); white-space: nowrap; }
  .badge.ok { background: var(--okbg); color: var(--ok); border-color: var(--ok); }
  .badge.bad { background: var(--badbg); color: var(--bad); border-color: var(--bad); }
  .badge.warn { background: var(--warnbg); color: var(--warn); border-color: var(--warn); }
  .badge.info { background: var(--infobg); color: var(--info); border-color: var(--info); }
  .bar { background: #eaeef2; border-radius: 4px; height: .55rem; min-width: 80px; overflow: hidden; }
  .bar > span { display: block; height: 100%; background: var(--ok); }
  .bar.mid > span { background: var(--warn); } .bar.low > span { background: var(--bad); }
  .note { color: var(--muted); font-size: .88rem; }
  /* Bilingual fixed prose: the English text stays inline and primary; the Korean line mirrors it
     underneath, muted, and never replaces it. */
  .bi-en { display: inline; }
  .bi-ko { display: block; color: var(--muted); font-size: .85em; line-height: 1.3; margin-top: .1rem; }
  .bi-ko-inl { display: inline; color: var(--muted); font-size: .85em; margin-left: .35em; }
  ol.actions li { margin: .25rem 0; }
  details { margin: .5rem 0 1rem; } details > summary { cursor: pointer; color: var(--info); }
  footer { color: #6e7781; font-size: .8rem; margin-top: 2.5rem; border-top: 1px solid var(--line); padding-top: .5rem; }
  code, .path, td.path, .mono { overflow-wrap: anywhere; }
  pre.val { overflow-x: auto; }
  @media (max-width: 720px) {
    body { margin: 1rem auto; padding: 0 .75rem; font-size: .95rem; }
    h1 { font-size: 1.2rem; }
    h2 { font-size: 1.05rem; margin-top: 1.5rem; }
    .cards { grid-template-columns: repeat(2, 1fr); gap: .5rem; }
    .card .v { font-size: 1.1rem; }
    table { display: block; overflow-x: auto; -webkit-overflow-scrolling: touch; }
    th, td { font-size: .82rem; padding: .3rem .45rem; }
    nav.toc { gap: .3rem; font-size: .8rem; }
    nav.toc a { padding: .2rem .5rem; }
    .banner { padding: .6rem .7rem; }
  }
  @media print { details { display: block; } details > summary { display: none; } nav.toc { display: none; } }
`

// ReportValueLimit is the most bytes of one recorded value a report shows. Recorded values can hold
// personal data, and a report is shared more widely than the recording, so it shows only enough to
// see the difference.
const ReportValueLimit = 2048

// Bi renders one fixed report string in both languages: the English text first, then the Korean
// text as a second line under it. English is the primary text of every report; Korean only repeats
// it, so a reader who lacks either language still gets the other. Recorded data (paths, numbers,
// case names, JSON, diffs, command lines, seeds) never goes through Bi; only fixed prose does.
// Both halves are escaped, so even a caller that mistakenly wraps data cannot inject markup.
// The result is a plain string so Go-side callers and tests can match the English inside it;
// templates insert it through biHTML, which marks it safe for html/template.
// BiInline is the compact pair for one-line contexts: tabs, card labels, table headers. The
// Korean follows on the same line, smaller and muted, so a bilingual label is one item, not two.
func BiInline(en, ko string) string {
	return `<span class="bi-en">` + template.HTMLEscapeString(en) + `</span><span class="bi-ko-inl" lang="ko">` + template.HTMLEscapeString(ko) + `</span>`
}

func Bi(en, ko string) string {
	return `<span class="bi-en">` + template.HTMLEscapeString(en) + `</span><span class="bi-ko" lang="ko">` + template.HTMLEscapeString(ko) + `</span>`
}

// biHTML is Bi as an html/template function, so templates can mark fixed prose bilingual without
// the default contextual escaping showing the spans as text.
func biHTML(en, ko string) template.HTML { return template.HTML(Bi(en, ko)) }

func biInlineHTML(en, ko string) template.HTML { return template.HTML(BiInline(en, ko)) }
