---
name: go-anything
description: Verify a Go rewrite against its recorded original, run the migration loop until it converges, audit authorization outcomes, and check the Korean security function confirmation checklist. Use when porting Go code, migrating a Python or Node service to Go, deciding whether a rewrite can replace the original, or preparing a Korean government security confirmation.
---

# go-anything

The original's recorded behavior is the specification. A rewrite is done when the tool says so, not when it looks done.

## Workflow A: command-line program

1. Analyze first: `go-anything analyze <root>` (no code runs).
2. Record the original on real inputs: `printf '7\n' | go-anything record -o rec.json -- <original cmd>`.
3. Verify the rewrite: `go-anything verify rec.json -- <rewrite cmd>`. Exit 0 only when every case matches.
4. Shadow before switching: `go-anything shadow rec.json -- <original> -- <rewrite>`.
5. Bench only after verify passes: `go-anything bench -runs 5 rec.json -- <original> -- <rewrite>`.

## Workflow B: service migration (the refine loop)

1. Record the service: `go-anything app record -cases cases.json -o app.json -base <original url>`.
2. Scaffold one unit: `go-anything wire gen <root> <unit> /prefix/ -cases app.json`.
3. Run the loop: `go-anything wire refine <root> <unit> /prefix/ -cases app.json -original-url <url> -agent "<agent cmd>"`.
   Each round probes both sides with fresh inputs (percent-encoded variants, lowercase methods, boundary values), records every disagreement with the original's answer, regenerates the unit test, and asks the agent to fix the rewrite.
4. Convergence is two consecutive clean rounds on different seeds. One clean round is NOT convergence.
5. Read the ledger at `<root>/.go-anything/checks/<unit>/` (JSON per round plus an HTML page). The "not checked" list is part of the verdict; never claim more than it says.

## Workflow C: security function confirmation (Korean government scheme)

- `go-anything secure -root <dir>` maps all 29 clauses of the server common security requirements V3.0 R1 onto the module; `checklist.json` and `security.html` land under `<root>/.go-anything/security/`.
- Manual items are for a person; do not mark them done yourself.
- The gate integration is opt-in (`gate -security`); never present it as part of the default gate.

## Workflow D: authorization audit

- `go-anything rbac -cases cases.json -principals p.json -policy pol.json -base <url>` sends every case as every principal and judges allow/deny outcomes only.
- Tokens are environment variable NAMES in the principal file. Never write token values into files, commands, or reports. Run this only when the operator has set the variables.

## Rules

- Flags go before positional arguments.
- The agent writing a rewrite works clean-room: recorded behavior only, never the original's source.
- The original is the source of truth by default. An original crash is a defect (reported, not recorded) unless the operator chose the `expected` policy.
- Never report a speedup when outputs differ; `bench` refuses to, and so should you.
- Compare options belong on the tool and the loop together: `-json-body`, `-float-tolerance`, `-normalize timestamps -normalize uuids` or `name=regex`.
- A passing verify means the recorded cases match. When in doubt, record more inputs or run another refine round on a fresh seed.
