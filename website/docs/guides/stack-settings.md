---
title: Stack settings
---

# Stack settings: `minato.yml`

A `minato.yml` next to the compose file configures the stack. Every field is
optional: a folder with only a `compose.yml` is a valid stack.

```yaml
mode: compose            # compose (default) or swarm
project: blog            # compose project / swarm stack name, default: folder name
compose: compose.yml     # auto detected: compose.y(a)ml, docker-compose.y(a)ml
disabled: false          # true takes the stack down and keeps it down

vars:                    # free values, usable as ${vars.NAME}
  DOMAIN: blog.example.com

env:                     # exported to compose, usable as ${NAME} in compose.yml
  TAG: "1.4.2"
  DATABASE_URL: postgres://blog:${secrets.DB_PASSWORD}@db/blog

secrets: secrets.enc.yml # default

files:
  dir: files             # default
  templates: true        # render *.tmpl files (default true)
  on_change: restart     # restart | recreate | none: services mounting a changed file

deploy:
  auto: true             # deploy on every git change. false: only CLI and webhooks
  pull: missing          # missing | always | never
  wait: false            # wait for healthy containers or converged services

webhooks:
  release:               # POST /hooks/blog/release
    action: pull         # deploy | pull | restart | recreate
    services: [web]      # empty means the whole stack
    token: ${secrets.HOOK_TOKEN}
```

## Modes

- **compose** runs `docker compose up -d --remove-orphans` in the deploy
  folder, with `env` exported.
- **swarm** runs `docker stack deploy --prune`. File based `configs` and
  `secrets` get a content hash in their name so updates roll out, and old
  versions are pruned.

External networks declared in the compose file are created when missing
(overlay and attachable in swarm mode).

## Disabling a stack

`disabled: true` takes the stack down on the next sync and keeps it down.
`minato down STACK` stops it once, but the next deploy brings it back unless
the stack is disabled in git.

## Manual deploys

With `deploy.auto: false`, git changes mark the stack `out-of-sync` without
deploying it. Deploy with `minato deploy STACK` or a webhook.
