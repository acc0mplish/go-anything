<p align="center">
  <a href="https://gitreels.com/acc0mplish/go-anything"><img src="https://gitreels.com/acc0mplish/go-anything.webp" alt="acc0mplish/go-anything in 15 seconds" width="100%"></a>
  <br>
  <a href="https://gitreels.com"><img src="https://gitreels.com/badge.svg" alt="Made with GitReels" height="24"></a>
</p>

# go-anything (English)

[한국어 README](./README.md) · You are reading the English version.

Record what an original program actually does. Verify a Go rewrite against that behavior. Let an agent fix every disagreement until the rewrite converges. Prove security compliance against the Korean government's security function confirmation checklist.

A migration that "looks done" is not done. go-anything makes behavior the contract: the original's recorded answers are the specification, fresh probes keep testing the rewrite, and the loop does not stop while a disagreement exists.

- Version: 0.38.0
- Go: 1.27+
- Module: `github.com/go-anything/go-anything`

## Install

```sh
go install github.com/go-anything/go-anything/cmd/go-anything@latest
```

Or build from a checkout:

```sh
go build -o go-anything ./cmd/go-anything
```

Release binaries for linux, darwin and windows come from `go-anything package`, which also generates the installation document.

## Supported features

| Feature | Status | What it does |
|---|:---:|---|
| `record` / `verify` | ✅ | CLI recording and rewrite judging (`-json-body`, `-float-tolerance`, `-normalize`) |
| `app record` / `app replay` | ✅ | Whole-service recording and replay, `-repeat` instability detection, `-case` single replay. `app record` also takes `-delay-ms` `-retries` `-retry-delay-ms`, which retry transport failures only (refused, dropped, body read); an HTTP status is an answer and is never retried |
| `wire gen` | ✅ | Unit scaffold + `main.go` mount; `-agent` writes the first rewrite from recorded behavior |
| `wire refine` (RSI loop) | ✅ | Probe, ask the original, record, agent fix, convergence verdict. `-delay-ms` `-retries` `-retry-delay-ms` apply to the probes sent to the ORIGINAL side only, retrying connection-level failures only (a connection accepted and then dropped is an original defect). `-concurrency N` (HTTP units, default 0 = off) fires each recorded case N times at once at both sides and judges answer consistency: a rewrite that answers identical concurrent requests differently is a divergence that blocks the round's convergence, while a failed send or a nondeterministic original stays a pending verdict recorded in the round record and the convergence summary. The blocking rule is a go-anything extension (the ported original reports divergence outside the loop). Only identical-request consistency is checked, so interleavings of distinct requests remain unchecked. `-chains N` (HTTP units, default 0 = off) walks each recorded collection after the probes agree: it creates a resource named with the reserved `chain-rN-…` name, then reads, mutates and deletes that one resource, comparing the two sides step by step — a rewrite that only replays the recording collapses on these new requests. A chain touches its own reserved-name resources only; on a target whose recording never observed DELETE the created resource stays behind on the original; an unjudgeable chain (a failed send, or a 5xx or transcript surprise from the original) blocks the round's convergence. `-serve-original '<command>'` (HTTP units, off by default) starts a fresh copy of the original with this shell command at the top of every round and stops it when the round ends — state the original accumulated in earlier rounds never colors a round's verdicts, and the two sides stay symmetric (the rewrite restarts every round already). `{{port}}` in the command is replaced by the port the tool picks (a missing placeholder logs a warning); readiness is any HTTP status answer (default 60 s, tunable with `-original-start-timeout`). The original inherits the WHOLE parent environment on purpose — it is the operator's own program, so the environment allowlist applied to the agent-generated rewrite does not apply to it — and runs in this tool's working directory. A failed start reports only the last 2000 bytes of the process output (the full inheritance can put configuration values and secrets into that output). It cannot be combined with `-original-url` and is Unix only, because the command runs through sh. With the option on, the original's state resets every round, so authorization-check and recorded-case comparison results can differ from the default run (state-dependent mismatches disappear), and chain-check `chain-rN-…` resources stay behind on that round's original when DELETE was never observed in the recording |
| `wire verify` / `set` / `status` | ✅ | Per-unit verification, unit switching, defect policy query |
| Live application probing | ✅ | Through the assembled `main.go`; process ownership verified, foreign listeners refused, one request builder for both sides |
| CLI probe isolation | ✅ | One child process per probe with a timeout; `os.Exit` cannot kill the harness; every exit shape becomes that probe's answer |
| Child environment allowlist | ✅ | The agent-generated rewrite sees only `GA_*` and basics, never parent tokens; cleanup kills the process group |
| `secure` + `gate -security` | ✅ | 39-item confirmation checklist, all 29 official clauses referenced, opt-in gate |
| `rbac` | ✅ | Structural authorization audit with 401/403/404 deny reasons |
| `analyze` | ✅ | Port-order analysis for Go, Python, JS/TS, C and Java originals |
| `shadow gen` / `frontdoor scaffold` / `profile -url` | ✅ | Drop-in shadow package, proxy entry point, live pprof |
| HTML reports (bilingual, responsive) | ✅ | Verification, analysis delta, RSI rounds, security checklist (Korean-first) |
| `pace` / `fresh` | ⏸ | Deferred with documented trigger conditions |
| `pin_clock` runtime patching | ❌ | Deliberate non-goal; masking and `-repeat` instead |

