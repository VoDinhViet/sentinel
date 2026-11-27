#!/usr/bin/env bash
# End-to-end test for the MySQL/MariaDB and MongoDB engines: seed MariaDB 10.11,
# MySQL 5.7 and MongoDB 5.0 (auth on), back each up with sentinel to a local
# restic repository, restore the dump into a new database and compare row counts.
# sentinel runs inside an Alpine container with the same client tools as the
# Dockerfile, so the test does not need mysql/mongo clients on the host.
# Needs docker only.
set -euo pipefail

ROOT=$(cd "$(dirname "$0")/.." && pwd)
WORK=$(mktemp -d)
COMPOSE=(docker compose -f "$ROOT/test/docker-compose.engines.yml")
GO_IMAGE=${GO_IMAGE:-golang:1.27}

cleanup() {
	set +e
	"${COMPOSE[@]}" down -v >/dev/null 2>&1
	rm -rf "$WORK"
}
trap cleanup EXIT

pass() { printf '  \033[32mPASS\033[0m %s\n' "$*"; }
fail() { printf '  \033[31mFAIL\033[0m %s\n' "$*"; exit 1; }

echo "== building sentinel (static, linux)"
docker run --rm -v "$ROOT":/src -v "$WORK":/out -w /src/agent -e CGO_ENABLED=0 -e GOFLAGS=-buildvcs=false \
	"$GO_IMAGE" go build -o /out/sentinel ./cmd/sentinel

echo "== starting mariadb, mysql and mongo"
"${COMPOSE[@]}" up -d --wait >/dev/null

MYSQL_PASS='e2e#my sql'
"${COMPOSE[@]}" exec -T mariadb mariadb -uroot -pe2e-maria-pass shop -e \
	"CREATE TABLE orders (id INT AUTO_INCREMENT PRIMARY KEY, note VARCHAR(40), blob_col BLOB);
	 INSERT INTO orders (note, blob_col) SELECT md5(seq), unhex('00ff00ff') FROM seq_1_to_3000;
	 CREATE FUNCTION answer() RETURNS INT DETERMINISTIC RETURN 42;"
"${COMPOSE[@]}" exec -T mysql mysql -uroot -p"$MYSQL_PASS" shop -e \
	"CREATE TABLE orders (id INT AUTO_INCREMENT PRIMARY KEY, note VARCHAR(40), blob_col BLOB);
	 INSERT INTO orders (note, blob_col) VALUES ('a', unhex('00ff')), ('b', unhex('ff00')), ('c', NULL);" 2>/dev/null
"${COMPOSE[@]}" exec -T mongo mongo --quiet -u root -p e2e-mongo-pass --authenticationDatabase admin crm \
	--eval 'for (var i = 0; i < 2000; i++) db.contacts.insertOne({n: i, name: "c" + i})' >/dev/null
pass "databases seeded"

cat >"$WORK/config.yaml" <<YAML
server_name: e2e
store: /work/state.db
repositories:
  local: { url: /work/repo, password: e2e-restic-pass }
jobs:
  - name: maria
    type: mysql
    mysql: { host: mariadb, user: root, password: "\${MARIA_PASS}", database: shop, tls: disabled }
    repositories: [local]
  - name: mysql57
    type: mysql
    mysql: { host: mysql, user: root, password: "\${MYSQL_PASS}", database: shop, tls: disabled }
    repositories: [local]
  - name: mongo
    type: mongodb
    mongodb: { host: mongo, user: root, password: "\${MONGO_PASS}", database: crm }
    repositories: [local]
  - name: maria-wrong-password
    type: mysql
    mysql: { host: mariadb, user: root, password: "not-the-password", database: shop, tls: disabled }
    repositories: [local]
YAML

cat >"$WORK/inside.sh" <<'INSIDE'
set -eu
trap 'chmod -R a+rwX /work' EXIT # files made as root must stay removable by the host user
apk add --no-cache restic mariadb-client mongodb-tools >/dev/null
export MARIA_PASS=e2e-maria-pass MONGO_PASS=e2e-mongo-pass
S() { /work/sentinel -c /work/config.yaml --log-level warn "$@"; }

for job in maria mysql57 mongo; do
	S backup init "$job" >/dev/null
	S backup run "$job" >/dev/null || { echo "FAIL backup $job"; exit 1; }
	S backup restore "$job" latest --target "/work/restore-$job" | tail -1
done

S doctor || { echo "FAIL doctor"; exit 1; }

# A dump that fails must not leave a snapshot, and must not leave its credentials file.
S backup init maria-wrong-password >/dev/null
if S backup run maria-wrong-password >/dev/null 2>&1; then echo "FAIL backup with a wrong password reported success"; exit 1; fi
echo "wrong_password_snapshots=$(S backup list maria-wrong-password | tail -n +2 | wc -l)"

