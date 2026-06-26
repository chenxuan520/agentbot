#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"
RUN_DIR="${ROOT_DIR}/data/run"
AGENT_PID_FILE="${RUN_DIR}/agent-bot.pid"
PID_FILE="${RUN_DIR}/watch-agent-bot.pid"
LOG_FILE="${RUN_DIR}/watch-agent-bot.log"
CHECK_INTERVAL_SECONDS="${CHECK_INTERVAL_SECONDS:-10}"
HEALTH_TIMEOUT_SECONDS="${HEALTH_TIMEOUT_SECONDS:-5}"
FAILURES_BEFORE_RESTART="${FAILURES_BEFORE_RESTART:-3}"
STARTUP_GRACE_SECONDS="${STARTUP_GRACE_SECONDS:-20}"
RESTART_SCRIPT="${ROOT_DIR}/scripts/restart-agent-bot.sh"
HEALTH_LAST_DETAIL=""

default_health_url() {
  python3 - "${ROOT_DIR}" <<'PY'
import json
import os
import pathlib
import sys

root = pathlib.Path(sys.argv[1])
config_path = os.environ.get("AGENT_BOT_CONFIG")
if config_path:
    path = pathlib.Path(config_path)
    if not path.is_absolute():
        path = root / path
else:
    path = root / "agent-bot.json"

host = "127.0.0.1"
port = 8080
try:
    data = json.loads(path.read_text())
except (FileNotFoundError, json.JSONDecodeError):
    data = {}

server = data.get("server") or {}
if server.get("host"):
    host = server["host"]
if isinstance(server.get("port"), int) and server["port"] > 0:
    port = server["port"]
if host in ("", "0.0.0.0", "::"):
    host = "127.0.0.1"

print(f"http://{host}:{port}/health")
PY
}

default_alert_webhook_url() {
  python3 - "${ROOT_DIR}" <<'PY'
import json
import os
import pathlib
import sys

root = pathlib.Path(sys.argv[1])
config_path = os.environ.get("AGENT_BOT_CONFIG")
if config_path:
    path = pathlib.Path(config_path)
    if not path.is_absolute():
        path = root / path
else:
    path = root / "agent-bot.json"

try:
    data = json.loads(path.read_text())
except (FileNotFoundError, json.JSONDecodeError):
    data = {}

providers = data.get("providers") or {}
feishu = providers.get("feishu") or {}
value = feishu.get("failureWebhookURL")
if isinstance(value, str):
    print(value.strip())
PY
}

HEALTH_URL="${HEALTH_URL:-}"
ALERT_WEBHOOK_URL="${ALERT_WEBHOOK_URL:-$(default_alert_webhook_url)}"

mkdir -p "${RUN_DIR}"

pid_is_running() {
  local pid="$1"
  kill -0 "${pid}" 2>/dev/null
}

pid_matches_watchdog() {
  local pid="$1"
  local cmd
  cmd="$(ps -p "${pid}" -o command= 2>/dev/null || true)"
  if [[ -z "${cmd}" ]]; then
    return 1
  fi
  [[ "${cmd}" == *"${ROOT_DIR}/scripts/watch-agent-bot.sh"* ]]
}

stop_pid() {
  local pid="$1"
  if ! pid_is_running "${pid}"; then
    return 0
  fi
  if ! pid_matches_watchdog "${pid}"; then
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
    if [[ "${cmd}" == *"${ROOT_DIR}/scripts/watch-agent-bot.sh"* ]]; then
      stop_pid "${pid}"
    fi
  done
}

health_ok() {
  local detail
  local status
  if detail="$(curl --fail --silent --show-error --output /dev/null --write-out 'http_code=%{http_code} total=%{time_total}' --max-time "${HEALTH_TIMEOUT_SECONDS}" "$1" 2>&1)"; then
    HEALTH_LAST_DETAIL="${detail}"
    return 0
  else
    status="$?"
  fi

  detail="${detail//$'\n'/ | }"
  HEALTH_LAST_DETAIL="curl_exit=${status} ${detail}"
  return 1
}

current_health_url() {
  if [[ -n "${HEALTH_URL}" ]]; then
    printf '%s\n' "${HEALTH_URL}"
    return 0
  fi
  default_health_url
}

current_agent_pid() {
  if [[ ! -f "${AGENT_PID_FILE}" ]]; then
    return 1
  fi
  tr -d '[:space:]' < "${AGENT_PID_FILE}"
}

current_agent_uptime_seconds() {
  local pid="$1"
  if [[ -z "${pid}" ]]; then
    return 1
  fi
  if ! pid_is_running "${pid}"; then
    return 1
  fi

  local age
  age="$(ps -p "${pid}" -o etimes= 2>/dev/null | tr -d '[:space:]')"
  if [[ -z "${age}" ]]; then
    return 1
  fi
  printf '%s\n' "${age}"
}

