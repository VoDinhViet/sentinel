#!/bin/sh
# PostToolUse (Edit|Write|MultiEdit): format Go files.
# Exit 2 sends stderr back to Claude so it can fix the file; missing tools are skipped silently.
command -v jq >/dev/null 2>&1 || exit 0

input=$(cat)
file=$(printf '%s' "$input" | jq -r '.tool_input.file_path // empty')
[ -n "$file" ] && [ -f "$file" ] || exit 0
root=${CLAUDE_PROJECT_DIR:-$PWD}
rel=${file#"$root"/}

case "$rel" in
*.go)
	session=$(printf '%s' "$input" | jq -r '.session_id // "default"')
	# Tells the Stop hook that Go code changed in this session.
	: >"${TMPDIR:-/tmp}/sentinel-claude-go-dirty-$session"
	command -v gofmt >/dev/null 2>&1 || exit 0
	if ! out=$(gofmt -l -w "$file" 2>&1); then
		printf 'gofmt failed on %s:\n%s\n' "$rel" "$out" >&2
		exit 2
	fi
	;;
esac
exit 0
