# Sentinel

One Go binary (`cmd/sentinel`) that runs on each Linux server as an **agent** (restic backups of Postgres, MySQL/MariaDB,
MongoDB and files to S3 / Google Drive; uptime checks; alerts) and optionally as a **central server** (`sentinel server`)
with a React web UI embedded in the binary. Monitoring metrics (CPU, memory, containers...) come from Beszel.

## Commands

- `make build`: frontend (`web/frontend`, Vite) then `CGO_ENABLED=0 go build` into `bin/sentinel`.
- `make test`: `go vet ./...`, `go test ./...`, `tsc -b` for the frontend. **Done means this is green.**
- `make e2e`: `test/e2e.sh` (docker: S3 via SeaweedFS, agent + server, web UI flows). `make e2e-engines`: MySQL and MongoDB.
- `make dev-frontend`: Vite with hot reload, proxies `/api` to `sentinel server run` on :8080.

## Layout

- `cmd/sentinel`: cobra commands (`run`, `backup`, `check`, `doctor`, `status`, `notify`, `server`).
- `internal/config`: YAML config, `${VAR}` expansion from `/etc/sentinel/sentinel.env`, strict decoding.
- `internal/backup` (restic wrapper), `internal/engine` (one file per DB engine, dump tool argv), `internal/scheduler`
  and `internal/schedule` (cron), `internal/uptime`, `internal/alert` + `internal/notify` (Telegram, email, Discord, Slack).
- `internal/store`: agent state in SQLite. `internal/agent`: reports to the central server.
- `internal/server`: HTTP API (`webapi*.go` for the UI, `agentapi.go` for agents), auth, audit, SPA;
  `internal/server/service`: business logic and the server's SQLite DB. `internal/beszel`: Beszel client.
- `web/frontend`: React + Tailwind + shadcn UI; build output goes to `internal/server/web/dist` (generated, do not edit).
- `deploy/`: install scripts, systemd units, example configs, docker compose. `gm/`: Vietnamese operations runbooks.

## Rules

- Path-scoped rules live in `.claude/rules/` (Go backend, frontend UI, deploy/ops).
- No AI/LLM features in the product: status and alerts are rule-based (thresholds, schedules).
- README, runbooks and `gm/` are Vietnamese; UI copy, code, comments and commit messages are English.
- Agents for this repo: `backup-safety-reviewer`, `go-api-reviewer`, `refactorer`, `ui-verifier` (`.claude/agents/`).