truncate_detail() {
  local detail="$1"
  if (( ${#detail} > 180 )); then
    detail="${detail:0:177}..."
  fi
  printf '%s\n' "${detail}"
}

build_alert_text() {
  local health_url="$1"
  local failures="$2"
  local detail="$3"
  printf 'agent-bot watchdog health check failed | health=%s | failures=%s | host=%s | detail=%s | action=restart | time=%s' \
    "${health_url}" "${failures}" "$(hostname)" "$(truncate_detail "${detail}")" "$(date -Is)"
}

notify_failure() {
  local health_url="$1"
  local failures="$2"
  local detail="$3"
  if [[ -z "${ALERT_WEBHOOK_URL}" ]]; then
    return 0
  fi

  local payload
  payload="$(python3 - "$(build_alert_text "${health_url}" "${failures}" "${detail}")" <<'PY'
import json
import sys

text = sys.argv[1]
print(json.dumps({"msg_type": "text", "content": {"text": text}}, ensure_ascii=False))
PY
)"

  if ! curl --silent --show-error --fail --max-time 5 \
    -H 'Content-Type: application/json; charset=utf-8' \
    -d "${payload}" \
    "${ALERT_WEBHOOK_URL}" >/dev/null; then
    printf '[%s] agent-bot failure webhook send failed\n' "$(date -Is)" >&2
  fi
}

run_loop() {
  local consecutive_failures=0
  local startup_grace_pid=""

  while true; do
    local health_url
    local agent_pid=""
    local agent_age=""
    health_url="$(current_health_url)"

    if agent_pid="$(current_agent_pid 2>/dev/null)" && agent_age="$(current_agent_uptime_seconds "${agent_pid}")"; then
      if (( agent_age < STARTUP_GRACE_SECONDS )); then
        if [[ "${startup_grace_pid}" != "${agent_pid}" ]]; then
          printf '[%s] skipping health check during startup grace: pid=%s age=%ss threshold=%ss\n' "$(date -Is)" "${agent_pid}" "${agent_age}" "${STARTUP_GRACE_SECONDS}"
          startup_grace_pid="${agent_pid}"
        fi
        consecutive_failures=0
        sleep "${CHECK_INTERVAL_SECONDS}"
        continue
      fi
    fi

    startup_grace_pid=""

    if ! health_ok "${health_url}"; then
      consecutive_failures=$((consecutive_failures + 1))
      printf '[%s] health check failed (%d/%d) on %s: %s\n' "$(date -Is)" "${consecutive_failures}" "${FAILURES_BEFORE_RESTART}" "${health_url}" "${HEALTH_LAST_DETAIL}"
      if (( consecutive_failures < FAILURES_BEFORE_RESTART )); then
        sleep "${CHECK_INTERVAL_SECONDS}"
        continue
      fi

      notify_failure "${health_url}" "${consecutive_failures}" "${HEALTH_LAST_DETAIL}"
      printf '[%s] agent-bot unhealthy on %s after %d consecutive failures, restarting\n' "$(date -Is)" "${health_url}" "${consecutive_failures}"
      "${RESTART_SCRIPT}"
      consecutive_failures=0
      sleep "${CHECK_INTERVAL_SECONDS}"
      continue
    fi

    if (( consecutive_failures > 0 )); then
      printf '[%s] health check recovered after %d consecutive failures on %s\n' "$(date -Is)" "${consecutive_failures}" "${health_url}"
    fi
    consecutive_failures=0
    sleep "${CHECK_INTERVAL_SECONDS}"
  done
}

start() {
  cleanup_existing
  nohup bash "${ROOT_DIR}/scripts/watch-agent-bot.sh" --loop >>"${LOG_FILE}" 2>&1 &
  local pid="$!"
  printf '%s\n' "${pid}" > "${PID_FILE}"
  printf 'watch-agent-bot started\n'
  printf 'pid: %s\n' "${pid}"
  printf 'health: %s\n' "$(current_health_url)"
  printf 'interval: %ss\n' "${CHECK_INTERVAL_SECONDS}"
  printf 'health timeout: %ss\n' "${HEALTH_TIMEOUT_SECONDS}"
  printf 'restart threshold: %s failures\n' "${FAILURES_BEFORE_RESTART}"
  printf 'startup grace: %ss\n' "${STARTUP_GRACE_SECONDS}"
  if [[ -n "${ALERT_WEBHOOK_URL}" ]]; then
    printf 'alert webhook: configured\n'
  fi
  printf 'log: %s\n' "${LOG_FILE}"
}

case "${1:-start}" in
  start)
    start
    ;;
  --loop)
    run_loop
    ;;
  stop)
    cleanup_existing
    ;;
  *)
    printf 'usage: %s [start|stop]\n' "$0" >&2
    exit 1
    ;;
esac
