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
  rollout:               # services updated without downtime (compose mode)
    web:
      probe: {port: 8080, path: /health}
      timeout: 2m        # default
      drain: 5s          # default

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

## Rolling deploys

By default, docker compose replaces a container by stopping the old one and
then starting the new one, so the service is down while the new version
boots. Services listed in `deploy.rollout` are updated without downtime:

1. a new container starts next to the running one, with the new image or
   configuration;
2. minato waits until it is ready;
3. both run for `drain`, so the reverse proxy routes to the new one;
4. the old container is stopped and removed.

If the new container exits, turns unhealthy or is not ready within `timeout`,
it is removed and the previous version keeps serving. The stack is reported
in error with the last logs of the new container.

```yaml title="stacks/blog/minato.yml"
deploy:
  rollout:
    web:
      probe: {port: 8080, path: /health}
```

The new container is ready when:

- `probe` is set: `GET http://<container ip>:<port><path>` answers 2xx. minato
  runs it from the host, so it works with images without shell nor curl;
- otherwise, the container has a healthcheck and it reports `healthy`;
- otherwise, the container stayed up for 10 seconds.

Rolling deploys apply to `minato deploy`, git syncs, `pull`, `recreate` and
webhooks using these actions. `restart` still restarts in place. Containers
are only replaced when their configuration or image changed.

A rolled out service cannot have a `container_name` nor publish a host port
(`8080:80`): two of its containers run at the same time. Put it behind a
reverse proxy instead. With Traefik, add the retry middleware so a request
reaching the old container while it stops is replayed on the new one:

```yaml title="stacks/blog/compose.yaml"
labels:
  - traefik.http.routers.blog.middlewares=blog-retry
  - traefik.http.middlewares.blog-retry.retry.attempts=3
```

In swarm mode, use `deploy.update_config` with `order: start-first` in the
compose file instead.

## Disabling a stack

`disabled: true` takes the stack down on the next sync and keeps it down.
`minato down STACK` stops it once, but the next deploy brings it back unless
the stack is disabled in git.

## Manual deploys

With `deploy.auto: false`, git changes mark the stack `out-of-sync` without
deploying it. Deploy with `minato deploy STACK` or a webhook.
