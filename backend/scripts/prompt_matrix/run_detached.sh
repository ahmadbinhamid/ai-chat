#!/bin/bash
# Detached prompt-matrix runner (survives parent shell exit).
set -euo pipefail
export PATH="/usr/local/bin:/opt/homebrew/bin:/usr/bin:/bin:$PATH"
cd /Applications/Projects/office/ai-chat/backend
export AI_CHAT_LOG="${AI_CHAT_LOG:-/tmp/ai-chat-e2e-bhd.log}"
export PROMPT_MATRIX_OUT="${PROMPT_MATRIX_OUT:-/tmp/ai_builder_prompt_matrix}"
export CHAT_ID="${CHAT_ID:-4d06348f-56b3-4441-9842-1c2440d9b186}"
export PROMPT_MATRIX_RESUME="${PROMPT_MATRIX_RESUME:-1}"
mkdir -p "$PROMPT_MATRIX_OUT"
exec python3 scripts/prompt_matrix/run_matrix.py >>"$PROMPT_MATRIX_OUT/runner.log" 2>&1
