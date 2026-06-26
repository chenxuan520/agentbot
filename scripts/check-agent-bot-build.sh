#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"
RUN_DIR="${ROOT_DIR}/data/run"
BIN_PATH="${RUN_DIR}/agent-bot-daemon"
PID_FILE="${RUN_DIR}/agent-bot.pid"

source_head="$(git -C "${ROOT_DIR}" rev-parse HEAD)"
source_short="$(git -C "${ROOT_DIR}" rev-parse --short HEAD)"

source_dirty="false"
if [[ -n "$(git -C "${ROOT_DIR}" status --porcelain --untracked-files=all)" ]]; then
  source_dirty="true"
fi

if [[ ! -f "${BIN_PATH}" ]]; then
  printf 'status: drift_detected\n'
  printf 'reason: binary_missing\n'
  printf 'binary_path: %s\n' "${BIN_PATH}"
  exit 1
fi

binary_revision=""
binary_time=""
binary_modified=""
while IFS= read -r line; do
  if [[ "${line}" == $'\t'build$'\tvcs.revision='* ]]; then
    binary_revision="${line#*$'\tvcs.revision='}"
  elif [[ "${line}" == $'\t'build$'\tvcs.time='* ]]; then
    binary_time="${line#*$'\tvcs.time='}"
  elif [[ "${line}" == $'\t'build$'\tvcs.modified='* ]]; then
    binary_modified="${line#*$'\tvcs.modified='}"
  fi
done < <(go version -m "${BIN_PATH}")

binary_mtime="$(stat -c '%y' "${BIN_PATH}")"
running_pid="-"
running_started_at="-"
running_exe="-"

if [[ -f "${PID_FILE}" ]]; then
  pid="$(tr -d '[:space:]' < "${PID_FILE}")"
  if [[ -n "${pid}" ]] && kill -0 "${pid}" 2>/dev/null; then
    running_pid="${pid}"
    running_started_at="$(ps -p "${pid}" -o lstart= 2>/dev/null | sed 's/^ *//')"
    running_exe="$(readlink "/proc/${pid}/exe" 2>/dev/null || true)"
  fi
fi

reasons=()
if [[ -z "${binary_revision}" ]]; then
  reasons+=("binary_vcs_revision_missing")
fi
if [[ -n "${binary_revision}" ]] && [[ "${binary_revision}" != "${source_head}" ]]; then
  reasons+=("binary_revision_differs_from_git_head")
fi
if [[ -n "${binary_modified}" ]] && [[ "${binary_modified}" != "${source_dirty}" ]]; then
  reasons+=("binary_dirty_flag_differs_from_worktree")
fi
if [[ "${running_pid}" == "-" ]]; then
  reasons+=("running_pid_missing_or_stale")
fi
if [[ "${running_exe}" == *" (deleted)" ]]; then
  reasons+=("running_process_uses_deleted_binary")
fi
if [[ "${running_exe}" != "-" ]] && [[ "${running_exe%% (deleted)}" != "${BIN_PATH}" ]]; then
  reasons+=("running_process_not_using_expected_binary_path")
fi

status="likely_in_sync"
exit_code=0
if (( ${#reasons[@]} > 0 )); then
  status="drift_detected"
  exit_code=1
fi

printf 'status: %s\n' "${status}"
printf 'source_head: %s (%s)\n' "${source_short}" "${source_head}"
printf 'source_dirty: %s\n' "${source_dirty}"
printf 'binary_path: %s\n' "${BIN_PATH}"
printf 'binary_mtime: %s\n' "${binary_mtime}"
printf 'binary_vcs_revision: %s\n' "${binary_revision:-<missing>}"
printf 'binary_vcs_time: %s\n' "${binary_time:-<missing>}"
printf 'binary_vcs_modified: %s\n' "${binary_modified:-<missing>}"
printf 'running_pid: %s\n' "${running_pid}"
printf 'running_started_at: %s\n' "${running_started_at}"
printf 'running_exe: %s\n' "${running_exe}"

if (( ${#reasons[@]} > 0 )); then
  printf 'reasons:\n'
  for reason in "${reasons[@]}"; do
    printf '  - %s\n' "${reason}"
  done
fi

exit "${exit_code}"
