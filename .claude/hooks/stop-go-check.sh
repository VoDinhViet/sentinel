#!/bin/sh
# Stop: when Go files were edited in this session, run go vet before Claude finishes.
# Exit 2 keeps Claude working with the errors; a passing check clears the marker.
command -v jq >/dev/null 2>&1 || exit 0
command -v go >/dev/null 2>&1 || exit 0

input=$(cat)
[ "$(printf '%s' "$input" | jq -r '.stop_hook_active // false')" = "true" ] && exit 0
session=$(printf '%s' "$input" | jq -r '.session_id // "default"')
marker="${TMPDIR:-/tmp}/sentinel-claude-go-dirty-$session"
[ -f "$marker" ] || exit 0

cd "${CLAUDE_PROJECT_DIR:-$PWD}" || exit 0
# go vet type-checks every package, so it also catches build errors.
if ! out=$(go vet ./... 2>&1); then
	printf 'go vet failed after Go edits:\n%s\n' "$(printf '%s\n' "$out" | tail -40)" >&2
	exit 2
fi
rm -f "$marker"
exit 0
