#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
PLAYWRIGHT_VERSION="${PLAYWRIGHT_VERSION:-1.55.0}"
SERVER_LOG="$(mktemp -t upstream-monitor-ui-server.XXXXXX)"
SERVER_PID=""

cleanup() {
  if [[ -n "${SERVER_PID}" ]] && kill -0 "${SERVER_PID}" 2>/dev/null; then
    kill "${SERVER_PID}" 2>/dev/null || true
    wait "${SERVER_PID}" 2>/dev/null || true
  fi
  rm -f "${SERVER_LOG}"
}
trap cleanup EXIT INT TERM

find_playwright_cli() {
  if [[ -n "${PLAYWRIGHT_CLI:-}" ]]; then
    printf '%s\n' "${PLAYWRIGHT_CLI}"
    return 0
  fi

  local candidate
  while IFS= read -r candidate; do
    if [[ -f "${candidate}" ]] && node "${candidate}" --version >/dev/null 2>&1; then
      printf '%s\n' "${candidate}"
      return 0
    fi
  done < <(
    find "${HOME}/.npm/_npx" -type f -path '*/node_modules/@playwright/test/cli.js' 2>/dev/null \
      | sort -r
  )

  return 1
}

resolve_playwright() {
  local cli node_modules
  if ! cli="$(find_playwright_cli)"; then
    node_modules="$(
      npm exec --yes --package="@playwright/test@${PLAYWRIGHT_VERSION}" -- \
        sh -c 'cd "$(dirname "$(command -v playwright)")/.." && pwd'
    )"
    cli="${node_modules}/@playwright/test/cli.js"
  else
    node_modules="$(cd "$(dirname "${cli}")/../.." && pwd)"
  fi

  if [[ ! -f "${cli}" ]]; then
    printf 'Playwright CLI not found: %s\n' "${cli}" >&2
    exit 1
  fi

  printf '%s\n' "${cli}"
}

if [[ -n "${UI_TEST_PORT:-}" ]]; then
  PORT="${UI_TEST_PORT}"
else
  PORT="$(python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1", 0)); print(s.getsockname()[1]); s.close()')"
fi

python3 -m http.server "${PORT}" --bind 127.0.0.1 --directory "${ROOT_DIR}" \
  >"${SERVER_LOG}" 2>&1 &
SERVER_PID=$!

python3 - "${PORT}" <<'PY'
import socket
import sys
import time

port = int(sys.argv[1])
deadline = time.time() + 10
while time.time() < deadline:
    with socket.socket() as client:
        client.settimeout(0.25)
        try:
            client.connect(("127.0.0.1", port))
            break
        except OSError:
            time.sleep(0.1)
else:
    raise SystemExit(f"UI test server did not start on port {port}")
PY

PLAYWRIGHT_CLI_PATH="$(resolve_playwright)"
PLAYWRIGHT_NODE_MODULES="$(cd "$(dirname "${PLAYWRIGHT_CLI_PATH}")/../.." && pwd)"

UI_BASE_URL="http://127.0.0.1:${PORT}" \
NODE_PATH="${PLAYWRIGHT_NODE_MODULES}${NODE_PATH:+:${NODE_PATH}}" \
  node "${PLAYWRIGHT_CLI_PATH}" test "${ROOT_DIR}/ui-smoke.spec.js" --reporter=line "$@"
