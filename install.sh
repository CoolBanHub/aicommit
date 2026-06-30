#!/usr/bin/env sh
# aicommit — cross-platform installer (macOS / Linux)
#
# Quick start (public repo):
#   curl -fsSL https://raw.githubusercontent.com/CoolBanHub/aicommit/main/install.sh | sh
#
# For a private repo, export a token with `repo` read access first:
#   export GITHUB_TOKEN=ghp_xxx
#   curl -fsSL https://raw.githubusercontent.com/CoolBanHub/aicommit/main/install.sh | sh
#
# Pin a version or choose a destination:
#   sh install.sh --version v0.0.1 --dir ~/.local/bin

set -eu

OWNER="CoolBanHub"
REPO="aicommit"
VERSION="latest"
INSTALL_DIR=""

usage() {
  cat <<EOF
aicommit installer

Usage:
  install.sh [--version <tag>] [--dir <path>]

Environment:
  GITHUB_TOKEN   personal access token (required for private repos)

Options:
  -v, --version <tag>   release tag to install (default: latest)
  -d, --dir <path>      install directory (default: /usr/local/bin or ~/.local/bin)
  -h, --help            show this help
EOF
}

while [ $# -gt 0 ]; do
  case "$1" in
    -v|--version) VERSION="${2:-}"; shift 2 ;;
    -d|--dir)     INSTALL_DIR="${2:-}"; shift 2 ;;
    -h|--help)    usage; exit 0 ;;
    *) printf 'Unknown option: %s\n' "$1" >&2; usage >&2; exit 1 ;;
  esac
done

need() { command -v "$1" >/dev/null 2>&1 || { printf 'Missing required command: %s\n' "$1" >&2; exit 1; }; }
need uname
need curl
need chmod
need mkdir

# Optional auth token: required for private repos and a higher API rate limit.
GH_TOKEN="${GITHUB_TOKEN:-${GH_TOKEN:-}}"
AUTH_HEADER=""
if [ -n "$GH_TOKEN" ]; then
  AUTH_HEADER="Authorization: Bearer $GH_TOKEN"
fi

# curl wrapper that injects the auth header when a token is present.
#   fetch <output-file|-> <url>
fetch() {
  out="$1"; shift
  if [ -n "$AUTH_HEADER" ]; then
    curl -fsSL --retry 3 -H "$AUTH_HEADER" -o "$out" "$@"
  else
    curl -fsSL --retry 3 -o "$out" "$@"
  fi
}

# --- detect platform ---
OS=$(uname -s)
ARCH=$(uname -m)
case "$OS" in
  Darwin) OS=darwin ;;
  Linux)  OS=linux ;;
  MINGW*|MSYS*|CYGWIN*) OS=windows ;;
  *) printf 'Unsupported OS: %s\n' "$OS" >&2; exit 1 ;;
esac
case "$ARCH" in
  x86_64|amd64)  ARCH=amd64 ;;
  aarch64|arm64) ARCH=arm64 ;;
  i386|i686)     ARCH=386 ;;
  *) printf 'Unsupported architecture: %s\n' "$ARCH" >&2; exit 1 ;;
esac

if [ "$OS" = "windows" ]; then
  printf 'Windows detected. Use install.ps1, or download the .exe from:\n' >&2
  printf '  https://github.com/%s/%s/releases/latest\n' "$OWNER" "$REPO" >&2
  exit 1
fi

ASSET="aicommit-${OS}-${ARCH}"

# --- resolve the latest release via the GitHub API ---
if [ "$VERSION" = "latest" ]; then
  api_url="https://api.github.com/repos/${OWNER}/${REPO}/releases/latest"
  VERSION=$(fetch - "$api_url" | grep '"tag_name"' | head -n1 | sed -E 's/.*"tag_name"[[:space:]]*:[[:space:]]*"([^"]+)".*/\1/' || true)
  if [ -z "${VERSION:-}" ]; then
    printf 'Could not resolve the latest release.\n' >&2
    [ -z "$GH_TOKEN" ] && printf 'Hint: this may be a private repo — set GITHUB_TOKEN and retry.\n' >&2
    exit 1
  fi
fi

URL="https://github.com/${OWNER}/${REPO}/releases/download/${VERSION}/${ASSET}"

# --- choose an install directory ---
if [ -z "$INSTALL_DIR" ]; then
  if [ -d /usr/local/bin ] && [ -w /usr/local/bin ]; then
    INSTALL_DIR="/usr/local/bin"
  else
    INSTALL_DIR="${HOME}/.local/bin"
  fi
fi

# --- download ---
WORK=$(mktemp -d 2>/dev/null || mktemp -d -t aicommit)
trap 'rm -rf "$WORK"' EXIT INT TERM
TMPFILE="${WORK}/${ASSET}"

printf 'Downloading aicommit %s (%s/%s)...\n' "$VERSION" "$OS" "$ARCH"
if ! fetch "$TMPFILE" "$URL"; then
  printf 'Download failed: %s\n' "$URL" >&2
  [ -z "$GH_TOKEN" ] && printf 'Hint: private repo? set GITHUB_TOKEN and retry.\n' >&2
  exit 1
fi
chmod +x "$TMPFILE"

# --- install ---
mkdir -p "$INSTALL_DIR"
TARGET="${INSTALL_DIR}/aicommit"
if [ -w "$INSTALL_DIR" ]; then
  mv -f "$TMPFILE" "$TARGET"
else
  printf 'Installing to %s requires elevated permissions.\n' "$INSTALL_DIR"
  need sudo
  sudo mv -f "$TMPFILE" "$TARGET"
  sudo chmod +x "$TARGET"
fi

# --- verify / hint ---
printf 'Installed aicommit to %s\n' "$TARGET"
if command -v aicommit >/dev/null 2>&1; then
  aicommit version 2>/dev/null || printf '(aicommit installed)\n'
  printf '\naicommit is ready. Try: aicommit commit --dry-run\n'
else
  printf '\nNOTE: %s is not on your PATH.\n' "$INSTALL_DIR"
  printf 'Add it to your shell profile, for example:\n'
  printf '  export PATH="%s:$PATH"\n' "$INSTALL_DIR"
fi
