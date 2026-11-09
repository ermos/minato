---
title: Installation
---

# Installation

Minato is one static binary for Linux and macOS (amd64 and arm64). Install it
on the server that runs your stacks, and on your laptop if you want to manage
it remotely.

:::tip Fresh VPS

On a server you just created, use the [bootstrap script](vps-bootstrap.mdx)
instead: it upgrades and hardens the system, and installs Docker, Swarm and
Minato in one go.

:::

## Install script

```bash
curl -fsSL https://raw.githubusercontent.com/ermos/minato/main/install.sh | sudo sh
```

The script downloads the latest release for your system, checks it against
`checksums.txt` and installs it in `/usr/local/bin`.

| Variable | Default | Effect |
| --- | --- | --- |
| `MINATO_VERSION` | latest release | install a precise version, for example `v0.2.0` |
| `MINATO_BIN_DIR` | `/usr/local/bin` | install directory |

## Manual download

Every [release](https://github.com/ermos/minato/releases) ships
`minato-<os>-<arch>` binaries and a `checksums.txt` file.

```bash
curl -fsSLO https://github.com/ermos/minato/releases/latest/download/minato-linux-amd64
sudo install -m 0755 minato-linux-amd64 /usr/local/bin/minato
```

## From source

```bash
go install github.com/ermos/minato/cmd/minato@latest
```

## Server requirements

- Linux with systemd, if you want the service installed for you.
- `docker` with the compose plugin, or a Docker Swarm manager.
- `git`.

Minato runs as root because it drives Docker. See
[security](../reference/security.md) for what that implies.
