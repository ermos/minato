---
title: What is Minato
slug: /intro
sidebar_position: 1
---

# What is Minato

> 港 *minato*: harbor, the place where ships and their containers dock.

Minato keeps the Docker stacks of a server in sync with a git repository. You
push a change, Minato pulls it, renders it (secrets, variables, templates,
files) and deploys it with `docker compose` or `docker stack deploy`.

It is a single binary with a daemon and a CLI. There is no web interface and no
database: the repository is the source of truth, and the git history is the
audit log.

## Who it is for

People who run a few VPS or a homelab, live in a terminal, and want their
infrastructure in git. It covers what Komodo, Portainer or Dokploy do for
deployments, without a dashboard to expose and maintain.

## What it does

- **GitOps first.** Every stack is a folder of the repository with a regular
  compose file. Minato polls the branch, or reacts to a webhook, and converges.
- **Encrypted secrets in git.** SOPS-like files: keys stay readable, values are
  encrypted with [age](https://age-encryption.org) and decrypted on the server
  only.
- **Interpolation.** `${secrets.X}`, `${vars.X}`, `${env.X}` and `${stack.X}`
  work in `minato.yml`, in the compose file and in `*.tmpl` files.
- **Attached files.** A `files/` folder next to the compose file is mounted
  with `./files/...`. Services using a file that changed are restarted.
- **Webhooks.** Your CI can sync the repository, or pull and redeploy one
  precise service after pushing an image.
- **Remote CLI.** `minato login` with an SSH key, an SSH certificate or a
  password, then manage stacks and secrets from your laptop.
- **Compose and Swarm.** External networks are created for you, swarm configs
  and secrets are versioned by content.

## How it fits together

```
 your laptop                    git remote                 your VPS
 ───────────                    ──────────                 ────────
 minato init  ──── git push ───▶ infra repo ◀──── poll ──── minato daemon
 minato login ─────────── HTTPS :7373 (API, webhooks) ────▶   │
                                                              ▼
                                                    docker compose / swarm
```

Next: [install Minato](getting-started/installation.md), then follow the
[quick start](getting-started/quick-start.md).
