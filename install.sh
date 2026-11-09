#!/bin/sh
# Install minato: curl -fsSL https://raw.githubusercontent.com/ermos/minato/main/install.sh | sudo sh
#
# Environment:
#   MINATO_VERSION   version to install (default: latest release), e.g. v0.2.0
#   MINATO_BIN_DIR   install directory (default: /usr/local/bin)
set -eu

REPO="ermos/minato"
BIN_DIR="${MINATO_BIN_DIR:-/usr/local/bin}"

case "$(uname -s)" in
  Linux) OS=linux ;;
  Darwin) OS=darwin ;;
  *) echo "unsupported system $(uname -s)" >&2; exit 1 ;;
esac
case "$(uname -m)" in
  x86_64|amd64) ARCH=amd64 ;;
  aarch64|arm64) ARCH=arm64 ;;
  *) echo "unsupported architecture $(uname -m)" >&2; exit 1 ;;
esac

VERSION="${MINATO_VERSION:-$(curl -fsSL "https://api.github.com/repos/$REPO/releases/latest" | sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p')}"
if [ -z "$VERSION" ]; then
  echo "cannot find the latest minato release" >&2
  exit 1
fi

ASSET="minato-$OS-$ARCH"
BASE="https://github.com/$REPO/releases/download/$VERSION"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

echo "downloading minato $VERSION ($OS/$ARCH)"
curl -fsSL "$BASE/$ASSET" -o "$TMP/$ASSET"
curl -fsSL "$BASE/checksums.txt" -o "$TMP/checksums.txt"

expected="$(grep " $ASSET\$" "$TMP/checksums.txt" | cut -d' ' -f1)"
if command -v sha256sum >/dev/null; then
  actual="$(sha256sum "$TMP/$ASSET" | cut -d' ' -f1)"
else
  actual="$(shasum -a 256 "$TMP/$ASSET" | cut -d' ' -f1)"
fi
if [ -z "$expected" ] || [ "$expected" != "$actual" ]; then
  echo "checksum mismatch for $ASSET" >&2
  exit 1
fi

mkdir -p "$BIN_DIR"
install -m 0755 "$TMP/$ASSET" "$BIN_DIR/minato"
echo "installed: $("$BIN_DIR/minato" --version)"

if [ "$OS" = linux ]; then
  command -v docker >/dev/null || echo "warning: docker is not installed" >&2
  command -v git >/dev/null || echo "warning: git is not installed" >&2
  echo "next on the server: sudo minato server init --repo git@github.com:you/infra.git --generate-ssh-key --systemd"
else
  echo "next: minato init infra, or minato login your-server"
fi