## The three flows

### 1. Record, then verify a rewrite

```sh
go-anything record -o rec.json -- python3 orders.py   # one case per stdin line
go-anything verify rec.json -- ./orders-rewrite       # exits non-zero on any mismatch
```

`verify -report out.html` writes an HTML report with the verdict, per-case diffs and next steps. Compare options: `-json-body` (JSON semantic compare), `-float-tolerance`, `-normalize timestamps -normalize uuids` or `name=regex` (mask volatile values on both sides).

### 2. Migrate a whole service with the refine loop

```sh
go-anything app record -cases cases.json -o app.json -base http://127.0.0.1:8080
go-anything wire gen ./svc orders /orders/ -cases app.json
go-anything wire refine ./svc orders /orders/ -cases app.json \
    -original-url http://127.0.0.1:8080 \
    -agent "claude -p --permission-mode acceptEdits" -rounds 4
```

Each round sends fresh probe inputs (including request-shape variants like `%31` and lowercase methods) to both sides. Every disagreement is recorded with the original's answer, the unit's test is regenerated, and the agent fixes the rewrite in a clean room: it sees only recorded behavior, never the original's source. Convergence requires two consecutive clean rounds on different seeds. Original crashes are separated into a defect list instead of being recorded as behavior.

For HTTP units the probes go through the assembled application: the module is built, `main.go` is started with the unit switched to the rewrite, and probes are sent over the network, so routing, middleware and authentication are part of what convergence checks. The started process is owned and watched, so there is no port-precheck race.

A round ledger lives in `.go-anything/checks/<unit>/` (JSON per round plus an HTML page), and states what was not checked, honestly.

### 3. Security function confirmation (Korean government scheme)

```sh
go-anything secure -root .            # checklist.json + security.html, exit 1 on mandatory failures
go-anything secure -root . -report-only
go-anything gate -root . -security    # opt-in: the checklist joins the release gate
```

39 checklist items map every clause of the official server common security requirements V3.0 R1 (국가용 보안요구사항 1·2편, 2023-08-21) onto a Go module; each item's `ref` column points at the official clause number. Source-checkable items (hardcoded credentials, weak crypto, `math/rand`, `InsecureSkipVerify`, SQL and shell assembly, cookie flags, debug endpoints) are checked automatically. Deployment-level clauses (lockout, admin IP allowlists, key lifecycle, audit record protection) are listed as manual items with bilingual guidance. `govulncheck` and `go vet` run when installed and skip with a note when not.

The security report is Korean-first with English second, because it is a government compliance artifact. Every other report is English-first with Korean second. The provenance block states plainly that the checklist does not replace the official text.

Optionality is a rule: `gate` runs the security checks only with `-security`, external tools never fail when absent, and nothing here is exposed through MCP.

## Structural authorization check

```sh
go-anything rbac -cases cases.json -principals principals.json -policy policy.json -base http://127.0.0.1:8081
```

Sends every case as every principal and audits allow or deny outcomes against the declared policy. B2B tenant leaks are exactly what this catches. Tokens are read from environment variables named in the principal file and never printed. Any critical finding exits non-zero. `wire refine -rbac-principals ... -rbac-policy ...` adds the same audit to the loop's convergence rule.

## What else is in the box

- `analyze` ports-order analysis for Go (go/types call graph) and for Python, JS/TS, C and Java originals: waves, risk sites with `file:line`, `start_here`, and the Go package each module should land in.
- `shadow gen` builds a drop-in shadow package from a Go package's signatures; `GA_SHADOW_<PKG>=old|shadow|new` switches implementations per call and logs mismatches.
- `frontdoor scaffold` writes a standard-library reverse proxy entry point that a wire unit mounts into.
- `profile -url` replays app cases against a live service while sampling `/debug/pprof/profile`.
- `fuzz`, `bench`, `map`, `lift`, `coverage`, `doctor`, `setup`, `package`, `commands`.

