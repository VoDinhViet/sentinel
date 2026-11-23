#!/bin/sh
# Install Sentinel and the tools it runs on Ubuntu/Debian, in one command, from a checkout of
# https://github.com/Cong-ty-ThoMi-Group/gm-backup (a private repo, so there is no raw download):
#
#   git clone https://github.com/Cong-ty-ThoMi-Group/gm-backup.git && cd gm-backup
#   make build && sudo ./deploy/install.sh ./bin/sentinel
#
# With options (after the binary):
#   sudo ./deploy/install.sh ./bin/sentinel --no-mongo
#
# From a release built elsewhere (`make dist` writes dist/sentinel_linux_<arch>.tar.gz):
#   tar -xzf sentinel_linux_amd64.tar.gz && sudo ./deploy/install.sh ./sentinel
#
# Without a binary the script downloads the latest release of SENTINEL_REPO; that only works when
# the release files can be fetched without logging in (a public repo, or SENTINEL_BASE_URL).
#
# Options:
#   --version TAG      install this release (default: the latest)
#   --no-db-clients    do not install the PostgreSQL and MariaDB client tools
#   --no-mongo         do not install mongodump
#   -h, --help         show this text
#
# Environment: SENTINEL_REPO (default Cong-ty-ThoMi-Group/gm-backup), SENTINEL_BASE_URL (where release
# files are downloaded from), RESTIC_VERSION, MONGO_TOOLS_VERSION.
#
# Idempotent: running it again upgrades the binary and keeps /etc/sentinel/config.yaml and
# /etc/sentinel/sentinel.env. POSIX sh, no bashisms.
set -eu

SENTINEL_REPO="${SENTINEL_REPO:-Cong-ty-ThoMi-Group/gm-backup}"
RESTIC_VERSION="${RESTIC_VERSION:-0.19.1}"
MONGO_TOOLS_VERSION="${MONGO_TOOLS_VERSION:-100.20.1}"
VERSION=latest
DB_CLIENTS=1
MONGO=1
BIN_SRC=

usage() {
	cat <<'EOT'
Install Sentinel and the tools it runs on Ubuntu/Debian.

  sudo ./deploy/install.sh ./bin/sentinel                # a binary you built yourself (make build)
  sudo ./deploy/install.sh ./bin/sentinel --no-mongo
  sudo ./deploy/install.sh --version v1.2.0              # download a release of SENTINEL_REPO

Options:
  --version TAG      install this release (default: the latest)
  --no-db-clients    do not install the PostgreSQL and MariaDB client tools
  --no-mongo         do not install mongodump
  -h, --help         show this text

Environment: SENTINEL_REPO, SENTINEL_BASE_URL, RESTIC_VERSION, MONGO_TOOLS_VERSION.
EOT
}

while [ $# -gt 0 ]; do
	case "$1" in
	--version)
		[ $# -ge 2 ] || { echo "--version needs a tag, e.g. v1.2.0" >&2; exit 2; }
		VERSION="$2"
		shift
		;;
	--no-db-clients) DB_CLIENTS=0 ;;
	--no-mongo) MONGO=0 ;;
	-h | --help)
		usage
		exit 0
		;;
	-*)
		echo "unknown option $1 (see --help)" >&2
		exit 2
		;;
	*) BIN_SRC="$1" ;;
	esac
	shift
done

say() { printf '\n==> %s\n' "$*"; }
die() { printf 'error: %s\n' "$*" >&2; exit 1; }

[ "$(id -u)" -eq 0 ] || die "run as root: curl -fsSL <url> | sudo sh"

case "$(uname -s)" in Linux) ;; *) die "Linux only" ;; esac
case "$(uname -m)" in
x86_64) ARCH=amd64 MONGO_ARCH=x86_64 ;;
aarch64 | arm64) ARCH=arm64 MONGO_ARCH=arm64 ;;
*) die "unsupported architecture $(uname -m)" ;;
esac

HAVE_APT=0
command -v apt-get >/dev/null 2>&1 && HAVE_APT=1

TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT

apt_install() {
	[ "$HAVE_APT" -eq 1 ] || die "this installer uses apt (Ubuntu/Debian); install by hand: $*"
	DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends "$@" >/dev/null
}

if [ "$HAVE_APT" -eq 1 ]; then
	say "updating the package index"
	DEBIAN_FRONTEND=noninteractive apt-get update -qq
fi
missing=""
for tool in curl tar bzip2 unzip sha256sum; do
	command -v "$tool" >/dev/null 2>&1 || missing="$missing $tool"
done
if [ -n "$missing" ] || [ ! -d /etc/ssl/certs ]; then
	say "installing base tools:$missing ca-certificates"
	apt_install curl ca-certificates tar bzip2 unzip coreutils tzdata
fi

fetch() { curl -fsSL --retry 3 --retry-delay 2 -o "$2" "$1"; }

# ---- Sentinel itself: a binary given on the command line, or a release download.
if [ -n "$BIN_SRC" ]; then
	[ -f "$BIN_SRC" ] || die "$BIN_SRC is not a file"
	DEPLOY_DIR=$(cd "$(dirname "$0")" && pwd)
