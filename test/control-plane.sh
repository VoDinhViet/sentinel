#!/usr/bin/env bash
# End-to-end test of the control plane with the real binaries and no docker: init a server, start it,
# sign in with sentinelctl, add a server, join it with `sentinel agent join`, see it as active, revoke
# it and see its certificate refused, and check the audit chain.
# Needs bin/sentinel, bin/sentinel-server and bin/sentinelctl (make build build-server), curl and jq.
set -euo pipefail

ROOT=$(cd "$(dirname "$0")/.." && pwd)
SENTINEL="$ROOT/bin/sentinel"
SERVER="$ROOT/bin/sentinel-server"
CTL="$ROOT/bin/sentinelctl"
WORK=$(mktemp -d)
AGENT_PORT=19320
CTL_PORT=19321
PASSWORD="correct horse battery"

cleanup() {
	# Never change the exit status of the script (see test/e2e.sh).
	set +e
	[ -n "${DAEMON_PID:-}" ] && kill "$DAEMON_PID" 2>/dev/null
	[ -n "${SERVER_PID:-}" ] && kill "$SERVER_PID" 2>/dev/null
	sleep 1
	[ -n "${DAEMON_PID:-}" ] && kill -9 "$DAEMON_PID" 2>/dev/null
	[ -n "${SERVER_PID:-}" ] && kill -9 "$SERVER_PID" 2>/dev/null
	rm -rf "$WORK"
}
trap cleanup EXIT

pass() { printf '  \033[32mPASS\033[0m %s\n' "$*"; }
fail() {
	printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2
	[ -f "$WORK/server.log" ] && sed 's/^/    server: /' "$WORK/server.log" >&2
	exit 1
}
check() { # check <message> <command...>: the command must succeed
	local msg=$1
	shift
	"$@" >/dev/null 2>&1 && pass "$msg" || fail "$msg"
}
ctl() { "$CTL" --dir "$WORK/ctl" "$@"; }
agent_status() { # the HTTP status of whoami with the agent's certificate
	curl -s -o /dev/null -w '%{http_code}' --cacert "$WORK/agent/ca.pem" --cert "$WORK/agent/cert.pem" \
		--key "$WORK/agent/key.pem" "https://127.0.0.1:$AGENT_PORT/v1/agent/whoami"
}

for binary in "$SENTINEL" "$SERVER" "$CTL"; do
	[ -x "$binary" ] || fail "$binary is missing: run make build build-server"
done

echo "== init"
INIT=$(printf '%s\n' "$PASSWORD" | "$SERVER" -c "$WORK/server.yaml" init --host 127.0.0.1 \
	--listen "127.0.0.1:$AGENT_PORT" --ctl-listen "127.0.0.1:$CTL_PORT" --state-dir "$WORK/state" --password-stdin)
FINGERPRINT=$(printf '%s\n' "$INIT" | grep -o 'sha256:[0-9a-f]*' | head -1)
[ -n "$FINGERPRINT" ] && pass "init printed the CA fingerprint" || fail "no fingerprint in the output of init"
[ "$(stat -c %a "$WORK/state/ca-key.pem")" = 600 ] && pass "the CA key is mode 0600" || fail "the CA key is readable by others"

echo "== serve"
"$SERVER" -c "$WORK/server.yaml" serve >"$WORK/server.log" 2>&1 &
SERVER_PID=$!
for _ in $(seq 50); do
	printf '%s\n' "$PASSWORD" | ctl login --url "https://127.0.0.1:$CTL_PORT" --ca-fingerprint "$FINGERPRINT" --password-stdin >/dev/null 2>&1 && break
	sleep 0.2
done
check "sentinelctl login works" ctl server ls
[ "$(stat -c %a "$WORK/ctl/session")" = 600 ] && pass "the session file is mode 0600" || fail "the session file is readable by others"
if printf 'not the password\n' | ctl --dir "$WORK/other" login --url "https://127.0.0.1:$CTL_PORT" --ca-fingerprint "$FINGERPRINT" --password-stdin >/dev/null 2>&1; then
	fail "a wrong password logged in"
fi
pass "a wrong password is refused"
if printf '%s\n' "$PASSWORD" | ctl --dir "$WORK/other" login --url "https://127.0.0.1:$CTL_PORT" --ca-fingerprint "sha256:$(printf 'ab%.0s' $(seq 32))" --password-stdin >/dev/null 2>&1; then
	fail "a control plane with another CA was trusted"
fi
pass "a CA that is not the pinned one is refused"

