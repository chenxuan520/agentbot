#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"
RUN_DIR="${ROOT_DIR}/data/run"
PID_FILE="${RUN_DIR}/watch-opencode.pid"
LOG_FILE="${RUN_DIR}/watch-opencode.log"
CHECK_INTERVAL_SECONDS="${CHECK_INTERVAL_SECONDS:-300}"
HEALTH_URL="${HEALTH_URL:-http://127.0.0.1:4096/global/health}"
RESTART_SCRIPT="${ROOT_DIR}/scripts/restart-opencode.sh"
ALERT_WEBHOOK_URL="${ALERT_WEBHOOK_URL:-}"
HEALTH_LAST_DETAIL=""

mkdir -p "${RUN_DIR}"

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

if [[ -z "${ALERT_WEBHOOK_URL}" ]]; then
  ALERT_WEBHOOK_URL="$(default_alert_webhook_url)"
fi

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
  [[ "${cmd}" == *"${ROOT_DIR}/scripts/watch-opencode.sh"* ]]
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
    if [[ "${cmd}" == *"${ROOT_DIR}/scripts/watch-opencode.sh"* ]]; then
      stop_pid "${pid}"
    fi
  done
}

health_ok() {
  local detail
  local status
  if detail="$(python3 - "$1" <<'PY'
import json
import sys
import urllib.error
import urllib.request

url = sys.argv[1]
try:
    with urllib.request.urlopen(url, timeout=5) as resp:
        body = resp.read().decode('utf-8', errors='replace')
        try:
            payload = json.loads(body)
        except json.JSONDecodeError as exc:
            print(f"http_code={resp.status} parse_error={exc.msg}")
            raise SystemExit(1)
        healthy = payload.get('healthy')
        print(f"http_code={resp.status} healthy={healthy}")
        raise SystemExit(0 if healthy is True else 1)
except urllib.error.HTTPError as exc:
    body = exc.read().decode('utf-8', errors='replace').strip()
    print(f"http_code={exc.code} detail={body}")
    raise SystemExit(1)
except Exception as exc:
    print(f"detail={type(exc).__name__}: {exc}")
    raise SystemExit(1)
PY
)"; then
    HEALTH_LAST_DETAIL="${detail}"
    return 0
  else
    status="$?"
  fi

  detail="${detail//$'\n'/ | }"
  HEALTH_LAST_DETAIL="curl_exit=${status} ${detail}"
  return 1
}

notify_failure() {
  local detail="$1"
  if [[ -z "${ALERT_WEBHOOK_URL}" ]]; then
    return 0
  fi

  local payload
  payload="$(python3 - "${HEALTH_URL}" "${detail}" <<'PY'
import json
import sys
from datetime import datetime, timezone

health_url = sys.argv[1]
detail = sys.argv[2]
text = (
    f"agent-bot opencode watchdog health check failed | "
    f"health={health_url} | detail={detail} | action=restart | "
    f"time={datetime.now(timezone.utc).astimezone().isoformat()}"
)
print(json.dumps({"msg_type": "text", "content": {"text": text}}, ensure_ascii=False))
PY
)"

  if ! curl --silent --show-error --fail --max-time 5 \
    -H 'Content-Type: application/json; charset=utf-8' \
    -d "${payload}" \
    "${ALERT_WEBHOOK_URL}" >/dev/null; then
    printf '[%s] opencode failure webhook send failed\n' "$(date -Is)" >&2
  fi
}

run_loop() {
  while true; do
    if ! health_ok "${HEALTH_URL}"; then
      printf '[%s] opencode unhealthy on %s: %s\n' "$(date -Is)" "${HEALTH_URL}" "${HEALTH_LAST_DETAIL}"
      notify_failure "${HEALTH_LAST_DETAIL}"
      printf '[%s] opencode unhealthy, restarting\n' "$(date -Is)"
      "${RESTART_SCRIPT}"
    fi
    sleep "${CHECK_INTERVAL_SECONDS}"
  done
}

start() {
  cleanup_existing
  nohup bash "${ROOT_DIR}/scripts/watch-opencode.sh" --loop >>"${LOG_FILE}" 2>&1 &
  local pid="$!"
  printf '%s\n' "${pid}" > "${PID_FILE}"
  printf 'watch-opencode started\n'
  printf 'pid: %s\n' "${pid}"
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