else
	if [ "$VERSION" = latest ]; then
		BASE_URL="${SENTINEL_BASE_URL:-https://github.com/$SENTINEL_REPO/releases/latest/download}"
	else
		BASE_URL="${SENTINEL_BASE_URL:-https://github.com/$SENTINEL_REPO/releases/download/$VERSION}"
	fi
	ASSET="sentinel_linux_${ARCH}.tar.gz"
	say "downloading Sentinel ($VERSION, linux/$ARCH)"
	fetch "$BASE_URL/$ASSET" "$TMP/$ASSET" || die "could not download $BASE_URL/$ASSET (is there a release in $SENTINEL_REPO?)"
	fetch "$BASE_URL/checksums.txt" "$TMP/checksums.txt" || die "could not download $BASE_URL/checksums.txt"
	(cd "$TMP" && grep " $ASSET\$" checksums.txt | sha256sum -c - >/dev/null) || die "checksum mismatch for $ASSET: not installing"
	mkdir -p "$TMP/pkg"
	tar -xzf "$TMP/$ASSET" -C "$TMP/pkg"
	BIN_SRC="$TMP/pkg/sentinel"
	DEPLOY_DIR="$TMP/pkg/deploy"
fi
[ -f "$DEPLOY_DIR/sentinel.service" ] && [ -f "$DEPLOY_DIR/config.example.yaml" ] || die "deploy files (sentinel.service, config.example.yaml) not found in $DEPLOY_DIR"

# ---- Tools Sentinel runs.
if ! command -v restic >/dev/null 2>&1; then
	say "installing restic $RESTIC_VERSION"
	fetch "https://github.com/restic/restic/releases/download/v${RESTIC_VERSION}/restic_${RESTIC_VERSION}_linux_${ARCH}.bz2" "$TMP/restic.bz2"
	bunzip2 "$TMP/restic.bz2"
	install -m 0755 "$TMP/restic" /usr/local/bin/restic
fi

if ! command -v rclone >/dev/null 2>&1; then
	say "installing rclone"
	fetch "https://downloads.rclone.org/rclone-current-linux-${ARCH}.zip" "$TMP/rclone.zip"
	(cd "$TMP" && unzip -q rclone.zip)
	install -m 0755 "$TMP"/rclone-*-linux-"$ARCH"/rclone /usr/local/bin/rclone
fi

if [ "$DB_CLIENTS" -eq 1 ]; then
	if [ "$HAVE_APT" -eq 1 ]; then
		say "installing the PostgreSQL and MariaDB clients (pg_dump, mariadb-dump)"
		apt_install postgresql-client mariadb-client
	else
		echo "note: not an apt system; install pg_dump and mariadb-dump yourself" >&2
	fi
fi

if [ "$MONGO" -eq 1 ] && ! command -v mongodump >/dev/null 2>&1; then
	say "installing mongodump $MONGO_TOOLS_VERSION"
	fetch "https://fastdl.mongodb.org/tools/db/mongodb-database-tools-ubuntu2204-${MONGO_ARCH}-${MONGO_TOOLS_VERSION}.tgz" "$TMP/mongo.tgz"
	mkdir -p "$TMP/mongo"
	tar -xzf "$TMP/mongo.tgz" -C "$TMP/mongo" --strip-components=1
	install -m 0755 "$TMP/mongo/bin/mongodump" /usr/local/bin/mongodump
	install -m 0755 "$TMP/mongo/bin/mongorestore" /usr/local/bin/mongorestore
fi

# ---- Install.
say "installing sentinel"
install -m 0755 "$BIN_SRC" /usr/local/bin/sentinel

install -d -m 0750 /etc/sentinel
if [ ! -f /etc/sentinel/config.yaml ]; then
	install -m 0640 "$DEPLOY_DIR/config.example.yaml" /etc/sentinel/config.yaml
	echo "wrote /etc/sentinel/config.yaml - edit it before starting"
fi
if [ ! -f /etc/sentinel/sentinel.env ]; then
	install -m 0600 /dev/null /etc/sentinel/sentinel.env
	echo "created /etc/sentinel/sentinel.env - put secrets here (KEY=value)"
fi

if [ -d /run/systemd/system ]; then
	install -m 0644 "$DEPLOY_DIR/sentinel.service" /etc/systemd/system/sentinel.service
	systemctl daemon-reload
else
	echo "note: systemd is not running here; the unit was not installed" >&2
fi

say "installed: $(/usr/local/bin/sentinel --version)"
cat <<'EOF'

Next steps:
  1. Edit /etc/sentinel/config.yaml and /etc/sentinel/sentinel.env
     (or start over with `sudo sentinel init --force`: a form that writes a short config
     and a random repository password into sentinel.env)
  2. For Google Drive: rclone config  (as root, so the service can read ~/.config/rclone)
  3. set -a; . /etc/sentinel/sentinel.env; set +a
     sentinel backup init <job>     # once per job
     sentinel doctor
     sentinel notify test
  4. systemctl enable --now sentinel
     journalctl -u sentinel -f
  Anytime: sudo sentinel manage     # interactive terminal screen
EOF
