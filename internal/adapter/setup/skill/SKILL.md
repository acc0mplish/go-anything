---
name: go-anything
description: Verify a Go rewrite against its recorded original. Use when porting Go code, checking that a rewrite behaves the same, or deciding whether a rewrite can replace the original.
---

# go-anything

Verify a rewrite by replaying recorded behavior. The original is always the source of truth.

## Workflow

1. **Analyze** the module: `go-anything analyze <root>` (no code runs).
2. **Record** the original on real inputs: `printf 'input\n' | go-anything record -o rec.json -- <original cmd>`.
   Errors the original raises are recorded too, and the rewrite must raise them.
3. **Verify** the rewrite: `go-anything verify rec.json -- <rewrite cmd>`. Exit 0 only if every case matches.
4. **Shadow** before switching traffic: `go-anything shadow rec.json -- <original> -- <rewrite>`.
5. **Bench** only after verify passes: `go-anything bench -runs 5 rec.json -- <original> -- <rewrite>`.

## Rules

- Flags go before positional arguments.
- A passing gate means the recorded cases match, nothing more. Record more real inputs when in doubt.
- Never report a speedup when outputs differ; `bench` refuses to, and so should you.
