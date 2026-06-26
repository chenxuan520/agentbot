#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"
WEB_DIR="${ROOT_DIR}/web"
RUN_DIR="${ROOT_DIR}/data/run"
PID_FILE="${RUN_DIR}/web.pid"
LOG_FILE="${RUN_DIR}/web.log"
HOST="127.0.0.1"
PORT="4173"

mkdir -p "${RUN_DIR}"

pid_is_running() {
  local pid="$1"
  kill -0 "${pid}" 2>/dev/null
}

process_cwd() {
  local pid="$1"
  readlink -f "/proc/${pid}/cwd" 2>/dev/null || true
}

pid_matches_web() {
  local pid="$1"
  local cmd
  cmd="$(ps -p "${pid}" -o command= 2>/dev/null || true)"
  if [[ -z "${cmd}" ]]; then
    return 1
  fi
  if [[ "${cmd}" == *"node ${WEB_DIR}/node_modules/.bin/vite --host ${HOST}"* ]]; then
    return 0
  fi
  if [[ "${cmd}" == *"vite --host ${HOST}"* ]] && [[ "$(process_cwd "${pid}")" == "${WEB_DIR}" ]]; then
    return 0
  fi
  if [[ "${cmd}" == *"npm run dev -- --host ${HOST}"* ]] && [[ "$(process_cwd "${pid}")" == "${WEB_DIR}" ]]; then
    return 0
  fi
  return 1
}

stop_pid() {
  local pid="$1"
  if ! pid_is_running "${pid}"; then
    return 0
  fi
  if ! pid_matches_web "${pid}"; then
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
    if pid_matches_web "${pid}"; then
      stop_pid "${pid}"
    fi
  done
}

cleanup_existing

cd "${WEB_DIR}"
nohup npm run dev -- --host "${HOST}" >>"${LOG_FILE}" 2>&1 &
pid="$!"
printf '%s\n' "${pid}" > "${PID_FILE}"

for ((i = 0; i < 20; i++)); do
  if curl --silent --fail "http://${HOST}:${PORT}" >/dev/null; then
    printf 'web restarted\n'
    printf 'pid: %s\n' "${pid}"
    printf 'log: %s\n' "${LOG_FILE}"
    exit 0
  fi
  sleep 0.5
done

printf 'web restart failed\n' >&2
printf 'log: %s\n' "${LOG_FILE}" >&2
exit 1
