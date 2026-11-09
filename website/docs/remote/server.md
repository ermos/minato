---
title: Server and TLS
---

# Server and TLS

The daemon exposes one public endpoint, port **7373** by default, serving the
remote API and the webhooks. On the server itself the CLI talks to the local
socket `/run/minato/minato.sock` (root only) and needs no login.

## `minato server init`

```bash
sudo minato server init --repo git@github.com:you/infra.git --generate-ssh-key \
     --admin you --admin-ssh-key @you.pub --systemd
```

| Flag | Default | Effect |
| --- | --- | --- |
| `--repo` | | git repository URL, ssh or https |
| `--branch` | `main` | branch to track |
| `--local-dir` | | use a local folder instead of cloning |
| `--ssh-key` | | SSH private key used for git |
| `--generate-ssh-key` | | generate an ed25519 deploy key |
| `--poll` | `1m` | polling interval, `0` relies on webhooks only |
| `--listen` | `:7373` | public address, empty disables remote access |
| `--domain` | | domain name of the server |
| `--tls` | `self-signed` | `self-signed`, `acme` or `off` |
| `--admin` | | create a first admin user |
| `--admin-ssh-key` | | SSH public key of the admin, or `@file` |
| `--no-password` | | disable password login |
| `--systemd` | | install and start the systemd service |
| `--force` | | overwrite an existing configuration |

`minato server info` prints the URL, the certificate fingerprint and the server
age key again at any time.

## TLS and domain

| `server.tls` | Use it when | Listen |
| --- | --- | --- |
| `self-signed` | you reach the server by IP or host name. `minato login` pins the certificate fingerprint, like SSH. | `:7373` |
| `acme` | `server.domain` points to the VPS and ports 80 and 443 are free. Let's Encrypt certificate. | `:443` |
| `off` | a reverse proxy (Traefik, Caddy, nginx) owns 443 and routes `minato.example.com` to `127.0.0.1:7373`. | `127.0.0.1:7373` |
| `files` | you bring `cert_file` and `key_file`. | any |

```bash
sudo minato server init --repo ... --domain minato.example.com --tls off   # behind Traefik or Caddy
sudo minato server init --repo ... --domain minato.example.com --tls acme  # direct Let's Encrypt
```

With `tls: off`, `trust_proxy` is enabled so rate limiting uses the client
address from `X-Forwarded-For`. Only enable it behind a proxy you control.

## Running the daemon

`--systemd` installs `/etc/systemd/system/minato.service`, which runs
`minato server run`. Without systemd:

```bash
sudo minato server run            # --json for structured logs, --debug for more
```
