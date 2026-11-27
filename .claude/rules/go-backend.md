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
- **TLS between our own ends** (agent ↔ control plane, `sentinelctl` ↔ control plane) is TLS 1.3. `InsecureSkipVerify` appears only
  inside `pki.PinnedTLSConfig`, together with its own `VerifyConnection`. Clients never follow redirects.
- **Credentials of the control plane**: the admin password is hashed with PBKDF2-HMAC-SHA256 (`crypto/pbkdf2`, 600 000 rounds,
  stored with its parameters) and compared with `subtle.ConstantTimeCompare`; sessions and join tokens are stored as SHA-256 and
  shown once. Agent identity comes from the verified client certificate, never from a request field.
- **Text from another machine** (reports, task output, names) goes through `termtext.Clean` before it is shown or put in an alert,
  and task output through `restic.Redact`. Bodies are capped with `http.MaxBytesReader`.
- **Errors**: wrap with `fmt.Errorf("doing x: %w", err)`; do not drop errors from restic/rclone/dump, retention or `restic check`.
  A failed backup step must reach the report and the alerting path, not only a log line.
- **Logging**: `log/slog` with key/value attributes (`slog.Error("backup failed", "job", name, "err", err)`).
- **SQLite migrations** (`internal/store/store.go`) are an append-only list versioned by `PRAGMA user_version`: add a new
  entry at the end, never edit or reorder an existing one. Queries use `?` parameters. The control plane's database
  (`server/internal/store/migrations/NNNN_*.sql`, embedded) follows the same rule.
- **Tests** sit next to the code (`*_test.go`), standard `testing` package (no testify), table-driven with `t.Run` where there
  are several cases. Tests that depend on time (timeouts, expiry, long-poll, watchdog) use `testing/synctest`, not `time.Sleep`
  or a hand-made clock. Fake dump tools in tests may use `sh -c` (see `internal/backup/backup_test.go`).
- **Done means**: `go vet ./...` and `go test ./...` pass; behaviour that touches restic, engines or the CLI is also covered by `make e2e` / `make e2e-engines` when docker is available.