echo "== join"
TOKEN=$(ctl server add web01 -o json | jq -r .token)
[ -n "$TOKEN" ] && [ "$TOKEN" != null ] && pass "server add gave a join token" || fail "no join token"
printf '%s\n' "$TOKEN" | "$SENTINEL" -c "$WORK/none.yaml" agent join --url "https://127.0.0.1:$AGENT_PORT" \
	--ca-fingerprint "$FINGERPRINT" --token-stdin --dir "$WORK/agent" >"$WORK/join.log" 2>&1 || {
	cat "$WORK/join.log" >&2
	fail "sentinel agent join"
}
pass "sentinel agent join"
grep -q "$TOKEN" "$WORK/join.log" && fail "the join token was printed" || pass "the join token is not printed"
[ "$(stat -c %a "$WORK/agent/key.pem")" = 600 ] && pass "the agent key is mode 0600" || fail "the agent key is readable by others"
[ "$(ctl server ls -o json | jq -r '.[0].state')" = active ] && pass "the server is active" || fail "the server is not active"
printf '%s\n' "$TOKEN" | "$SENTINEL" -c "$WORK/none.yaml" agent join --url "https://127.0.0.1:$AGENT_PORT" \
	--ca-fingerprint "$FINGERPRINT" --token-stdin --dir "$WORK/agent2" >/dev/null 2>&1 && fail "a join token worked twice"
pass "a join token works once"

