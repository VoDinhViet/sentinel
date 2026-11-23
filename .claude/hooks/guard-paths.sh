#!/bin/sh
# PreToolUse (Edit|Write|MultiEdit|NotebookEdit): refuse edits to generated files and secrets.
# Exit 2 blocks the tool call and shows the message to Claude.
command -v jq >/dev/null 2>&1 || exit 0

file=$(jq -r '.tool_input.file_path // .tool_input.notebook_path // empty')
[ -n "$file" ] || exit 0
rel=${file#"${CLAUDE_PROJECT_DIR:-$PWD}"/}

case "$rel" in
internal/server/web/dist/*)
	echo "Blocked: $rel is build output of web/frontend (make frontend). Edit the source in web/frontend/src instead." >&2
	exit 2
	;;
*.env)
	echo "Blocked: $rel holds secrets. Ask the user to edit it; examples belong in deploy/*.example.yaml with \${VAR}." >&2
	exit 2
	;;
go.sum)
	echo "Blocked: go.sum is maintained by the Go toolchain. Run 'go mod tidy' or 'go get' instead of editing it." >&2
	exit 2
	;;
esac
exit 0