# While a dump runs, no process may carry a password on its command line.
for job in maria mysql57 mongo; do
	S backup run "$job" >/dev/null &
	pid=$!
	leaked=0
	while kill -0 "$pid" 2>/dev/null; do
		for secret in "$MARIA_PASS" "$MONGO_PASS" "$MYSQL_PASS_RAW"; do
			if grep -qF -- "$secret" /proc/[0-9]*/cmdline 2>/dev/null; then leaked=1; fi
		done
	done
	wait "$pid" || { echo "FAIL second backup of $job"; exit 1; }
	echo "cmdline_leak_$job=$leaked"
done

# The passwords must not be visible to other processes while dumping, and the
# temporary credential files must be gone afterwards.
ls /tmp/sentinel-* 2>/dev/null && { echo "FAIL leftover credentials file"; exit 1; }

count() { mariadb --skip-ssl -h "$1" -uroot -p"$2" -N -e "$3"; }
MYSQL_PASS=$(printf '%s' "$MYSQL_PASS_RAW")

# MariaDB: load into a new database on the same server and compare.
count mariadb e2e-maria-pass "CREATE DATABASE restored"
mariadb --skip-ssl -h mariadb -uroot -pe2e-maria-pass restored </work/restore-maria/shop.sql
echo "maria_rows=$(count mariadb e2e-maria-pass 'SELECT count(*) FROM restored.orders')"
echo "maria_fn=$(count mariadb e2e-maria-pass 'SELECT restored.answer()')"
echo "maria_blob=$(count mariadb e2e-maria-pass "SELECT count(*) FROM restored.orders WHERE blob_col = unhex('00ff00ff')")"

# MySQL 5.7
count mysql "$MYSQL_PASS" "CREATE DATABASE restored"
mariadb --skip-ssl -h mysql -uroot -p"$MYSQL_PASS" restored </work/restore-mysql57/shop.sql
echo "mysql_rows=$(count mysql "$MYSQL_PASS" 'SELECT count(*) FROM restored.orders')"
echo "mysql_null_blob=$(count mysql "$MYSQL_PASS" 'SELECT count(*) FROM restored.orders WHERE blob_col IS NULL')"

# MongoDB: restore the archive into another namespace and count.
mongorestore --host mongo -u root -p e2e-mongo-pass --authenticationDatabase admin --gzip \
	--archive=/work/restore-mongo/crm.archive.gz --nsFrom 'crm.*' --nsTo 'crm_restored.*' >/dev/null 2>&1
INSIDE
echo "== running sentinel against the databases"
NETWORK=$(docker network ls --filter name=sentinel-engines --format '{{.Name}}' | head -1)
OUT=$(docker run --rm --network "$NETWORK" -v "$WORK":/work -e MYSQL_PASS="$MYSQL_PASS" -e MYSQL_PASS_RAW="$MYSQL_PASS" \
	alpine:3.22 sh /work/inside.sh) || { echo "$OUT"; fail "sentinel run inside container"; }
echo "$OUT" | sed 's/^/    /'

val() { echo "$OUT" | sed -n "s/^$1=//p"; }
[ "$(val wrong_password_snapshots)" = 0 ] && pass "wrong password: no snapshot was made" || fail "wrong-password snapshots: $(val wrong_password_snapshots)"
for job in maria mysql57 mongo; do
	[ "$(val cmdline_leak_$job)" = 0 ] && pass "$job: password never on a command line" || fail "$job: password found on a command line"
done
[ "$(val maria_rows)" = 3000 ] && pass "mariadb: 3000 rows restored" || fail "mariadb rows: $(val maria_rows)"
[ "$(val maria_fn)" = 42 ] && pass "mariadb: stored function restored" || fail "mariadb routine: $(val maria_fn)"
[ "$(val maria_blob)" = 3000 ] && pass "mariadb: binary column intact" || fail "mariadb blob: $(val maria_blob)"
[ "$(val mysql_rows)" = 3 ] && pass "mysql 5.7: 3 rows restored" || fail "mysql rows: $(val mysql_rows)"
[ "$(val mysql_null_blob)" = 1 ] && pass "mysql 5.7: NULL blob intact" || fail "mysql null blob: $(val mysql_null_blob)"

mongo_rows=$("${COMPOSE[@]}" exec -T mongo mongo --quiet -u root -p e2e-mongo-pass --authenticationDatabase admin crm_restored \
	--eval 'print(db.contacts.countDocuments({}))' | tail -1 | tr -d '\r')
[ "$mongo_rows" = 2000 ] && pass "mongodb: 2000 documents restored" || fail "mongodb documents: $mongo_rows"
