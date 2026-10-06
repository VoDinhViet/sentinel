#!/usr/bin/env bash
# End-to-end test: back up a real Postgres database and a directory to an S3
# (SeaweedFS S3) and a local repository, restore both and compare with the originals;
# then exercise the central server: register an agent, connect it, trigger a backup
# and a config change from the web UI (via curl) and check both take effect.
# Needs docker, restic, pg_dump/pg_restore/psql (major version 18), curl in PATH.
set -euo pipefail

ROOT=$(cd "$(dirname "$0")/.." && pwd)
BIN="$ROOT/bin/sentinel"
WORK=$(mktemp -d)
COMPOSE=(docker compose -f "$ROOT/test/docker-compose.yml")

cleanup() {
	# A cleanup trap must never change the script's exit status: under set -e,
	# any plain "[ -n "$x" ] && cmd" that short-circuits (x empty) would make
	# bash report the WHOLE script as failed even when every check passed.
	set +e
	for pid in "${AGENT2_PID:-}" "${SRV_PID:-}"; do
		[ -n "$pid" ] && kill "$pid" 2>/dev/null
	done
	sleep 1
	for pid in "${AGENT2_PID:-}" "${SRV_PID:-}"; do
		[ -n "$pid" ] && kill -9 "$pid" 2>/dev/null
	done
	"${COMPOSE[@]}" down -v >/dev/null 2>&1 || true
	rm -rf "$WORK"
}
trap cleanup EXIT

# Polls a shell condition for up to $1 seconds, failing with message $2 on timeout.
wait_for() {
	local seconds=$1 msg=$2 cond=$3
	for _ in $(seq "$seconds"); do
		eval "$cond" && return 0
		sleep 1
	done
	fail "$msg"
}

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
status_out=$(S status)
printf '%s' "$status_out" | grep -q "FAILED" && pass "status shows the failed run" || fail "status missing failure"

echo "== central server: register and connect an agent"
SRV_WORK="$WORK/central"
mkdir -p "$SRV_WORK"
ADMIN_PASS="e2e-admin-pass"
HASH=$(printf '%s\n' "$ADMIN_PASS" | "$BIN" server hash-password 2>/dev/null)
[ -n "$HASH" ] || fail "hash-password produced no output"
{
	echo 'listen: "127.0.0.1:58080"'
	echo "db: $SRV_WORK/server.db"
	echo "offline_after: 30s"
	echo "secure_cookies: false"  # curl is plain HTTP in this test; real deployments keep this on
	echo "admin:"
	echo "  username: admin"
	printf '  password_hash: "%s"\n' "$HASH"
} >"$SRV_WORK/server.yaml"

"$BIN" --log-level warn server run --server-config "$SRV_WORK/server.yaml" >"$SRV_WORK/server.log" 2>&1 &
SRV_PID=$!
wait_for 30 "central server did not start" 'curl -s -o /dev/null http://127.0.0.1:58080/login'
pass "central server listening"

ADD_OUT=$("$BIN" server --server-config "$SRV_WORK/server.yaml" add-agent e2e-agent 2>"$SRV_WORK/add-agent.err")
export AGENT_TOKEN="$ADD_OUT"
AGENT_ID=$(grep -oE 'id [0-9]+' "$SRV_WORK/add-agent.err" | grep -oE '[0-9]+')
[ -n "$AGENT_TOKEN" ] && [ -n "$AGENT_ID" ] || fail "add-agent did not return a token and id"
pass "registered agent e2e-agent (id $AGENT_ID)"

