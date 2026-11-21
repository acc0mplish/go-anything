# go-anything

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

Release binaries for linux, darwin and windows come from `go-anything package` (see `docs/INSTALL.md`).

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
- Compare options are equivalent: `ignore_key_order` → `-json-body`, `float_tolerance` → `-float-tolerance`, `case` → `app replay -case`, `normalize` → `-normalize`.

## Project layout

```
cmd/go-anything/       CLI wiring and command registry
internal/domain/       cases, comparison, reports, checklist model (pure)
internal/usecase/      recording, verification, refine rules, rewrite generation
internal/adapter/      app runner, wire gen, analyze (go/py/js/c/java), secure scan, mcp
docs/                  STATUS, HARDENING, RSI_PLAN, DECISIONS, INSTALL
```

The comparison code a generated unit test uses is copied from `internal/domain/compare.go` itself, so the tool and the rewrite's own `go test` judge answers by the same rules.

## Development

```sh
gofmt -l .
go vet ./...
go test -race -count=1 ./...
```

All three are expected clean. Status and history of the build live in `docs/STATUS.md` (Korean), the design rules in `docs/HARDENING.md` and `docs/DECISIONS.md`, the loop plan in `docs/RSI_PLAN.md`.

## 한국어 요약

go-anything은 원본 프로그램의 동작을 기록하고, Go 재작성이 그 동작과 일치하는지 검증하며, 불일치를 에이전트가 고치는 루프를 수렴시킬 때까지 돌리는 마이그레이션 도구입니다.

- `record`와 `verify`로 시작하고, `wire refine`이 프로브, 기록, 에이전트 수정, 수렴 판정을 라운드마다 반복합니다. 수렴은 서로 다른 시드에서 연속 두 라운드 깨끗할 때만 선언합니다.
- `secure`는 국가정보원 국가용 보안요구사항(서버 공통 V3.0 R1)의 조항 29개 전부를 Go 모듈 검사 항목으로 연결한 확인서 체크리스트입니다. 자동 검사와 사람 확인 항목이 이중언어로 구분되고, 게이트는 옵션입니다.
- `rbac`은 요청마다 주체를 바꿔 보내 허용과 거부를 정책과 대비하는 구조적 권한 검사입니다. B2B 테넌트 누수가 이 검사의 존재 이유입니다.
- 보고서는 HTML로 나오고, 보안 확인서 리포트만 한국어 우선, 나머지는 영어 우선 한국어 차선입니다.
- 원본 소스는 절대 에이전트에게 보여주지 않습니다. 기록된 동작만이 재작성의 사양입니다.

## License

To be decided by the maintainer before the first public push.
