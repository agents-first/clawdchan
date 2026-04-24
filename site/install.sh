#!/bin/sh
# ClawdChan installer.
#
#   curl -fsSL https://clawdchan.ai/install.sh | sh
#   curl -fsSL https://clawdchan.ai/install.sh | sh -s -- --version v0.1.0
#   curl -fsSL https://clawdchan.ai/install.sh | sh -s -- --prefix ~/.local
#
# Downloads the matching prebuilt archive from GitHub Releases, unpacks
# clawdchan, clawdchan-mcp, clawdchan-relay into a bin dir, and optionally
# runs `clawdchan setup`. POSIX sh only — no bashisms.

set -eu

REPO="agents-first/clawdchan"
DEFAULT_PREFIX="${HOME}/.clawdchan"
VERSION=""
PREFIX=""
RUN_SETUP="auto"
QUIET=0

say()  { [ "$QUIET" -eq 1 ] || printf '%s\n' "$*"; }
warn() { printf '%s\n' "$*" >&2; }
die()  { warn "error: $*"; exit 1; }

usage() {
  cat <<EOF
clawdchan installer

Options:
  --version <tag>     Install a specific release tag (default: latest)
  --prefix  <dir>     Install into <dir>/bin (default: ~/.clawdchan/bin)
  --no-setup          Don't run 'clawdchan setup' after install
  --setup             Always run 'clawdchan setup' (no prompt)
  --quiet             Suppress progress output
  -h, --help          Show this help
EOF
}

while [ $# -gt 0 ]; do
  case "$1" in
    --version)    VERSION="${2:?}"; shift 2 ;;
    --version=*)  VERSION="${1#*=}"; shift ;;
    --prefix)     PREFIX="${2:?}"; shift 2 ;;
    --prefix=*)   PREFIX="${1#*=}"; shift ;;
    --no-setup)   RUN_SETUP="no"; shift ;;
    --setup)      RUN_SETUP="yes"; shift ;;
    --quiet|-q)   QUIET=1; shift ;;
    -h|--help)    usage; exit 0 ;;
    *)            die "unknown flag: $1 (see --help)" ;;
  esac
done

[ -n "$PREFIX" ] || PREFIX="$DEFAULT_PREFIX"
BIN_DIR="$PREFIX/bin"

uname_s=$(uname -s 2>/dev/null || echo unknown)
uname_m=$(uname -m 2>/dev/null || echo unknown)

case "$uname_s" in
  Darwin)                 OS_ARCHIVE="macOS" ;;
  Linux)                  OS_ARCHIVE="Linux" ;;
  MINGW*|MSYS*|CYGWIN*)   die "windows: use irm https://clawdchan.ai/install.ps1 | iex" ;;
  *)                      die "unsupported OS: $uname_s" ;;
esac

case "$uname_m" in
  x86_64|amd64)           ARCH_ARCHIVE="x86_64" ;;
  arm64|aarch64)          ARCH_ARCHIVE="arm64" ;;
  *)                      die "unsupported arch: $uname_m" ;;
esac

need() { command -v "$1" >/dev/null 2>&1 || die "missing dependency: $1"; }
need uname
need tar
if command -v curl >/dev/null 2>&1; then
  DL() { curl -fsSL "$1" -o "$2"; }
  DL_STDOUT() { curl -fsSL "$1"; }
elif command -v wget >/dev/null 2>&1; then
  DL() { wget -qO "$2" "$1"; }
  DL_STDOUT() { wget -qO- "$1"; }
else
  die "need curl or wget"
fi

