---
title: Interpolation
---

# Interpolation

Minato replaces `${namespace.NAME}` references in `minato.yml`, in the compose
file and in every `*.tmpl` file of the stack.

| Syntax | Value |
| --- | --- |
| `${secrets.NAME}` | decrypted secret from `secrets.enc.yml` |
| `${secretfile.NAME}` | absolute path of a file containing the secret, for compose `secrets:` |
| `${vars.NAME}` | value from `vars` in `minato.yml` |
| `${env.NAME}` | environment variable of the daemon |
| `${stack.name}`, `${stack.project}`, `${stack.mode}`, `${stack.dir}` | stack information, `dir` is the deploy folder |
| `${ns.NAME:-default}` | default when missing or empty |
| `${ns.NAME:?message}` | fails the render with a message when missing or empty |
| `$${ns.NAME}` | literal `${ns.NAME}` |

## Rules

- `vars` may use `secrets`, `env` and `stack`. `env` and webhooks may use
  everything, including `vars`.
- The compose file and every `*.tmpl` file are interpolated:
  `nginx.conf.tmpl` becomes `nginx.conf`.
- Only these namespaces are touched. Compose variables like `${TAG}` and nginx
  variables like `$host` are left alone.
- Values injected into the compose file are `$` escaped: a password containing
  `$` is safe.
- An unresolved reference fails the render of that stack with the list of
  problems. The running version stays untouched.

## Example

```nginx title="stacks/blog/files/nginx.conf.tmpl"
server {
  listen 80;
  server_name ${vars.DOMAIN};
  location / {
    proxy_pass http://app:3000;
    proxy_set_header Host $host;
  }
}
```

`${vars.DOMAIN}` is replaced, `$host` is kept for nginx.

## Preview the result

```bash
minato render stacks/blog                  # secrets redacted
minato render stacks/blog --show-secrets   # needs your age key
minato render stacks/blog --out /tmp/blog  # write the whole bundle
```
