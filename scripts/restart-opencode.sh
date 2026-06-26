#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"
RUN_DIR="${ROOT_DIR}/data/run"
PID_FILE="${RUN_DIR}/opencode.pid"
LOG_FILE="${RUN_DIR}/opencode.log"
HOST="127.0.0.1"
PORT="4096"

mkdir -p "${RUN_DIR}"

pid_is_running() {
  local pid="$1"
  kill -0 "${pid}" 2>/dev/null
}

pid_matches_opencode() {
  local pid="$1"
  local cmd
  cmd="$(ps -p "${pid}" -o command= 2>/dev/null || true)"
  if [[ -z "${cmd}" ]]; then
    return 1
  fi
  [[ "${cmd}" == *"opencode serve"* ]] && [[ "${cmd}" == *"--port ${PORT}"* ]]
}

stop_pid() {
  local pid="$1"
  if ! pid_is_running "${pid}"; then
    return 0
  fi
  if ! pid_matches_opencode "${pid}"; then
    return 0
  fi

  kill "${pid}" 2>/dev/null || true
  local i
  for ((i = 0; i < 20; i++)); do
    if ! pid_is_running "${pid}"; then
      return 0
    fi
    sleep 0.5
  done

  kill -9 "${pid}" 2>/dev/null || true
}

cleanup_existing() {
  if [[ -f "${PID_FILE}" ]]; then
    local pid
    pid="$(tr -d '[:space:]' < "${PID_FILE}")"
    if [[ -n "${pid}" ]]; then
      stop_pid "${pid}"
    fi
    rm -f "${PID_FILE}"
  fi

  ps -axo pid=,command= | while read -r pid cmd; do
    if [[ -z "${pid}" || -z "${cmd}" ]]; then
      continue
    fi
    if [[ "${cmd}" == *"opencode serve"* ]] && [[ "${cmd}" == *"--port ${PORT}"* ]]; then
      stop_pid "${pid}"
    fi
  done
}

cleanup_existing

cd "${ROOT_DIR}"
nohup opencode serve --hostname "${HOST}" --port "${PORT}" >>"${LOG_FILE}" 2>&1 &
pid="$!"
printf '%s\n' "${pid}" > "${PID_FILE}"

for ((i = 0; i < 20; i++)); do
  if python3 - <<'PY'
import json
import urllib.request
with urllib.request.urlopen('http://127.0.0.1:4096/global/health', timeout=1) as resp:
    if resp.status != 200:
        raise SystemExit(1)
    payload = json.loads(resp.read().decode('utf-8'))
    if payload.get('healthy') is not True:
        raise SystemExit(1)
PY
  then
    printf 'opencode restarted\n'
    printf 'pid: %s\n' "${pid}"
    printf 'log: %s\n' "${LOG_FILE}"
    exit 0
  fi
  sleep 0.5
done

printf 'opencode restart failed\n' >&2
printf 'log: %s\n' "${LOG_FILE}" >&2
exit 1