if [ -z "$VERSION" ]; then
  say "==> resolving latest release"
  VERSION=$(DL_STDOUT "https://api.github.com/repos/$REPO/releases/latest" \
    | sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' | head -n1)
  [ -n "$VERSION" ] || die "could not resolve latest release — pass --version vX.Y.Z"
fi

VERSION_NO_V=${VERSION#v}
ARCHIVE="clawdchan_${VERSION_NO_V}_${OS_ARCHIVE}_${ARCH_ARCHIVE}.tar.gz"
URL="https://github.com/$REPO/releases/download/${VERSION}/${ARCHIVE}"

say "==> clawdchan ${VERSION} (${OS_ARCHIVE}_${ARCH_ARCHIVE})"
say "    → $URL"

TMP=$(mktemp -d 2>/dev/null || mktemp -d -t clawdchan)
trap 'rm -rf "$TMP"' EXIT INT HUP TERM

DL "$URL" "$TMP/$ARCHIVE" || die "download failed"

say "==> verifying checksum"
SUMS_URL="https://github.com/$REPO/releases/download/${VERSION}/checksums.txt"
if DL "$SUMS_URL" "$TMP/checksums.txt" 2>/dev/null; then
  WANT=$(grep " ${ARCHIVE}$" "$TMP/checksums.txt" | awk '{print $1}' | head -n1)
  if [ -n "$WANT" ]; then
    if command -v shasum >/dev/null 2>&1; then
      GOT=$(shasum -a 256 "$TMP/$ARCHIVE" | awk '{print $1}')
    elif command -v sha256sum >/dev/null 2>&1; then
      GOT=$(sha256sum "$TMP/$ARCHIVE" | awk '{print $1}')
    else
      GOT=""
    fi
    [ -n "$GOT" ] || warn "    (no sha256 tool found — skipping)"
    if [ -n "$GOT" ] && [ "$WANT" != "$GOT" ]; then
      die "checksum mismatch: want $WANT, got $GOT"
    fi
  else
    warn "    (archive not listed in checksums.txt — skipping)"
  fi
else
  warn "    (checksums.txt not found — skipping)"
fi

say "==> installing to $BIN_DIR"
mkdir -p "$BIN_DIR"
tar -xzf "$TMP/$ARCHIVE" -C "$TMP"
for b in clawdchan clawdchan-mcp clawdchan-relay; do
  src="$TMP/$b"
  [ -f "$src" ] || die "missing $b in archive"
  mv "$src" "$BIN_DIR/$b"
  chmod +x "$BIN_DIR/$b"
done

SHELL_NAME=$(basename "${SHELL:-sh}")
case ":$PATH:" in
  *":$BIN_DIR:"*) PATH_OK=1 ;;
  *)              PATH_OK=0 ;;
esac

say ""
say "installed:"
say "  $BIN_DIR/clawdchan"
say "  $BIN_DIR/clawdchan-mcp"
say "  $BIN_DIR/clawdchan-relay"
say ""

if [ "$PATH_OK" -eq 0 ]; then
  case "$SHELL_NAME" in
    zsh)  RC="$HOME/.zshrc" ;;
    bash) RC="$HOME/.bashrc" ;;
    fish) RC="$HOME/.config/fish/config.fish" ;;
    *)    RC="your shell profile" ;;
  esac
  say "add this to $RC:"
  if [ "$SHELL_NAME" = "fish" ]; then
    say "  fish_add_path $BIN_DIR"
  else
    say "  export PATH=\"$BIN_DIR:\$PATH\""
  fi
  say ""
fi

if [ "$uname_s" = "Darwin" ] && ! command -v terminal-notifier >/dev/null 2>&1; then
  if command -v brew >/dev/null 2>&1 && [ -t 0 ] && [ -t 1 ]; then
    printf "\nterminal-notifier is recommended on macOS (osascript banners are often dropped).\nInstall via 'brew install terminal-notifier'? [Y/n] "
    read ans
    case "$ans" in
      n|N|no|NO) say "skipped — install later with: brew install terminal-notifier" ;;
      *)         brew install terminal-notifier || warn "brew install failed — continue without it" ;;
    esac
  else
    say "note: install 'terminal-notifier' for reliable macOS banners (brew install terminal-notifier)"
  fi
  say ""
fi

if [ "$RUN_SETUP" = "yes" ] || { [ "$RUN_SETUP" = "auto" ] && [ -t 0 ] && [ -t 1 ]; }; then
  say "==> running: clawdchan setup"
  "$BIN_DIR/clawdchan" setup || warn "setup did not complete — re-run '$BIN_DIR/clawdchan setup' anytime"
else
  say "next: run '$BIN_DIR/clawdchan setup'"
fi
