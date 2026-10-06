#!/usr/bin/env bash
# End-to-end test: back up a real Postgres database and a directory to an S3
# (SeaweedFS S3) and a local repository, restore both and compare with the originals.
# Needs docker, restic, pg_dump/pg_restore/psql (major version 18) in PATH.
set -euo pipefail

ROOT=$(cd "$(dirname "$0")/.." && pwd)
BIN="$ROOT/bin/sentinel"
WORK=$(mktemp -d)
COMPOSE=(docker compose -f "$ROOT/test/docker-compose.yml")

cleanup() {
	"${COMPOSE[@]}" down -v >/dev/null 2>&1 || true
	rm -rf "$WORK"
}
trap cleanup EXIT

pass() { printf '  \033[32mPASS\033[0m %s\n' "$*"; }
fail() { printf '  \033[31mFAIL\033[0m %s\n' "$*"; exit 1; }

echo "== starting postgres and s3"
"${COMPOSE[@]}" up -d --wait >/dev/null
for i in $(seq 60); do
	curl -s -o /dev/null http://127.0.0.1:59000/ && break
	[ "$i" -eq 60 ] && fail "s3 gateway did not start"
	sleep 1
done

export PGHOST=127.0.0.1 PGPORT=55432 PGUSER=postgres PGPASSWORD=e2e-pg-pass
psql -q -d app -c "CREATE TABLE orders (id serial PRIMARY KEY, note text);
                   INSERT INTO orders (note) SELECT md5(i::text) FROM generate_series(1, 5000) i;"

mkdir -p "$WORK/files/sub"
head -c 2000000 /dev/urandom >"$WORK/files/blob.bin"
echo "hello" >"$WORK/files/sub/hello.txt"
echo "skip me" >"$WORK/files/skip.tmp"

export RESTIC_PW=e2e-restic-pass
cat >"$WORK/config.yaml" <<EOF
server_name: e2e
store: $WORK/state.db
repositories:
  s3:
    url: "s3:http://127.0.0.1:59000/sentinel/e2e"
    password: "\${RESTIC_PW}"
    env:
      AWS_ACCESS_KEY_ID: e2e-access
      AWS_SECRET_ACCESS_KEY: e2e-secret-key
  local:
    url: "$WORK/repo-local"
    password: "\${RESTIC_PW}"
backups:
  - name: db
    type: postgres
    postgres: { host: 127.0.0.1, port: 55432, user: postgres, password: "\${PGPASSWORD}", database: app }
    repositories: [s3, local]
    retention: { last: 2 }
  - name: files
    type: files
    paths: ["$WORK/files"]
    exclude: ["*.tmp"]
    repositories: [s3]
EOF
S() { "$BIN" -c "$WORK/config.yaml" --log-level warn "$@"; }

echo "== init"
S backup init db >/dev/null || fail "init db"
S backup init files >/dev/null || fail "init files"
pass "repositories initialized"
S doctor >/dev/null || fail "doctor"
pass "doctor"

echo "== postgres backup + restore"
S backup run db >/dev/null || fail "backup db"
pass "backup db to s3 and local"
for _ in 1 2; do S backup run db -r s3 >/dev/null; done
count=$(S backup list db -r s3 | tail -n +2 | wc -l)
[ "$count" -eq 2 ] && pass "retention keeps last 2 (have $count)" || fail "retention: $count snapshots"

S backup restore db latest -r s3 --target "$WORK/restore-db" >/dev/null
psql -q -d postgres -c "CREATE DATABASE restored"
pg_restore --no-owner -d restored "$WORK/restore-db/app.dump"
rows=$(psql -tA -d restored -c "SELECT count(*) FROM orders")
[ "$rows" -eq 5000 ] && pass "restored database has 5000 rows" || fail "restored rows: $rows"

echo "== files backup + restore"
S backup run files >/dev/null || fail "backup files"
pass "backup files"
S backup restore files latest --target "$WORK/restore-files" >/dev/null
restored="$WORK/restore-files$WORK/files"
cmp "$WORK/files/blob.bin" "$restored/blob.bin" && diff -q "$WORK/files/sub" "$restored/sub" >/dev/null &&
	pass "restored files identical" || fail "restored files differ"
[ ! -e "$restored/skip.tmp" ] && pass "excluded file not backed up" || fail "skip.tmp was backed up"

echo "== failure handling"
"${COMPOSE[@]}" stop postgres >/dev/null
if S backup run db -r local >/dev/null 2>&1; then fail "backup with postgres down reported success"; fi
pass "backup fails when postgres is down"
S status | grep -q "FAILED" && pass "status shows the failed run" || fail "status missing failure"

echo "== all e2e checks passed"
