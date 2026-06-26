#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"
RUN_DIR="${ROOT_DIR}/data/run"
BIN_PATH="${RUN_DIR}/agent-bot-daemon"
PID_FILE="${RUN_DIR}/agent-bot.pid"
LOG_FILE="${RUN_DIR}/agent-bot.log"

mkdir -p "${RUN_DIR}"

process_cwd() {
  local pid="$1"
  readlink -f "/proc/${pid}/cwd" 2>/dev/null || true
}

pid_is_running() {
  local pid="$1"
  kill -0 "${pid}" 2>/dev/null
}

pid_matches_agent_bot() {
  local pid="$1"
  local cmd
  cmd="$(ps -p "${pid}" -o command= 2>/dev/null || true)"
  if [[ -z "${cmd}" ]]; then
    return 1
  fi
  if [[ "${cmd}" == *"${BIN_PATH} run"* ]]; then
    return 0
  fi
  if [[ "${cmd}" == *"/agent-bot run"* ]] && [[ "$(process_cwd "${pid}")" == "${ROOT_DIR}" ]]; then
    return 0
  fi
  if [[ "${cmd}" == *"go run ${ROOT_DIR}/cmd/agent-bot run"* ]]; then
    return 0
  fi
  if [[ "${cmd}" == *"go run ./cmd/agent-bot run"* ]]; then
    [[ "$(process_cwd "${pid}")" == "${ROOT_DIR}" ]]
    return
  fi
  return 1
}

stop_pid() {
  local pid="$1"
  if ! pid_is_running "${pid}"; then
    return 0
  fi
  if ! pid_matches_agent_bot "${pid}"; then
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
    if pid_matches_agent_bot "${pid}"; then
      stop_pid "${pid}"
    fi
  done
}

cd "${ROOT_DIR}"
go build -o "${BIN_PATH}" "${ROOT_DIR}/cmd/agent-bot"
cleanup_existing

nohup "${BIN_PATH}" run "$@" >>"${LOG_FILE}" 2>&1 &
pid="$!"
printf '%s\n' "${pid}" > "${PID_FILE}"

printf 'agent-bot restarted\n'
printf 'pid: %s\n' "${pid}"
printf 'log: %s\n' "${LOG_FILE}"
