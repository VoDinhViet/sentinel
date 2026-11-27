# Sentinel

The agent is one Go binary (`agent/cmd/sentinel`) that runs on each Linux server (restic backups of files and databases, uptime
checks, alerts). It is a CLI plus a daemon (`sentinel run`) driven by a YAML config. There is no web UI.

**Direction (RFC 0002, in progress):** an optional control plane, `sentinel-server`, that agents dial out to over mTLS. The
repo is three Go modules joined by `go.work`: `pkg/` (shared: `alert`, `notify`, `termtext`, `yamlenv`; `agentapi` and `pki`
come later), `agent/` (this code, binary `sentinel`) and `server/` (binaries `sentinel-server` and `sentinelctl`, not yet
there). **The agent stands alone**: it builds without `go.work` (`make layers` checks it), is one static binary, imports only
`agent/` and `pkg/`, and works with no control plane at all, so it can be rolled out to many servers by itself. The daemon can
still serve a small JSON HTTP API (`internal/api`, `net/http`, bearer token, off unless `api.listen` is set) and watch other
servers (`internal/fleet`); both go away when the control plane replaces them. The admin of the control plane signs in with one
password: no usernames, no roles. Once they are gone the daemon listens on no port. `deploy/` stays where it is until
`deploy/server/` is added (then it becomes `deploy/agent/`), because the release tarball and `./deploy/install.sh` are paths
operators use.

- **Done means `make test` is green** (it runs the `layers` check, then vet, test, lint and govulncheck in every module listed in
  `MODULES` in the Makefile). `make e2e` and `make e2e-engines` need docker.
- No AI/LLM features in the product: status and alerts are rule-based (thresholds, schedules).
- README and runbooks are Vietnamese, and so are the command and flag descriptions in `--help` and the screens of `sentinel manage` and `sentinel init` (technical terms such as job, backup, snapshot, restore, repository, retention and schedule stay in English); log lines, restic/engine errors, code, comments and commit messages are English.
- Layout follows go.dev/doc/modules/layout, inside each module: `agent/cmd/sentinel` holds thin cobra commands, `agent/internal/` packages are flat and split by responsibility, and `agent/internal/app` is the service the CLI, the manager and the API all call. In `agent/internal/tui` each screen opened over the tabs (help, delete, snapshots, restore, busy, form, folder browser) is a `screen` in `screen.go`, kept in a stack on the model: push to open, pop to close. Prefer the standard library over a new dependency.
- **Remote actions are a fixed table** in `pkg/agentapi` (until `pkg/` exists, in the package that holds the wire types). Never add
  an action that runs a shell or takes a path, a package name or a free-form command. The agent's local allowlist
  (`control_plane.allow`, everything off by default) has the last word; the control plane cannot widen it.
- `internal/app` (agent) and `server/internal/app` are the services their CLI, TUI and HTTP layers call; `server/internal/httpapi`
  holds handlers only. `pkg/` never imports `agent/` or `server/`, and `server/` never imports `agent/` outside tests.
- Path-scoped rules live in `.claude/rules/` (Go code, deploy/ops).
