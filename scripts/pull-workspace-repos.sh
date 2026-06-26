#!/usr/bin/env bash

set -u

workspace_root="${1:-}"

log() {
  printf '[%s] %s\n' "$(date '+%Y-%m-%d %H:%M:%S')" "$*"
}

if [[ -z "$workspace_root" ]]; then
  log "usage: $0 <workspace-root>"
  exit 2
fi

if [[ ! -d "$workspace_root" ]]; then
  log "workspace not found: $workspace_root"
  exit 1
fi

shopt -s nullglob

for entry in "$workspace_root"/*; do
  name="$(basename "$entry")"

  case "$name" in
    AGENTS.md|config.yaml|opencode.json)
      continue
      ;;
  esac

  if [[ ! -L "$entry" ]]; then
    continue
  fi

  if [[ ! -e "$entry" ]]; then
    log "skip $name: broken symlink"
    continue
  fi

  resolved="$(readlink -f "$entry")"
  if [[ -z "$resolved" || ! -d "$resolved" ]]; then
    log "skip $name: target is not a directory"
    continue
  fi

  if [[ ! -d "$resolved/.git" && ! -f "$resolved/.git" ]]; then
    log "skip $name: not a git repo"
    continue
  fi

  branch="$(git -C "$resolved" rev-parse --abbrev-ref HEAD 2>/dev/null || printf 'unknown')"
  log "pull $name branch=$branch path=$resolved"
  if git -C "$resolved" pull --ff-only; then
    log "ok $name"
  else
    status=$?
    log "fail $name exit=$status"
  fi
done
