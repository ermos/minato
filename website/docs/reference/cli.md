---
title: CLI
---

# CLI

Every command has a `--help`. Global flags:

| Flag | Env | Effect |
| --- | --- | --- |
| `-c, --config` | `MINATO_CONFIG` | server configuration file, `/etc/minato/config.yml` |
| `-r, --remote` | `MINATO_REMOTE` | saved remote to use instead of the local daemon |

## Repository

Run in your working copy, no server needed.

| Command | Effect |
| --- | --- |
| `minato init [DIR]` | create a GitOps repository |
| `minato validate [REPO_DIR]` | validate every stack, no key needed: run it in CI |
| `minato render STACK_DIR` | render locally, secrets redacted (`--show-secrets`, `--out DIR`) |
| `minato secrets ...` | manage secrets, see [secrets](../guides/secrets.md) |

## Operations

Local socket on the server, or the current remote.

| Command | Effect |
| --- | --- |
| `minato status` | commit, stacks and their status: deployed, out-of-sync, error, disabled, down, orphaned |
| `minato sync [-f]` | git pull and reconcile now, `-f` redeploys everything |
| `minato deploy STACK [SERVICE...]` | deploy a stack |
| `minato pull STACK [SERVICE...]` | pull images and redeploy |
| `minato restart STACK [SERVICE...]` | restart containers |
| `minato recreate STACK [SERVICE...]` | recreate containers |
| `minato down STACK` | stop a stack, set `disabled: true` in git to keep it down |
| `minato logs STACK [SERVICE] [-f] [-n 200]` | stream logs |
| `minato ps STACK` | list containers |
| `minato hooks` | list webhook endpoints |
| `minato compose STACK -- ARGS` | run docker compose with the stack environment, on the server only |

## Remote access

| Command | Effect |
| --- | --- |
| `minato login SERVER` | log in and save the remote, see [remote CLI](../remote/client.md) |
| `minato logout` | revoke the session |
| `minato whoami` | current user and role |
| `minato remote ls` | saved remotes |
| `minato remote use NAME` | change the current remote |
| `minato remote rm NAME` | forget a remote |

## Server administration

On the server, as root.

| Command | Effect |
| --- | --- |
| `minato server init` | write the configuration and keys, see [server](../remote/server.md) |
| `minato server run` | run the daemon |
| `minato server info` | URL, certificate fingerprint and age key |
| `minato user add NAME` | add a user, see [users](../remote/users.md) |
| `minato user ls` | list users and their keys |
| `minato user rm NAME` | remove a user and revoke their sessions |
| `minato user passwd NAME` | set or remove (`--remove`) a password |
| `minato user role NAME ROLE` | change a role |
| `minato user key add NAME KEY` | register an SSH key |
| `minato user key rm NAME KEY` | remove an SSH key by fingerprint or comment |
