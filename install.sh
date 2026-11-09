#!/bin/sh
# Install the latest minato release: curl -fsSL https://raw.githubusercontent.com/ermos/minato/main/install.sh | sudo sh
set -eu
REPO="ermos/minato"
case "$(uname -m)" in
  x86_64|amd64) ARCH=amd64 ;;
  aarch64|arm64) ARCH=arm64 ;;
  *) echo "unsupported architecture $(uname -m)" >&2; exit 1 ;;
esac
VERSION="${MINATO_VERSION:-$(curl -fsSL "https://api.github.com/repos/$REPO/releases/latest" | sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p')}"
URL="https://github.com/$REPO/releases/download/$VERSION/minato-linux-$ARCH"
echo "downloading minato $VERSION ($ARCH)"
curl -fsSL "$URL" -o /usr/local/bin/minato
chmod +x /usr/local/bin/minato
command -v docker >/dev/null || echo "warning: docker is not installed" >&2
command -v git >/dev/null || echo "warning: git is not installed" >&2
echo "installed: $(/usr/local/bin/minato --version)"
echo "next: sudo minato init --repo git@github.com:you/infra.git --generate-ssh-key --systemd"
