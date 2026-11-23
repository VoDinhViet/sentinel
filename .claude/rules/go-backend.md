---
paths:
  - "cmd/**"
  - "internal/**"
---

# Go backend rules (Sentinel)

- **No CGO.** The binary is built with `CGO_ENABLED=0`; SQLite is `modernc.org/sqlite` (driver name `"sqlite"`). Do not add
  dependencies that need CGO or a C toolchain.
- **External tools** (restic, rclone, dump tools): `exec.CommandContext(ctx, bin, args...)` with an argv slice. Never build a
  shell string or run `sh -c` in production code. Every call gets a `context` so it can be cancelled and timed out.
- **Secrets** never go in argv, logs, alert text, API responses or error messages. Pass passwords as env vars or via
  `writeSecretFile` in `internal/engine/engine.go` (0600 temp file, removed by the caller). Config values use `${VAR}`.
- **Errors**: wrap with `fmt.Errorf("doing x: %w", err)`; do not drop errors from restic/rclone/dump, retention or `restic check`.
  A failed backup step must reach the report and the alerting path, not only a log line.
- **Logging**: `log/slog` with key/value attributes (`slog.Error("backup failed", "job", name, "err", err)`).
- **SQLite migrations** (`internal/store/store.go`) are an append-only list versioned by `PRAGMA user_version`: add a new
  entry at the end, never edit or reorder an existing one. Queries use `?` parameters.
- **Tests** sit next to the code (`*_test.go`), standard `testing` package (no testify), table-driven with `t.Run` where there
  are several cases. Fake dump tools in tests may use `sh -c` (see `internal/backup/backup_test.go`).
- **Done means**: `go vet ./...` and `go test ./...` pass; behaviour that touches restic, engines or the CLI is also covered by `make e2e` / `make e2e-engines` when docker is available.
