#!/usr/bin/env bash
# Install the `mrstack` Agent Skill into an agent's skills directory.
#
# Usage:
#   ./skills/install.sh                 # auto-detect client
#   ./skills/install.sh --client cursor # force a client
#   ./skills/install.sh --list          # show supported clients and exit
#
# Supported clients: cursor, claude, codex, opencode, goose, agents
# (Run remotely: curl -sSL https://raw.githubusercontent.com/nkaewam/mrstack/main/skills/install.sh | bash)
set -euo pipefail

SKILL_NAME="mrstack"
SUPPORTED="cursor claude codex opencode goose agents"

client_for_flag() {
  case "$1" in
    cursor)   printf '%s\n' "$HOME/.cursor/skills" ;;
    claude)    printf '%s\n' "$HOME/.claude/skills" ;;
    codex)     printf '%s\n' "$HOME/.codex/skills" ;;
    opencode)  printf '%s\n' "$HOME/.local/share/opencode/skill" ;;
    goose)     printf '%s\n' "$HOME/.goose/skills" ;;
    agents)    printf '%s\n' "$HOME/.agents/skills" ;;
    *) return 1 ;;
  esac
}

detect_client() {
  for c in $SUPPORTED; do
    dir="$(client_for_flag "$c" 2>/dev/null || true)"
    if [ -d "$(dirname "${dir}")" ]; then return 0; fi
  done
  # default to Cursor (most common, creates on demand)
  echo "cursor"
}

print_list() {
  echo "Supported clients (and their install directories):"
  for c in $SUPPORTED; do
    printf '  %-9s %s\n' "$c" "$(client_for_flag "$c")"
  done
}

CLIENT=""
LIST=0
while [ $# -gt 0 ]; do
  case "$1" in
    --client) CLIENT="$2"; shift 2 ;;
    --list)   LIST=1; shift ;;
    -h|--help)
      sed -n '2,9p' "$0"; exit 0 ;;
    *) echo "unknown option: $1" >&2; exit 2 ;;
  esac
done

if [ "$LIST" -eq 1 ]; then print_list; exit 0; fi

if [ -n "$CLIENT" ]; then
  if ! client_for_flag "$CLIENT" >/dev/null 2>&1; then
    echo "error: unknown client '$CLIENT'" >&2
    echo "supported: $SUPPORTED" >&2
    exit 2
  fi
else
  CLIENT="$(detect_client)"
fi

DEST="$(client_for_flag "$CLIENT")"

# Resolve the skill source relative to this script (works for local + piped runs).
SCRIPT_DIR=""
if [ -n "${BASH_SOURCE[0]:-}" ] && [ -f "${BASH_SOURCE[0]}" ]; then
  SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
fi

SRC_LOCAL="${SCRIPT_DIR}/${SKILL_NAME}"
SRC_REMOTE="https://raw.githubusercontent.com/nkaewam/mrstack/main/skills/${SKILL_NAME}"

mkdir -p "${DEST}/${SKILL_NAME}"

fetch() { # fetch <remote-path> > stdout
  if command -v curl >/dev/null 2>&1; then
    curl -fsSL "${SRC_REMOTE}/$1"
  else
    wget -qO- "${SRC_REMOTE}/$1"
  fi
}

if [ -d "$SRC_LOCAL" ]; then
  cp -R "${SRC_LOCAL}/." "${DEST}/${SKILL_NAME}/"
else
  # Piped/remote run with no local source: fetch each file from GitHub raw.
  fetch "SKILL.md" > "${DEST}/${SKILL_NAME}/SKILL.md"
  mkdir -p "${DEST}/${SKILL_NAME}/references"
  fetch "references/REFERENCE.md" > "${DEST}/${SKILL_NAME}/references/REFERENCE.md"
fi

echo "Installed ${SKILL_NAME} skill for ${CLIENT}:"
echo "  ${DEST}/${SKILL_NAME}"
echo
echo "Restart your agent (or open a new session) to load it."