COOKIES="$WORK/cookies.txt"
curl -s -c "$COOKIES" -d "username=admin" -d "password=$ADMIN_PASS" http://127.0.0.1:58080/login -o /dev/null
CSRF=$(curl -s -b "$COOKIES" -c "$COOKIES" http://127.0.0.1:58080/manage | grep -oE 'X-CSRF-Token": "[^"]+' | sed 's/.*: "//')
[ -n "$CSRF" ] || fail "could not log in to the central server UI"
pass "logged in to central server UI"

mkdir -p "$WORK/agent2/restore"
cat >"$WORK/agent2/config.yaml" <<EOF
server_name: e2e-agent
store: $WORK/agent2/state.db
restore_dir: $WORK/agent2/restore
central:
  url: "http://127.0.0.1:58080"
  token: "\${AGENT_TOKEN}"
  managed: true
repositories:
  local:
    url: "$WORK/agent2/repo"
    password: "\${RESTIC_PW}"
backups:
  - name: files2
    type: files
    paths: ["$WORK/files"]
    repositories: [local]
EOF
"$BIN" -c "$WORK/agent2/config.yaml" backup init files2 >/dev/null || fail "could not init agent2's backup repository"
"$BIN" --log-level warn -c "$WORK/agent2/config.yaml" run >"$WORK/agent2/agent.log" 2>&1 &
AGENT2_PID=$!

wait_for 60 "agent never showed online in the central UI" \
	'page=$(curl -s -b "$COOKIES" "http://127.0.0.1:58080/manage"); printf "%s" "$page" | grep -q "badge ok\">online"'
pass "agent connected and reports as online"

echo "== central server: backup now from the UI"
curl -s -b "$COOKIES" -H "X-CSRF-Token: $CSRF" -d "job=files2" \
	"http://127.0.0.1:58080/agents/$AGENT_ID/backup" -o "$WORK/backup-now.html"
grep -qE "queued|running|badge ok" "$WORK/backup-now.html" || fail "backup now did not queue a command"
wait_for 90 "backup triggered from the UI never completed" \
	'page=$(curl -s -b "$COOKIES" "http://127.0.0.1:58080/agents/'"$AGENT_ID"'"); printf "%s" "$page" | grep -q "badge ok\">ok<"'
pass "backup triggered from the UI completed and is visible on the agent page"

echo "== central server: change config from the UI"
# The textarea content spans multiple lines, so extract it with awk rather than
# sed. A real browser HTML-decodes textarea content automatically; curl does
# not, so undo html/template's escaping (&#34; etc.) before reusing the text.
html_decode() {
	sed -e 's/&#34;/"/g' -e "s/&#39;/'/g" -e 's/&lt;/</g' -e 's/&gt;/>/g' -e 's/&amp;/\&/g'
}
MANAGED_YAML=$(curl -s -b "$COOKIES" "http://127.0.0.1:58080/agents/$AGENT_ID/config" | awk '
	/<textarea/ { intex = 1; sub(/^[^>]*>/, "") }
	intex {
		if (match($0, /<\/textarea>/)) { print substr($0, 1, RSTART - 1); exit }
		print
	}
' | html_decode)
NEW_YAML=$(printf '%s\nuptime:\n  - { name: e2e-check, type: tcp, address: "127.0.0.1:55432" }\n' "$MANAGED_YAML")
curl -s -L -b "$COOKIES" -H "X-CSRF-Token: $CSRF" \
	--data-urlencode "yaml=$NEW_YAML" --data-urlencode "comment=e2e added uptime check" \
	"http://127.0.0.1:58080/agents/$AGENT_ID/config" -o "$WORK/config-save.html"
grep -q "Saved as v2" "$WORK/config-save.html" || fail "config was not saved as a new version: $(cat "$WORK/config-save.html")"
pass "new config version saved"
# Poll the agent's own cache file for the new content: it is ground truth for
# "the agent fetched, accepted and applied v2", unlike any single badge on the
# config page (the history table shows "running" next to v1 until the agent
# actually switches, so grepping the page for that text alone proves nothing).
wait_for 120 "agent never applied the config edited in the UI" \
	'grep -q "uptime:" "$WORK/agent2/managed-config.json" 2>/dev/null'
pass "agent applied the config edited in the UI"

echo "== all e2e checks passed"
