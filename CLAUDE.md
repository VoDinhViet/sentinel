# Sentinel

One Go binary (`cmd/sentinel`) that runs on each Linux server (restic backups of files and databases, uptime checks,
alerts). It is a CLI plus a daemon (`sentinel run`) driven by a YAML config; there is no web UI and no central server. The daemon can serve a small JSON HTTP API (`internal/api`, `net/http`, bearer token, off unless `api.listen` is set).

- **Done means `make test` is green.** `make e2e` and `make e2e-engines` need docker.
- No AI/LLM features in the product: status and alerts are rule-based (thresholds, schedules).
- README and runbooks are Vietnamese, and so are the command and flag descriptions in `--help` and the screens of `sentinel manage` and `sentinel init` (technical terms such as job, backup, snapshot, restore, repository, retention and schedule stay in English); log lines, restic/engine errors, code, comments and commit messages are English.
- Layout follows go.dev/doc/modules/layout: `cmd/sentinel` holds thin cobra commands, `internal/` packages are flat and split by responsibility, and `internal/app` is the service the CLI, the manager and the API all call. In `internal/tui` each screen opened over the tabs (help, delete, snapshots, restore, busy, form, folder browser) is a `screen` in `screen.go`, kept in a stack on the model: push to open, pop to close. Prefer the standard library over a new dependency.
- Path-scoped rules live in `.claude/rules/` (Go code, deploy/ops).
