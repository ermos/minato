---
title: Fresh VPS setup
---

# Fresh VPS setup

One script turns a freshly created Debian or Ubuntu server into a hardened
Docker host running Minato. Run it once, right after creating the server, as
root.

```bash
curl -fsSL https://ermos.github.io/minato/bootstrap.sh -o bootstrap.sh
less bootstrap.sh                                   # read what you run as root
sudo bash bootstrap.sh --user alice --ssh-key "ssh-ed25519 AAAA... alice@laptop" \
     --repo git@github.com:you/infra.git
```

Supported systems: Debian 12 and newer, Ubuntu 22.04 and newer, amd64 and
arm64. The script can run again safely, for example to open a port.

:::warning Keep your session open

At the end, SSH only accepts keys, for the admin user only. Log in from a
**new terminal** with the command the script prints before closing the current
session.

:::

## What it does

In order:

1. **Upgrades the system** (`apt full-upgrade`) and installs the tools it needs.
2. **Time**: time zone (UTC by default) and NTP.
3. **Swap**: a 2 GB swap file when the server has less than 4 GB of RAM and no swap.
4. **Automatic updates** with unattended-upgrades: security and stable
   updates of the distribution, and Docker. When an update needs it, the
   server reboots at 04:00.
5. **Admin user** with sudo and your SSH keys. It has no password, so sudo
   does not ask for one.
6. **Kernel hardening** with sysctl:
   - no ICMP redirects or source routing, spoofed packets logged, SYN cookies;
   - kernel pointers and logs hidden, ptrace and unprivileged eBPF restricted;
   - no core dumps;
   - rare file systems and network protocols disabled.
7. **Logs**: a persistent journal, capped at 500 MB and one month.
8. **AppArmor** enabled.
9. **Docker** from the official repository:
   - `no-new-privileges` and `icc=false` for every container;
   - rotated logs, so a chatty container cannot fill the disk.
10. **Docker Swarm**: a single node swarm. Its ports stay closed.
11. **Firewall** (UFW):
    - incoming traffic denied, except SSH (rate limited), 80, 443 and the ports
      you open;
    - **the same rules apply to ports published by containers**. Without this,
      Docker bypasses UFW and a `ports: ["5432:5432"]` would expose your
      database to the internet.
12. **Fail2ban**: SSH bans of one hour that grow up to one week for repeat
    offenders.
13. **Minato**: installed and, with `--repo`, configured:
    - runs `minato server init` with a deploy key, your user as admin
      (SSH keys only) and the systemd service;
    - opens port 7373.
14. **SSH hardening**, last:
    - keys only, no root login, only the admin user;
    - modern algorithms, optional port change.

## Options

| Option | Default | Effect |
| --- | --- | --- |
| `--user NAME` | required | admin user to create |
| `--ssh-key KEY` | keys of root | allowed public key, or `@file`, repeatable |
| `--ssh-port PORT` | `22` | SSH port |
| `--allow-port PORT[/tcp\|udp]` | | open a port, for the host and for containers, repeatable |
| `--timezone TZ` | `UTC` | time zone |
| `--swap SIZE` | auto | swap file size, `0` to skip |
| `--reboot-time HH:MM` | `04:00` | automatic reboot time after updates, `off` to never reboot |
| `--reboot` | | reboot at the end if an update needs it |
| `--no-swarm` | | plain Docker, no swarm |
| `--swarm-addr IP` | main IPv4 | swarm advertise address |
| `--no-docker-hardening` | | keep the Docker defaults for `no-new-privileges` and `icc` |
| `--no-minato` | | do not install Minato |
| `--minato-version VER` | latest | Minato version to install |
| `--repo URL` | | GitOps repository, runs `minato server init` |
| `--branch NAME` | `main` | branch to track |
| `--domain NAME` | | domain of the Minato API |
| `--tls MODE` | `self-signed` | `self-signed`, `acme` or `off`, see [server and TLS](../remote/server.md) |

The whole output is saved in `/var/log/minato-bootstrap.log`.

## After the script

1. Log in as the admin user from a new terminal.
2. With `--repo`, add the printed **deploy key** to the repository, and the
   **server age key** to `.minato.yml`.
3. Reboot if the script says so: `sudo reboot`.

## Publishing a container port

Ports 80 and 443 are open for containers too, which covers a reverse proxy
like Traefik or Caddy. To expose another port, run the script again with it:

```bash
sudo bash bootstrap.sh --user alice --allow-port 8443/tcp
```

Ports published only for the server itself are best bound to the loopback
address, for example `ports: ["127.0.0.1:5432:5432"]`.

## Things to know

- **No new privileges.** A container whose entrypoint relies on `sudo` or a
  setuid binary fails with this setting. Run the script with
  `--no-docker-hardening`, or set `security_opt: ["no-new-privileges=false"]`
  on that service.
- **Default bridge isolation.** `icc=false` only isolates containers on the
  default bridge. Compose and swarm networks work as usual.
- **Containers restart during Docker updates.** Unattended upgrades update
  Docker too, which restarts running containers. The swarm mode does not allow
  the `live-restore` option that would avoid it.
