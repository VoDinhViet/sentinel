#!/bin/sh
# PostToolUse (Edit|Write|MultiEdit): format Go files, lint frontend files, flag colour literals.
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
web/frontend/src/*.ts | web/frontend/src/*.tsx)
	status=0
	# Only the text just written, so existing generated shadcn code is not re-reported.
	written=$(printf '%s' "$input" | jq -r '.tool_input.new_string // .tool_input.content // ([.tool_input.edits[]?.new_string] | join("\n"))')
	colours=$(printf '%s\n' "$written" |
		grep -nP "(?<![\w&])#[0-9a-fA-F]{3,8}\b(?!['\"]\])|\b(rgba?|hsla?|oklch)\((?!\s*var\()" 2>/dev/null | head -5)
	if [ -n "$colours" ]; then
		printf 'Colour literal in %s; use a token from src/index.css (see .claude/rules/frontend-ui.md):\n%s\n' "$rel" "$colours" >&2
		status=2
	fi
	oxlint="$root/web/frontend/node_modules/.bin/oxlint"
	if [ -x "$oxlint" ]; then
		if ! out=$(cd "$root/web/frontend" && "$oxlint" "$file" 2>&1); then
			printf 'oxlint %s:\n%s\n' "$rel" "$(printf '%s\n' "$out" | tail -40)" >&2
			status=2
		fi
	fi
	exit $status
	;;
esac
exit 0
