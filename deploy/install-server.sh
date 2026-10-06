#!/bin/sh
# Install the sentinel central server and enable it.
# Usage: sudo ./install-server.sh [path/to/sentinel-binary]
set -eu

BIN_SRC="${1:-./sentinel}"
SCRIPT_DIR=$(cd "$(dirname "$0")" && pwd)

if [ "$(id -u)" -ne 0 ]; then
	echo "run as root (sudo)" >&2
	exit 1
fi

echo "installing sentinel from $BIN_SRC"
install -m 0755 "$BIN_SRC" /usr/local/bin/sentinel

install -d -m 0750 /etc/sentinel
if [ ! -f /etc/sentinel/server.yaml ]; then
	install -m 0640 "$SCRIPT_DIR/server.example.yaml" /etc/sentinel/server.yaml
	echo "wrote /etc/sentinel/server.yaml - edit it before starting"
fi
if [ ! -f /etc/sentinel/sentinel.env ]; then
	install -m 0600 /dev/null /etc/sentinel/sentinel.env
	echo "created /etc/sentinel/sentinel.env - put secrets here (KEY=value)"
fi

install -m 0644 "$SCRIPT_DIR/sentinel-server.service" /etc/systemd/system/sentinel-server.service
systemctl daemon-reload

cat <<'EOF'

Next steps:
  1. Choose an admin password and hash it:
       sentinel server hash-password
     Put the result in /etc/sentinel/server.yaml as admin.password_hash
  2. Edit /etc/sentinel/server.yaml (listen address, TLS or reverse proxy, notifiers)
     and /etc/sentinel/sentinel.env (secrets referenced as ${VAR})
  3. systemctl enable --now sentinel-server
     journalctl -u sentinel-server -f
  4. Open the UI, log in, and use "Add server" to register your first agent -
     it shows the token and config snippet to put on that server.

Alternatively, register an agent from the command line:
  set -a; . /etc/sentinel/sentinel.env; set +a
  sentinel server add-agent web-01
EOF
