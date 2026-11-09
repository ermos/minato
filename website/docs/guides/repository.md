---
title: Repository layout
---

# Repository layout

```
infra/
├── .minato.yml               # repository settings: stacks dir, secret recipients
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
with `.` or `_` are ignored. A full example lives in
[`examples/infra`](https://github.com/ermos/minato/tree/main/examples/infra).

## `.minato.yml`

```yaml
stacks_dir: stacks
secrets:
  recipients:
    - age1server...   # printed by `minato server init` and `minato server info`
    - age1alice...    # `minato secrets keygen` on your laptop
```

`recipients` lists the age (or `ssh-ed25519` / `ssh-rsa`) public keys allowed
to decrypt the secrets of the repository. The server key must be in the list.

## `minato init`

```bash
minato init [DIR]
```

| Flag | Effect |
| --- | --- |
| `--recipient KEY` | add a recipient, repeatable |
| `--remote-key NAME` | add the server key of this saved remote (default: the current remote, or the local daemon) |
| `--stacks-dir` | name of the stacks folder, `stacks` by default |
| `--no-key` | do not generate a personal age key |
| `--no-example` | skip the `stacks/hello` example |
| `--no-git` | skip `git init` |
| `--force` | write into a non empty folder |

## Validate in CI

`minato validate` parses every stack and checks interpolation without any key.
Run it on pull requests:

```yaml
- run: curl -fsSL https://raw.githubusercontent.com/ermos/minato/main/install.sh | sudo sh
- run: minato validate .
```
