# Decisions

- **Language:** Go, latest stable toolchain (`go 1.27.4`, from `go.dev/VERSION`, SHA256 verified).
- **Architecture:** Clean Architecture. Dependencies point inward only:
  `cmd` -> `adapter` / `harness` -> `usecase` -> `domain`. `domain` imports only the standard library.
- **Ports:** `usecase` declares `RecordingStore`, `Subject`, `Analyzer`; adapters implement them. Composition root: `cmd/go-anything`.
- **Verification loop:** record -> verify (replay every case) -> gate. Concept from rust-anything; no Rust code ported.
- **Errors are behavior:** an input the original fails on is recorded as an error case; the rewrite must fail too.
- **Newline drift:** only trailing `\r\n` is normalized.
- **Exec without a shell:** subjects run via `exec` with argv; inputs are never shell-interpreted.
- **Timeouts:** every subject run has a timeout (default 30s).
- **Atomic saves:** recordings are written to a temp file and renamed.
- **Timing requires equal output:** `bench` refuses to report a valid speedup when outputs differ.
- **Shadow returns the original:** production callers never receive the rewrite's answer during a shadow run.
- **Setup is dry-run by default:** nothing is written without `-apply`; existing config is merged, backed up, and invalid files are never overwritten.
- **Agent presence is detected by its own directory**, not by a settings file alone.
