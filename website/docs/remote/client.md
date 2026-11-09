---
title: Remote CLI
---

# Remote CLI

The same `minato` binary manages a server from your laptop.

```bash
minato login vps.example.com -u alice          # https://vps.example.com:7373
minato login https://minato.example.com -u alice --password
minato remote ls                               # saved servers, * is current
minato remote use prod
minato -r staging status                       # one-off on another remote
minato whoami
minato logout
```

## `minato login`

| Flag | Effect |
| --- | --- |
| `-u, --user` | user name, defaults to `$USER` |
| `--name` | name of the saved remote, defaults to the host |
| `-i, --identity` | SSH private key to use |
| `--password` | skip SSH keys and use a password |
| `--fingerprint` | expected certificate fingerprint (`sha256:...`), skips the prompt |
| `-y, --yes` | trust a self-signed certificate without asking |

The first connection to a self-signed server shows the certificate
fingerprint, like SSH does. Compare it with `minato server info` on the server.
The fingerprint is then pinned: a changed certificate is refused.

Without `--password`, the CLI tries SSH certificates, then keys from
ssh-agent, then `~/.ssh/id_ed25519`, `id_ecdsa` and `id_rsa`. It falls back to
a password prompt when the server allows it.

## Which server a command talks to

1. `--remote NAME` or `$MINATO_REMOTE`.
2. The local socket, when the CLI runs on the server.
3. The current remote, set by `minato login` or `minato remote use`.

Remotes and session tokens are saved in `~/.config/minato/remotes.yml`, with
mode 0600. Set `$MINATO_REMOTES` to use another file.
