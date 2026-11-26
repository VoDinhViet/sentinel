# Sentinel

One Go binary (`cmd/sentinel`) that runs on each Linux server (restic backups of files and databases, uptime checks,
alerts). It is a CLI plus a daemon (`sentinel run`) driven by a YAML config; there is no web UI and no central server.

- **Done means `make test` is green.** `make e2e` and `make e2e-engines` need docker.
- No AI/LLM features in the product: status and alerts are rule-based (thresholds, schedules).
- README and runbooks are Vietnamese, and so are the command and flag descriptions in `--help` and the screens of `sentinel manage` and `sentinel init` (technical terms such as job, backup, snapshot, restore, repository, retention and schedule stay in English); log lines, restic/engine errors, code, comments and commit messages are English.
- Path-scoped rules live in `.claude/rules/` (Go code, deploy/ops).
