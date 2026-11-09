---
title: Quick start
---

# Quick start

From an empty folder to a stack deployed on a VPS, in three steps.

## 1. Create the repository on your laptop

```bash
minato init infra
cd infra
git remote add origin git@github.com:you/infra.git
git push -u origin main
```

`minato init` creates:

- `.minato.yml`, the repository settings, with your age public key as a secret
  recipient. The key is generated in `~/.config/minato/age.key` if you have
  none.
- `stacks/hello`, an example stack with a compose file and a `minato.yml`.
- a `.gitignore`, and runs `git init`.

## 2. Set up the server

On a freshly created VPS, the [bootstrap script](vps-bootstrap.md) does
everything below, and also upgrades and hardens the server and installs Docker:

```bash
curl -fsSL https://ermos.github.io/minato/bootstrap.sh -o bootstrap.sh
sudo bash bootstrap.sh --user you --repo git@github.com:you/infra.git
```

On a server that already runs Docker:

```bash
curl -fsSL https://raw.githubusercontent.com/ermos/minato/main/install.sh | sudo sh
sudo minato server init --repo git@github.com:you/infra.git --generate-ssh-key \
     --admin you --admin-ssh-key "ssh-ed25519 AAAA... you@laptop" --systemd
```

`server init` writes `/etc/minato/config.yml`, generates the server age key, a
deploy key, a TLS certificate and webhook tokens, creates the first admin and
starts the `minato` systemd service. It prints three things to copy:

1. **The server age key.** Add it to the recipients of `.minato.yml`, so the
   server can decrypt your secrets.
2. **The deploy key.** Add it to the repository. Give it write access if you
   want to edit secrets remotely.
3. **The login command** for your laptop.

## 3. Work from your laptop

```bash
minato login vps.example.com -u you      # pins the self-signed certificate
minato status
minato secrets set hello HOOK_TOKEN --random 32
minato logs hello -f
```

From now on, every push to `main` is deployed within a minute. Add a
[webhook](../guides/webhooks.md) to deploy instantly, and read the
[stack settings](../guides/stack-settings.md) to go further.