echo "== certificate"
[ "$(agent_status)" = 200 ] && pass "the agent's certificate is accepted" || fail "the agent's certificate was not accepted"
[ "$(curl -s -o /dev/null -w '%{http_code}' --cacert "$WORK/agent/ca.pem" "https://127.0.0.1:$AGENT_PORT/v1/agent/whoami")" = 401 ] &&
	pass "no certificate, no agent route" || fail "an agent route answered without a certificate"

echo "== report"
if command -v restic >/dev/null 2>&1; then
	mkdir -p "$WORK/agentstate"
	cat >"$WORK/agent.yaml" <<EOF
server_name: web01
store: $WORK/agentstate/state.db
control_plane:
  url: "https://127.0.0.1:$AGENT_PORT"
  dir: "$WORK/agent"
  allow:
    doctor: true
    services: [demo.service]
    os_updates: check
EOF
	"$SENTINEL" -c "$WORK/agent.yaml" run >"$WORK/daemon.log" 2>&1 &
	DAEMON_PID=$!
	for _ in $(seq 50); do
		[ "$(ctl server show web01 -o json 2>/dev/null | jq -r '.report.host.hostname // empty')" != "" ] && break
		sleep 0.2
	done
	[ "$(ctl server show web01 -o json | jq -r '.status')" = ONLINE ] && pass "the daemon reports and the server is ONLINE" ||
		{ sed 's/^/    daemon: /' "$WORK/daemon.log" >&2; fail "the daemon's report did not arrive"; }
	ctl server show web01 | grep "Máy:" >/dev/null && pass "sentinelctl server show prints the host" || fail "server show printed no host"
	[ "$(ctl alert -o json | jq length)" = 0 ] && pass "no alert while the server reports" || fail "an alert is open for a reporting server"
	ctl server show web01 | grep "Cho phép:.*doctor.run.*service.restart" >/dev/null && pass "server show says which actions the server allows" || fail "server show does not list the allowed actions"

	echo "== task"
	if ctl run web01 backup.start job=nothing --wait >"$WORK/refused.txt" 2>&1; then fail "a backup was started on a server that does not allow it"; fi
	grep -q "not allowed" "$WORK/refused.txt" && pass "the agent refuses an action its allowlist does not have" || fail "no refusal in: $(cat "$WORK/refused.txt")"
	DOCTOR_STATE=$(ctl run web01 doctor.run --wait -o json | jq -r .state)
	case "$DOCTOR_STATE" in succeeded | failed) pass "doctor.run ran on the server ($DOCTOR_STATE)" ;; *) fail "doctor.run ended as: $DOCTOR_STATE" ;; esac
	ctl run web01 doctor.run --follow >"$WORK/follow.txt" 2>&1
	grep -q "running the checks" "$WORK/follow.txt" && grep -q "succeeded" "$WORK/follow.txt" && pass "run --follow streams the log and the result" || fail "no log from doctor.run --follow: $(cat "$WORK/follow.txt")"
	ctl task ls --server web01 | grep "doctor.run" >/dev/null && pass "task ls lists the tasks" || fail "task ls shows no task"
	if ctl run web01 shell.run cmd=id >/dev/null 2>&1; then fail "an action that is not in the table was queued"; fi
	pass "an action that is not in the table is refused by the control plane"
	ctl audit -n 50 | grep task.result >/dev/null && pass "the audit log has the results" || fail "no task.result in the audit log"
	echo "== service task"
	# The daemon looks for systemctl in fixed places, so a stand-in on PATH is not used: the real one answers. The
	# unit does not exist (and this is not root, usually), so the task must fail, but with systemctl's own words:
	# it got as far as systemctl with this unit. The exact arguments are covered by the executor's tests.
	ctl run web01 service.restart unit=demo.service --wait >"$WORK/service.txt" 2>&1 && fail "a unit that does not exist was restarted"
	grep -q "systemctl restart demo.service" "$WORK/service.txt" || grep -q "cannot be told apart" "$WORK/service.txt" && pass "a unit on the allowlist is handed to systemctl, and its answer comes back" || fail "no answer from systemctl: $(cat "$WORK/service.txt")"
	if ctl run web01 service.stop unit=other.service --wait >"$WORK/other.txt" 2>&1; then fail "a unit that is not on the allowlist was stopped"; fi
	grep -q "control_plane.allow.services: \\[other.service\\]" "$WORK/other.txt" && pass "a unit that is not listed is refused, and the refusal says what to write" || fail "no refusal in: $(cat "$WORK/other.txt")"
	for bad in "unit=sentinel.service" "unit=demo.service;id" "unit=--user.service" "unit=/etc/x.service"; do
		if ctl run web01 service.restart "$bad" >"$WORK/bad.txt" 2>&1; then fail "the control plane queued service.restart $bad"; fi
	done
	pass "a unit that is the agent, a flag, a path or a second command never becomes a task"
	[ "$(ctl task ls --server web01 -o json | jq '[.[] | select(.params.unit != null and .params.unit != "demo.service" and .params.unit != "other.service")] | length')" = 0 ] && pass "no such task is in the queue" || fail "a bad unit is in the queue"
	echo "== os updates"
	# Only the check is allowed here, and it never changes the machine (apt-get -s). Where there is no apt-get the
	# task fails with a clear message, which also proves it reached the executor. The upgrade is never run.
	ctl run web01 os.check_updates --wait >"$WORK/osupdates.txt" 2>&1
	grep -q "packages can be upgraded" "$WORK/osupdates.txt" || grep -q "has no apt-get" "$WORK/osupdates.txt" && pass "os.check_updates reaches the executor and reports (or says there is no apt)" || fail "no answer: $(cat "$WORK/osupdates.txt")"
	if ctl run web01 os.apply_updates --yes --wait >"$WORK/apply.txt" 2>&1; then fail "an upgrade ran while os_updates is only check"; fi
	grep -q "control_plane.allow.os_updates: apply" "$WORK/apply.txt" && pass "os.apply_updates is refused unless os_updates is apply, and the refusal says what to write" || fail "no refusal in: $(cat "$WORK/apply.txt")"
	if ctl run web01 os.apply_updates </dev/null >"$WORK/apply2.txt" 2>&1; then fail "os.apply_updates was queued without a confirmation"; fi
	grep -q -- "--yes" "$WORK/apply2.txt" && pass "os.apply_updates asks for a confirmation (a script must say --yes)" || fail "no confirmation asked: $(cat "$WORK/apply2.txt")"
	kill "$DAEMON_PID" 2>/dev/null
	wait "$DAEMON_PID" 2>/dev/null
	DAEMON_PID=
else
	echo "  SKIP the daemon's report: restic is not installed"
fi

echo "== revoke"
ctl server revoke web01 --yes >/dev/null
[ "$(agent_status)" = 401 ] && pass "a revoked certificate is refused at once" || fail "a revoked certificate was still accepted"

echo "== audit"
ctl audit -n 100 | grep agent.revoke >/dev/null && pass "the audit log has the revoke" || fail "no revoke in the audit log"
"$SERVER" -c "$WORK/server.yaml" audit verify | grep nguyên >/dev/null && pass "audit chain (server)" || fail "audit verify (server)"
ctl audit verify | grep nguyên >/dev/null && pass "audit chain (sentinelctl)" || fail "audit verify (sentinelctl)"

echo "== screen"
ctl </dev/null | grep "màn hình terminal" >/dev/null && pass "sentinelctl with no terminal prints the help, not the screen" || fail "sentinelctl with no terminal did not print the help"

echo "== logout"
ctl logout >/dev/null
[ ! -e "$WORK/ctl/session" ] && pass "logout removes the session" || fail "the session file stayed"
if ctl server ls >/dev/null 2>&1; then fail "a command ran after logout"; fi
pass "no session, no commands"
echo "control plane e2e: all passed"