Run `go-anything` with no arguments for the full usage, or `go-anything commands` for the command table and which ones an agent can reach through MCP.

## Compared with rust-anything

go-anything is a Go-native rethinking of the same job, not a port. The tool surface is comparable (record, verify, analyze, bench, fuzz, shadow, map, package, gate, app record/replay, frontdoor, profile, from-tests). The differences:

- The refine loop with an agent in the loop, round ledgers and convergence rules is go-anything's core.
- Structural RBAC checking and the Korean security function confirmation checklist do not exist in rust-anything.
- rust-anything pins the clock inside the original's runtime (napi and Python monkey-patching). go-anything does not patch foreign runtimes; it detects instability at record time (`-repeat`), masks volatile values at compare time (`-normalize`), and reports what it could not check.
- Compare options are equivalent: `ignore_key_order` → `-json-body`, `float_tolerance` → `-float-tolerance`, `case` → `app replay -case`, `normalize` → `-normalize` (built-ins: `timestamps`, `uuids`, `host_urls`, `counts`, `tokens`, `session_ids`, `oauth`, plus `name=regex`; every built-in mask keeps a JSON answer valid, and only `counts` masks a number, as a quoted string).

## Project layout

```
cmd/go-anything/       CLI wiring and command registry
internal/domain/       cases, comparison, reports, checklist model (pure)
internal/usecase/      recording, verification, refine rules, rewrite generation
internal/adapter/      app runner, wire gen, analyze (go/py/js/c/java), secure scan, mcp
docs/                  STATUS, HARDENING, RSI_PLAN, DECISIONS
```

The comparison code a generated unit test uses is copied from `internal/domain/compare.go` itself, so the tool and the rewrite's own `go test` judge answers by the same rules.

## Development

```sh
gofmt -l .
go vet ./...
go test -race -count=1 ./...
```

All three are expected clean. Status and history of the build live in `docs/STATUS.md` (Korean), the design rules in `docs/HARDENING.md` and `docs/DECISIONS.md`, the loop plan in `docs/RSI_PLAN.md`.

## 라이선스

Modified MIT + Commons Clause + 미디어 제한. 전문은 [`LICENSE`](./LICENSE)에 있다. 판매(SaaS·호스팅·화이트라벨 포함)는 금지되고, 저작권자의 사전 서면 동의 없이 이 소프트웨어로 상업적 공개 미디어 콘텐츠를 만들 수 없다. OSI 정의의 오픈소스가 아니라 소스 공개(source-available)이다.

## 한국어 요약

go-anything은 원본 프로그램의 동작을 기록하고, Go 재작성이 그 동작과 일치하는지 검증하며, 불일치를 에이전트가 고치는 루프를 일치 완료시킬 때까지 돌리는 마이그레이션 도구입니다.

- `record`와 `verify`로 시작하고, `wire refine`이 점검 요청, 기록, 에이전트 수정, 일치 완료 판정을 라운드마다 반복합니다. 일치 완료는 서로 다른 시드에서 연속 두 라운드가 불일치 0일 때만 선언합니다.
- `secure`는 국가정보원 국가용 보안요구사항(서버 공통 V3.0 R1)의 조항 29개 전부를 Go 모듈 검사 항목으로 연결한 확인서 체크리스트입니다. 자동 검사와 사람 확인 항목이 이중언어로 구분되고, 게이트는 옵션입니다.
- `rbac`은 요청마다 주체를 바꿔 보내 허용과 거부를 정책과 대비하는 구조적 권한 검사입니다. B2B 테넌트 누수가 이 검사의 존재 이유입니다.
- 보고서는 HTML로 나오고, 보안 확인서 리포트만 한국어 우선, 나머지는 영어 우선 한국어 차선입니다.
- 원본 소스는 절대 에이전트에게 보여주지 않습니다. 기록된 동작만이 재작성의 사양입니다.

## License

**Modified MIT + Commons Clause + Media Restrictions** (full text: [`LICENSE`](./LICENSE)). MIT grant plus the following constraints:

1. **Commons Clause** - no Selling the Software (providing it to third parties for a fee or consideration, including SaaS, hosting, white-label and rebrand). This is source-available, not open source under the OSI definition.
2. **Media Restriction** - without the Copyright Holder's prior written consent, the Software's source code, UI and derivatives may not be used to create public media content (broadcasts, demos, reviews or analysis on YouTube, Twitch, blogs, especially where commercial monetization or self-promotion misrepresents ownership).
3. Violation terminates all rights immediately, enforced via DMCA Takedown and platform copyright procedures.

Copyright (c) 2026 acc0mplish
