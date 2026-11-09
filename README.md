<p align="center">
  <img src="website/static/img/seal.svg" alt="Minato seal" width="96">
</p>

<h1 align="center">Minato</h1>

<p align="center">
  <a href="https://github.com/ermos/minato/actions/workflows/ci.yml"><img alt="CI" src="https://github.com/ermos/minato/actions/workflows/ci.yml/badge.svg"></a>
  <a href="https://github.com/ermos/minato/releases/latest"><img alt="Release" src="https://img.shields.io/github/v/release/ermos/minato?sort=semver"></a>
  <a href="LICENSE"><img alt="License: AGPL v3" src="https://img.shields.io/badge/license-AGPL%20v3-2c5d8f"></a>
  <a href="https://ermos.github.io/minato/"><img alt="Documentation" src="https://img.shields.io/badge/docs-ermos.github.io%2Fminato-2c5d8f"></a>
</p>

> 港 *minato*, "harbor": where ships and their containers dock.
> GitOps for Docker Compose and Docker Swarm on a VPS. One binary, no UI, no database.

**Documentation: [ermos.github.io/minato](https://ermos.github.io/minato/)**

Minato keeps the stacks of a server in sync with a git repository. Push a
change, Minato pulls it, renders it (secrets, variables, templates, files) and
deploys it with `docker compose` or `docker stack deploy`. A lightweight
alternative to Komodo, Portainer or Dokploy for people who live in a terminal
and want their infrastructure in git.

- **GitOps first**: the repository is the source of truth, every stack is a folder.
- **Encrypted secrets in git**: SOPS-like files, keys readable, values encrypted with [age](https://age-encryption.org), decrypted on the server only.
- **Interpolation** everywhere: `${secrets.X}`, `${vars.X}`, `${env.X}`, `${stack.X}` in `minato.yml`, the compose file and `*.tmpl` files.
- **Attached files**: a `files/` folder next to the compose file, mounted with `./files/...`, services restarted when a file changes.
- **Webhooks** for CI: sync the repo, or pull, deploy, restart a precise service.
- **Remote CLI**: `minato login` with an SSH key, an SSH certificate or a password, then manage stacks and secrets from your laptop. Roles: viewer, operator, admin.
- **Compose and Swarm**, external networks created automatically, immutable swarm configs versioned for you.

## Quick start

On your laptop:

```sh
minato init infra                  # .minato.yml, stacks/hello, your age key, git init
cd infra && git remote add origin git@github.com:you/infra.git && git push -u origin main
```

On a freshly created VPS, one script upgrades and hardens the system (SSH keys
only, firewall that also covers container ports, fail2ban, automatic security
updates, kernel settings) and installs Docker, Swarm and Minato. See the
[fresh VPS setup](https://ermos.github.io/minato/docs/getting-started/vps-bootstrap).

```sh
curl -fsSL https://ermos.github.io/minato/bootstrap.sh -o bootstrap.sh
sudo bash bootstrap.sh --user you --repo git@github.com:you/infra.git
```

On a server that already runs Docker:

```sh
curl -fsSL https://raw.githubusercontent.com/ermos/minato/main/install.sh | sudo sh
sudo minato server init --repo git@github.com:you/infra.git --generate-ssh-key \
     --admin you --admin-ssh-key "ssh-ed25519 AAAA... you@laptop" --systemd
```

`server init` writes `/etc/minato/config.yml`, generates the server age key,
a deploy key, a TLS certificate and webhook tokens, creates the first admin
and starts the `minato` systemd service. It prints the **server age key** (add
it to `.minato.yml`), the **deploy key** (add it to the repository, with write
access if you want to edit secrets remotely) and the **login command**.

Back on your laptop:

```sh
minato login vps.example.com -u you      # pins the self-signed certificate
minato status
minato secrets set hello HOOK_TOKEN --random 32
minato logs hello -f
```

Requirements on the server: Linux, `docker` with the compose plugin, `git`.

## Repository layout

```
infra/
├── .minato.yml               # repo settings: stacks dir, secret recipients
└── stacks/
    ├── blog/
    │   ├── compose.yml       # a regular compose file
    │   ├── minato.yml        # optional stack settings
    │   ├── secrets.enc.yml   # encrypted secrets, safe to commit
    │   └── files/            # mounted with ./files/..., *.tmpl are rendered
    │       ├── nginx.conf.tmpl
    │       └── static/
    └── whoami/
        └── compose.yml
```

Every folder of `stacks/` holding a compose file is a stack. Folders starting
with `.` or `_` are ignored. See [`examples/infra`](examples/infra).

```yaml
# .minato.yml
stacks_dir: stacks
secrets:
  recipients:
    - age1server...   # printed by `minato server init` / `minato server info`
    - age1alice...    # `minato secrets keygen` on your laptop
```

`minato init` creates all of this and fills the recipients with your key, the
key of the server you are logged in to and every `--recipient`.

## Stack settings: `minato.yml`

Everything is optional.

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
  auto: true             # deploy on every git change. false: only CLI / webhooks
  pull: missing          # missing | always | never
  wait: false            # wait for healthy containers / converged services

webhooks:
  release:               # POST /hooks/blog/release
    action: pull         # deploy | pull | restart | recreate
    services: [web]      # empty means the whole stack
    token: ${secrets.HOOK_TOKEN}
```

## Interpolation

| Syntax | Value |
| --- | --- |
| `${secrets.NAME}` | decrypted secret from `secrets.enc.yml` |
| `${secretfile.NAME}` | absolute path of a file containing the secret, for compose `secrets:` |
| `${vars.NAME}` | value from `vars` in `minato.yml` |
| `${env.NAME}` | environment variable of the daemon |
| `${stack.name}`, `${stack.project}`, `${stack.mode}`, `${stack.dir}` | stack info, `dir` is the deploy folder |
| `${ns.NAME:-default}` | default when missing or empty |
| `${ns.NAME:?message}` | fails the render with a message when missing or empty |
| `$${ns.NAME}` | literal `${ns.NAME}` |

- `vars` may use `secrets`, `env` and `stack`. `env` and webhooks may use everything including `vars`.
- The compose file and every `*.tmpl` file are interpolated (`nginx.conf.tmpl` becomes `nginx.conf`).
- Only these namespaces are touched: compose variables like `${TAG}` and nginx `$host` are left alone.
- Values injected into the compose file are `$` escaped, a password containing `$` is safe.
- Any unresolved reference fails the render of that stack with the list of problems; the running version stays untouched.

## Secrets

Secret files are flat `KEY: value` maps. Keys stay in clear text so diffs and
reviews make sense, values are encrypted with AES-256-GCM using a random data
key, itself encrypted with age for every recipient. The key name is
authenticated with its value and a MAC covers the whole file, so swapping or
removing entries is detected. Unchanged values keep their ciphertext: a git
diff only shows what really changed.

```yaml
DB_PASSWORD: ENC[v1,ziT+AOT4N9OAQvyg5Iv7...]
HOOK_TOKEN: ENC[v1,6FIrISb26J9nj6tHA60A...]
_minato:
  version: 1
  recipients: [age1server..., age1alice...]
  data_key: YWdlLWVuY3J5cHRpb24ub3JnL3YxCi0+...
  mac: a30163217d1efac2ba21a4e78e0bf48a...
```

Every `minato secrets` command takes a **target**:

- a **stack name** (`blog`): the server does the work through the API (local
  socket, or remote after `minato login`). It decrypts with its own key,
  commits as you, pushes to git and redeploys. No age key needed on your
  laptop, the deploy key needs write access.
- a **file path** (`stacks/blog/secrets.enc.yml`): edited in your working
  copy with your own age key, you commit and push.

```sh
minato secrets set blog DB_PASSWORD --random 32       # operator
cat cert.pem | minato secrets set blog TLS_CERT
minato secrets ls blog                                 # names only, viewer
minato secrets get blog DB_PASSWORD                    # admin
minato secrets edit blog                               # $EDITOR, one commit
minato secrets unset blog OLD_KEY
minato secrets rotate blog                             # new data key for current recipients
minato secrets rotate blog --value DB_PASSWORD         # regenerate a value

minato secrets keygen                                  # personal key, ~/.config/minato/age.key
minato secrets edit stacks/blog/secrets.enc.yml        # local mode
minato secrets encrypt plain.yml -o stacks/blog/secrets.enc.yml
```

Prefer `${secretfile.X}` with compose secrets over environment variables when
the image supports `*_FILE` variables:

```yaml
services:
  db:
    image: postgres:17
    environment:
      POSTGRES_PASSWORD_FILE: /run/secrets/db_password
    secrets: [db_password]
secrets:
  db_password:
    file: ${secretfile.DB_PASSWORD}
```

## Attached files

Everything in the stack folder (except `minato.yml` and the secrets file) is
copied to the deploy folder `/var/lib/minato/stacks/<stack>/`, next to the
rendered compose file. Mount it with relative paths:

```yaml
volumes:
  - ./files/nginx.conf:/etc/nginx/conf.d/default.conf:ro
  - ./files/static:/usr/share/nginx/html:ro
```

- Files are rewritten **in place**, single file bind mounts keep working.
- Services mounting a changed file (bind mount, compose `configs` or `secrets`) are restarted after the deploy (`files.on_change`).
- Only files written by Minato are ever removed: data created by containers in `./data` survives.
- Symlinks are not copied.
- In swarm mode, file based `configs` and `secrets` get a content hash in their name so updates roll out, old versions are pruned.

## Remote access

The daemon exposes one public endpoint, port **7373** by default, serving the
remote API and the webhooks. On the server itself the CLI uses the local
socket `/run/minato/minato.sock` (root only) and needs no login.

### TLS and domain

| `server.tls` | Use it when | Listen |
| --- | --- | --- |
| `self-signed` (default) | you reach the server by IP or host. `minato login` pins the certificate fingerprint, like SSH. | `:7373` |
| `acme` | `server.domain` points to the VPS and ports 80/443 are free. Let's Encrypt certificate. | `:443` |
| `off` | a reverse proxy (Traefik, Caddy, nginx) owns 443 and routes `minato.example.com` to `127.0.0.1:7373`. | `127.0.0.1:7373` |
| `files` | you bring `cert_file` and `key_file`. | any |

```sh
sudo minato server init --repo ... --domain minato.example.com --tls off   # behind Traefik/Caddy
sudo minato server init --repo ... --domain minato.example.com --tls acme  # direct Let's Encrypt
sudo minato server info                                                    # URL, fingerprint, age key
```

### Users

Users live in `/etc/minato/users.yml` and are managed on the server:

```sh
sudo minato user add alice --role admin --ssh-key @alice.pub
sudo minato user add ci --role operator --password
sudo minato user key add alice "ssh-ed25519 AAAA... alice@desktop"
sudo minato user role alice operator
sudo minato user passwd ci            # revokes its sessions
sudo minato user rm ci                # revokes its sessions
sudo minato user ls
```

| Role | Can |
| --- | --- |
| `viewer` | status, hooks, logs, ps, secret names |
| `operator` | + sync, deploy, pull, restart, recreate, down, set / unset / rotate secrets |
| `admin` | + read secret values and rendered compose files |

Login methods, configured in `/etc/minato/config.yml`:

```yaml
auth:
  password: false        # SSH keys and certificates only
  ssh_cas:               # trust certificates from your SSH CA
    - ssh-ed25519 AAAA... ca@company
  session_ttl: 720h
```

- **SSH key**: the client signs a single use challenge with ssh-agent, `~/.ssh/id_*` or `-i key`.
- **SSH certificate**: `id_ed25519-cert.pub` signed by a CA of `auth.ssh_cas`, with the user name as principal. The user must exist in `users.yml` (it gives the role), no key needs to be registered.
- **Password**: bcrypt, disabled with `auth.password: false`.
- Failed logins are rate limited per IP, sessions are random 256 bit tokens stored hashed.

### Client

```sh
minato login vps.example.com -u alice          # https://vps.example.com:7373
minato login https://minato.example.com -u alice --password
minato remote ls                               # saved servers, * is current
minato remote use prod
minato -r staging status                       # one-off on another remote
minato whoami
minato logout
```

Remotes are saved in `~/.config/minato/remotes.yml` (mode 0600). The CLI uses
`--remote` / `$MINATO_REMOTE` first, then the local socket when it runs on the
server, then the current remote.

## Webhooks

Served on the public endpoint.

| Endpoint | Auth | Effect |
| --- | --- | --- |
| `POST /hooks/sync` | `webhooks.token` or `webhooks.secret` (HMAC) | git pull + reconcile every stack |
| `POST /hooks/<stack>/<hook>` | the hook `token` / `secret` from `minato.yml` | git pull, then run the hook action |
| `GET /healthz` | none | liveness |

- Token: `Authorization: Bearer ...`, `X-Minato-Token`, `X-Gitlab-Token` or `?token=`.
- HMAC SHA-256: GitHub `X-Hub-Signature-256`, Gitea / Forgejo `X-Gitea-Signature`, Gogs.
- Push events on another branch and pings are ignored.
- `?wait=1` runs synchronously and returns the result, with a 500 on failure: your CI job fails when the deploy fails.
- `?services=web,worker` narrows a stack hook (must be a subset of the hook `services`).
- `?force=1` on `/hooks/sync` redeploys everything.

```yaml
# GitHub Actions, after pushing ghcr.io/me/blog:latest
- run: curl -fsS -X POST -H "X-Minato-Token: ${{ secrets.MINATO_HOOK }}" "https://minato.example.com/hooks/blog/release?wait=1"
```

## CLI

```
Repository
  minato init [DIR]                   create a GitOps repository
  minato validate [REPO_DIR]          validate every stack, no key needed: run it in CI
  minato render STACK_DIR             render locally, secrets redacted (--show-secrets, --out DIR)
  minato secrets ...                  see above

Operations (local socket or remote)
  minato status                       commit, stacks, status (deployed, out-of-sync, error, disabled, orphaned)
  minato sync [-f]                    git pull + reconcile now (-f redeploys everything)
  minato deploy|pull|restart|recreate STACK [SERVICE...]
  minato down STACK                   stop (set disabled: true in git to keep it down)
  minato logs STACK [SERVICE] [-f] [-n 200]
  minato ps STACK
  minato hooks                        list webhook endpoints
  minato compose STACK -- ARGS        docker compose with the stack environment (on the server)

Remote access
  minato login | logout | remote ls|use|rm | whoami

Server administration (on the VPS, as root)
  minato server init | run | info
  minato user add | ls | rm | passwd | role | key add | key rm
```

## How reconciliation works

1. Every `repo.poll` (or on webhook) the branch is fetched and hard reset.
2. Each stack is rendered in memory and hashed (compose, env, files, secrets).
3. If the hash differs from the deployed one (or the last deploy failed), the
   deploy folder is synced and `docker compose up -d --remove-orphans` /
   `docker stack deploy --prune` runs. Stacks with `deploy.auto: false` are
   marked `out-of-sync` instead.
4. Services mounting changed files are restarted.
5. Stacks removed from git are taken down when `prune: true`, otherwise marked
   `orphaned`. A stack that fails to parse is never considered removed.
6. Failures are logged, kept in `minato status`, optionally sent to Slack or
   Discord (`notify.url`), and retried on the next sync.

## Security notes

- `/etc/minato` and `/var/lib/minato` are root only (0700), the local socket is 0600.
- Decrypted secrets never touch the disk, except files you request with `${secretfile.X}` inside the root only deploy folder.
- Secret changes made through the API are regular git commits authored by the remote user: the history is your audit log.
- Tokens and signatures are compared in constant time, unknown stacks and hooks answer 401.
- The daemon runs as root because it drives docker: treat write access to the repository, and the `operator` role, as root access to the server.

## Contributing

```sh
make fmt lint test build   # bin/minato
make docs-serve            # documentation site on localhost:3000
```

Pull requests are squash merged and their title becomes the commit on `main`,
so titles follow [Conventional Commits](https://www.conventionalcommits.org)
(`feat(secrets): ...`, `fix(webhook): ...`). Releases are automatic:
release-please opens a release pull request with the next semver version and
the changelog, and merging it publishes the binaries with GoReleaser. See the
[contributing guide](https://ermos.github.io/minato/docs/contributing).

## License

[GNU AGPL v3](LICENSE).
