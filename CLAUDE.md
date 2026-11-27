# Sentinel

The agent is one Go binary (`cmd/sentinel`) that runs on each Linux server (restic backups of files and databases, uptime checks,
alerts). It is a CLI plus a daemon (`sentinel run`) driven by a YAML config. There is no web UI.

**Direction (RFC 0002, in progress):** an optional control plane, `sentinel-server`, that agents dial out to over mTLS. The
repo becomes three Go modules joined by `go.work`: `pkg/` (shared: `agentapi`, `pki`, `alert`, `notify`, `termtext`, `yamlenv`),
`agent/` (this code, binary `sentinel`) and `server/` (binaries `sentinel-server` and `sentinelctl`). Until the repo is split
(phase 1) the code is still one module at the root, and the daemon can still serve a small JSON HTTP API (`internal/api`,
`net/http`, bearer token, off unless `api.listen` is set) and watch other servers (`internal/fleet`); both go away when the
control plane replaces them. The admin of the control plane signs in with one password: no usernames, no roles. Once the split
is done the daemon listens on no port.

- **Done means `make test` is green.** `make e2e` and `make e2e-engines` need docker.
- No AI/LLM features in the product: status and alerts are rule-based (thresholds, schedules).
- README and runbooks are Vietnamese, and so are the command and flag descriptions in `--help` and the screens of `sentinel manage` and `sentinel init` (technical terms such as job, backup, snapshot, restore, repository, retention and schedule stay in English); log lines, restic/engine errors, code, comments and commit messages are English.
- Layout follows go.dev/doc/modules/layout: `cmd/sentinel` holds thin cobra commands, `internal/` packages are flat and split by responsibility, and `internal/app` is the service the CLI, the manager and the API all call. In `internal/tui` each screen opened over the tabs (help, delete, snapshots, restore, busy, form, folder browser) is a `screen` in `screen.go`, kept in a stack on the model: push to open, pop to close. Prefer the standard library over a new dependency.
- **Remote actions are a fixed table** in `pkg/agentapi` (until `pkg/` exists, in the package that holds the wire types). Never add
  an action that runs a shell or takes a path, a package name or a free-form command. The agent's local allowlist
  (`control_plane.allow`, everything off by default) has the last word; the control plane cannot widen it.
- `internal/app` (agent) and `server/internal/app` are the services their CLI, TUI and HTTP layers call; `server/internal/httpapi`
  holds handlers only. `pkg/` never imports `agent/` or `server/`, and `server/` never imports `agent/` outside tests.
- Path-scoped rules live in `.claude/rules/` (Go code, deploy/ops).
