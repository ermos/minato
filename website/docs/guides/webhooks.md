---
title: Webhooks
---

# Webhooks

Webhooks let your git host or your CI tell Minato to act now, instead of
waiting for the next poll. They are served on the public endpoint, port 7373 by
default.

| Endpoint | Auth | Effect |
| --- | --- | --- |
| `POST /hooks/sync` | `webhooks.token` or `webhooks.secret` (HMAC) | git pull, then reconcile every stack |
| `POST /hooks/<stack>/<hook>` | the hook `token` or `secret` from `minato.yml` | git pull, then run the hook action |
| `GET /healthz` | none | liveness |

`minato hooks` lists the endpoints of every stack.

## Authentication

- **Token**: `Authorization: Bearer ...`, `X-Minato-Token`, `X-Gitlab-Token`
  or `?token=`.
- **HMAC SHA-256 signature**: GitHub `X-Hub-Signature-256`, Gitea and Forgejo
  `X-Gitea-Signature`, Gogs.

Unknown stacks and hooks answer 401, like a wrong token.

## Options

- `?wait=1` runs synchronously and returns the result, with a 500 on failure:
  your CI job fails when the deploy fails.
- `?services=web,worker` narrows a stack hook. It must be a subset of the hook
  `services`.
- `?force=1` on `/hooks/sync` redeploys everything.
- Push events on another branch and pings are ignored.

## Push to git, deploy now

Add a webhook in your repository settings pointing to
`https://vps.example.com:7373/hooks/sync`, with the `webhooks.secret` of
`/etc/minato/config.yml` as secret.

## New image, redeploy one service

```yaml title="stacks/blog/minato.yml"
webhooks:
  release:
    action: pull
    services: [web]
    token: ${secrets.HOOK_TOKEN}
```

```yaml title=".github/workflows/release.yml"
- run: |
    curl -fsS -X POST \
      -H "X-Minato-Token: ${{ secrets.MINATO_HOOK }}" \
      "https://minato.example.com/hooks/blog/release?wait=1"
```
