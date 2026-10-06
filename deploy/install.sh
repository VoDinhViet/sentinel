#!/bin/sh
# Install sentinel, restic and rclone on a Linux server and enable the service.
# Usage: sudo ./install.sh [path/to/sentinel-binary]
set -eu

RESTIC_VERSION="${RESTIC_VERSION:-0.19.1}"
BIN_SRC="${1:-./sentinel}"
SCRIPT_DIR=$(cd "$(dirname "$0")" && pwd)

if [ "$(id -u)" -ne 0 ]; then
	echo "run as root (sudo)" >&2
	exit 1
fi

case "$(uname -m)" in
	x86_64) ARCH=amd64 ;;
	aarch64 | arm64) ARCH=arm64 ;;
	*) echo "unsupported architecture $(uname -m)" >&2; exit 1 ;;
esac

TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT

if ! command -v restic >/dev/null 2>&1; then
	echo "installing restic $RESTIC_VERSION"
	curl -fsSL -o "$TMP/restic.bz2" \
		"https://github.com/restic/restic/releases/download/v${RESTIC_VERSION}/restic_${RESTIC_VERSION}_linux_${ARCH}.bz2"
	bunzip2 "$TMP/restic.bz2"
	install -m 0755 "$TMP/restic" /usr/local/bin/restic
fi

if ! command -v rclone >/dev/null 2>&1; then
	echo "installing rclone"
	curl -fsSL -o "$TMP/rclone.zip" "https://downloads.rclone.org/rclone-current-linux-${ARCH}.zip"
	(cd "$TMP" && unzip -q rclone.zip)
	install -m 0755 "$TMP"/rclone-*-linux-"$ARCH"/rclone /usr/local/bin/rclone
fi

echo "installing sentinel from $BIN_SRC"
install -m 0755 "$BIN_SRC" /usr/local/bin/sentinel

install -d -m 0750 /etc/sentinel
if [ ! -f /etc/sentinel/config.yaml ]; then
	install -m 0640 "$SCRIPT_DIR/config.example.yaml" /etc/sentinel/config.yaml
	echo "wrote /etc/sentinel/config.yaml - edit it before starting"
fi
if [ ! -f /etc/sentinel/sentinel.env ]; then
	install -m 0600 /dev/null /etc/sentinel/sentinel.env
	echo "created /etc/sentinel/sentinel.env - put secrets here (KEY=value)"
fi

install -m 0644 "$SCRIPT_DIR/sentinel.service" /etc/systemd/system/sentinel.service
systemctl daemon-reload

cat <<'EOF'

Next steps:
  1. Edit /etc/sentinel/config.yaml and /etc/sentinel/sentinel.env
  2. For Google Drive: rclone config  (as root, so the service can read ~/.config/rclone)
  3. set -a; . /etc/sentinel/sentinel.env; set +a
     sentinel backup init <job>     # once per job
     sentinel doctor
     sentinel notify test
  4. systemctl enable --now sentinel
     journalctl -u sentinel -f
EOF
