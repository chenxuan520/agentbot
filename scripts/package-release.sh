#!/usr/bin/env bash
set -euo pipefail

if [[ $# -ne 4 ]]; then
  printf 'usage: bash scripts/package-release.sh <version> <goos> <goarch> <output-dir>\n' >&2
  exit 1
fi

VERSION="$1"
TARGET_GOOS="$2"
TARGET_GOARCH="$3"
OUTPUT_DIR="$4"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"
WEB_DIST_DIR="${ROOT_DIR}/web/dist"

if [[ ! -d "${WEB_DIST_DIR}" ]]; then
  printf 'web dist is missing: run npm run build in web/ first\n' >&2
  exit 1
fi

mkdir -p "${OUTPUT_DIR}"
OUTPUT_DIR="$(cd "${OUTPUT_DIR}" && pwd)"

BINARY_NAME="agent-bot"
ARCHIVE_EXT="tar.gz"
if [[ "${TARGET_GOOS}" == "windows" ]]; then
  BINARY_NAME="agent-bot.exe"
  ARCHIVE_EXT="zip"
fi

PACKAGE_NAME="agent-bot_${VERSION}_${TARGET_GOOS}_${TARGET_GOARCH}"
STAGE_DIR="$(mktemp -d "${TMPDIR:-/tmp}/agent-bot-release.XXXXXX")"
PACKAGE_DIR="${STAGE_DIR}/${PACKAGE_NAME}"
RELEASE_SCRIPTS=(
  "pull-workspace-repos.sh"
  "restart-opencode.sh"
  "watch-opencode.sh"
)

cleanup() {
  rm -rf "${STAGE_DIR}"
}
trap cleanup EXIT

mkdir -p \
  "${PACKAGE_DIR}/agents/repos" \
  "${PACKAGE_DIR}/data/chats" \
  "${PACKAGE_DIR}/data/run" \
  "${PACKAGE_DIR}/web/dist"

GOOS="${TARGET_GOOS}" GOARCH="${TARGET_GOARCH}" CGO_ENABLED=0 \
  go build -trimpath -o "${PACKAGE_DIR}/${BINARY_NAME}" "${ROOT_DIR}/cmd/agent-bot"

cp "${ROOT_DIR}/README.md" "${ROOT_DIR}/agent-bot.example.json" "${PACKAGE_DIR}/"
rsync -a --exclude '__pycache__/' --exclude '*.pyc' "${ROOT_DIR}/docs/" "${PACKAGE_DIR}/docs/"
rsync -a --exclude '__pycache__/' --exclude '*.pyc' "${ROOT_DIR}/templates/" "${PACKAGE_DIR}/templates/"
rsync -a --exclude 'repos/' "${ROOT_DIR}/agents/" "${PACKAGE_DIR}/agents/"
rsync -a "${WEB_DIST_DIR}/" "${PACKAGE_DIR}/web/dist/"

mkdir -p "${PACKAGE_DIR}/scripts"
for script_name in "${RELEASE_SCRIPTS[@]}"; do
  cp "${ROOT_DIR}/scripts/${script_name}" "${PACKAGE_DIR}/scripts/${script_name}"
done

touch "${PACKAGE_DIR}/agents/repos/.gitkeep"
touch "${PACKAGE_DIR}/data/chats/.gitkeep"
touch "${PACKAGE_DIR}/data/run/.gitkeep"

if [[ "${ARCHIVE_EXT}" == "zip" ]]; then
  ARCHIVE_PATH="${OUTPUT_DIR}/${PACKAGE_NAME}.zip"
  (
    cd "${STAGE_DIR}"
    zip -qr "${ARCHIVE_PATH}" "${PACKAGE_NAME}"
  )
else
  ARCHIVE_PATH="${OUTPUT_DIR}/${PACKAGE_NAME}.tar.gz"
  (
    cd "${STAGE_DIR}"
    tar -czf "${ARCHIVE_PATH}" "${PACKAGE_NAME}"
  )
fi

printf 'created: %s\n' "${ARCHIVE_PATH}"
