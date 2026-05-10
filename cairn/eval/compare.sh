#!/usr/bin/env bash
# Apples-to-apples eval of the Cairn simplifier prompt against multiple engines:
#   - claude-code  (subprocess, current production)
#   - deepseek     (DeepSeek API, OpenAI-compat, hosted)
#   - ollama       (local model on <server-host> — typically ZAYA1-8B or similar)
#
# Usage:  ./compare.sh <pr-content-file>
# Where pr-content-file is shaped like Cairn's BuildUserPrompt output
# (title, description, branch, commits, files, diff). See samples/.
#
# Configure via env (or edit defaults below):

OLLAMA_URL="${OLLAMA_URL:-http://100.70.156.32:11434}"   # <server-host> tailnet IP
OLLAMA_MODEL="${OLLAMA_MODEL:-zaya1-8b}"                  # whatever tag landed

DEEPSEEK_URL="${DEEPSEEK_URL:-https://api.deepseek.com/v1}"
DEEPSEEK_MODEL="${DEEPSEEK_MODEL:-deepseek-chat}"        # or deepseek-reasoner
DEEPSEEK_API_KEY="${DEEPSEEK_API_KEY:-}"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SYSTEM_PROMPT_FILE="${SYSTEM_PROMPT_FILE:-$SCRIPT_DIR/system-prompt.txt}"
OUT_DIR="${OUT_DIR:-$SCRIPT_DIR/runs}"

set -euo pipefail

PR_FILE="${1:?provide a pr-content file (try samples/*.md)}"
PR_BODY="$(cat "$PR_FILE")"
SYS="$(cat "$SYSTEM_PROMPT_FILE")"

mkdir -p "$OUT_DIR"
BASENAME="$(basename "${PR_FILE%.*}")"
TIMESTAMP="$(date -u +%Y%m%dT%H%M%SZ)"

# ---- engines ----

run_claude_code() {
    local out="$OUT_DIR/${BASENAME}-${TIMESTAMP}-claude-code.txt"
    local start=$(date +%s)
    claude -p "$SYS

---

$PR_BODY" > "$out"
    local elapsed=$(($(date +%s) - start))
    echo "  saved → $out  (${elapsed}s)"
    head -10 "$out"
}

run_openai_compat() {
    local label="$1" url="$2" model="$3" key="$4" path_suffix="$5"
    local out="$OUT_DIR/${BASENAME}-${TIMESTAMP}-${label}.txt"
    local start=$(date +%s)
    local payload
    payload="$(jq -n --arg model "$model" --arg sys "$SYS" --arg user "$PR_BODY" '{
        model: $model,
        messages: [
            {role: "system", content: $sys},
            {role: "user",   content: $user}
        ],
        stream: false
    }')"
    local headers=(-H "Content-Type: application/json")
    if [ -n "$key" ]; then
        headers+=(-H "Authorization: Bearer $key")
    fi
    curl -s "${url}${path_suffix}" "${headers[@]}" -d "$payload" \
        | jq -r 'try .choices[0].message.content // .message.content // .' \
        > "$out"
    local elapsed=$(($(date +%s) - start))
    echo "  saved → $out  (${elapsed}s)"
    head -10 "$out"
}

# ---- runner ----

echo "=== claude-code ==="
if command -v claude >/dev/null && [ -z "${SKIP_CLAUDE:-}" ]; then
    run_claude_code
else
    echo "  (skipped — set SKIP_CLAUDE=1 to silence, or install claude)"
fi
echo

if [ -z "${SKIP_DEEPSEEK:-}" ]; then
    if [ -z "$DEEPSEEK_API_KEY" ]; then
        echo "=== deepseek-api === (skipped — set DEEPSEEK_API_KEY)"
    else
        echo "=== deepseek-api ($DEEPSEEK_MODEL) ==="
        run_openai_compat "deepseek-${DEEPSEEK_MODEL}" "$DEEPSEEK_URL" "$DEEPSEEK_MODEL" "$DEEPSEEK_API_KEY" "/chat/completions"
    fi
    echo
fi

if [ -z "${SKIP_OLLAMA:-}" ]; then
    echo "=== ollama ($OLLAMA_MODEL) ==="
    run_openai_compat "ollama-${OLLAMA_MODEL}" "$OLLAMA_URL" "$OLLAMA_MODEL" "" "/api/chat"
    echo
fi

echo "=== run summary ==="
ls -la "$OUT_DIR/${BASENAME}-${TIMESTAMP}"* 2>/dev/null | awk '{print $5, $9}'
