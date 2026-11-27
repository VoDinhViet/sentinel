---
paths:
  - "deploy/**"
  - "Dockerfile"
  - ".goreleaser.yaml"
  - "test/*.sh"
  - "test/*.yml"
---

# Deploy and ops rules

- **Install scripts** (`deploy/install.sh`) are POSIX `#!/bin/sh` with `set -eu`, so they run on
  minimal distros. No bash-only syntax there. They must be idempotent: running them again upgrades the binary and keeps the
  existing `/etc/sentinel/config.yaml` and `sentinel.env`.
- **Secrets** live only in `/etc/sentinel/sentinel.env` (mode 0600, loaded by `EnvironmentFile=`). Never put a real secret in
  a unit file, compose file, example config, Dockerfile or test fixture; examples use `${VAR}` placeholders.
- **systemd units** keep their hardening (`ProtectSystem`, `PrivateTmp`, `NoNewPrivileges`, `StateDirectory`); the daemon
  needs `CAP_NET_RAW` for ping. Do not loosen these without saying why.
- **Control plane unit** (`deploy/server/sentinel-server.service`): same hardening, no capabilities, `StateDirectory=sentinel-server`
  (database, CA key and server key live there, directory 0700, keys 0600). Its secrets go in `/etc/sentinel/server.env`, mode 0600.
  The admin password is never in a file, an env var or argv: it is typed to `sentinel-server init`/`passwd`.
- **The agent daemon listens on no port.** It dials out to the control plane; do not add a listener or open a firewall port for it.
  Certificates the agent holds live under its state directory, not `/etc` (`ProtectSystem=full` makes `/etc` read-only).
- **Docker**: the image packages the prebuilt `bin/sentinel` on Alpine with restic, rclone and the dump tools; it runs as root unless
  compose sets uid 10001. Keep dump tool versions compatible with the engines the e2e tests cover.
- **e2e scripts** (`test/*.sh`) clean up their containers on exit and must keep passing: `make e2e`, `make e2e-engines`.
- Operator docs for these files (`README.md`) are Vietnamese; update them when behaviour or flags change.
