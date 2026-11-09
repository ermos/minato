---
title: How reconciliation works
---

# How reconciliation works

1. Every `repo.poll` interval, or on a webhook, the branch is fetched and hard
   reset.
2. Each stack is rendered in memory and hashed: compose file, environment,
   files and secrets.
3. If the hash differs from the deployed one, or the last deploy failed, the
   deploy folder is synced and `docker compose up -d --remove-orphans` or
   `docker stack deploy --prune` runs. Stacks with `deploy.auto: false` are
   marked `out-of-sync` instead.
4. Services mounting changed files are restarted.
5. Stacks removed from git are taken down when `prune: true`, otherwise marked
   `orphaned`.
6. Failures are logged, kept in `minato status`, optionally sent to Slack or
   Discord (`notify.url`), and retried on the next sync.

## Stack status

| Status | Meaning |
| --- | --- |
| `deployed` | running the version of the current commit |
| `out-of-sync` | git changed, waiting for a manual deploy (`deploy.auto: false`) |
| `error` | the last render or deploy failed, see `minato status` |
| `disabled` | `disabled: true` in git, kept down |
| `down` | stopped with `minato down` |
| `orphaned` | removed from git, still running because `prune` is off |

## The deploy folder

Each stack is rendered to `/var/lib/minato/stacks/<stack>/`. Minato keeps a
manifest of the files it wrote in `.minato/manifest` and only ever removes
those, so data written by containers next to the compose file is safe.
