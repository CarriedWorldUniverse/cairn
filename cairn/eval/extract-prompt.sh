#!/usr/bin/env bash
# Refresh system-prompt.txt from services/cairn/summarizer/prompt.go.
# Run this whenever the SystemPrompt const in source changes, so the eval
# stays apples-to-apples with what production sends.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
SRC="$REPO_ROOT/services/cairn/summarizer/prompt.go"
OUT="$SCRIPT_DIR/system-prompt.txt"

if [ ! -f "$SRC" ]; then
    echo "ERROR: prompt source not found at $SRC" >&2
    exit 1
fi

# Extract everything between the opening backtick after "SystemPrompt = `"
# and the closing backtick on its own line.
awk '
    /^const SystemPrompt = `/ {
        in_prompt = 1
        line = $0
        sub(/^const SystemPrompt = `/, "", line)
        # If the closing backtick is on the opening line (one-liner), strip + exit
        if (line ~ /`/) { sub(/`.*$/, "", line); print line; exit }
        print line
        next
    }
    in_prompt && /`/ {
        # Strip the trailing backtick (and anything after it on that line)
        sub(/`.*$/, "")
        print
        exit
    }
    in_prompt { print }
' "$SRC" > "$OUT"

echo "Refreshed $OUT from $SRC"
echo "  $(wc -l <"$OUT") lines, $(wc -c <"$OUT") bytes"
