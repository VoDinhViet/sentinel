---
paths:
  - "deploy/**"
  - "Dockerfile"
  - ".goreleaser.yaml"
  - "test/*.sh"
  - "test/*.yml"
---

# Deploy and ops rules

- **Install scripts** (`deploy/install.sh`, `deploy/install-server.sh`) are POSIX `#!/bin/sh` with `set -eu`, so they run on
  minimal distros. No bash-only syntax there. They must be idempotent: running them again upgrades the binary and keeps the
  existing `/etc/sentinel/config.yaml` and `sentinel.env`.
- **Secrets** live only in `/etc/sentinel/sentinel.env` (mode 0600, loaded by `EnvironmentFile=`). Never put a real secret in
  a unit file, compose file, example config, Dockerfile or test fixture; examples use `${VAR}` placeholders.
- **systemd units** keep their hardening (`ProtectSystem`, `PrivateTmp`, `NoNewPrivileges`, `StateDirectory`); the agent
  needs `CAP_NET_RAW` for ping. Do not loosen these without saying why.
- **Docker**: the image packages the prebuilt `bin/sentinel` on Alpine with restic, rclone and the dump tools; the server runs
  as uid 10001. Keep dump tool versions compatible with the engines the e2e tests cover.
- **e2e scripts** (`test/*.sh`) clean up their containers on exit and must keep passing: `make e2e`, `make e2e-engines`.
- Operator docs for these files (`README.md`, `gm/`) are Vietnamese; update them when behaviour or flags change.
