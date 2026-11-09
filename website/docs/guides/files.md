---
title: Attached files
---

# Attached files

Configuration files, static assets, scripts: put them next to the compose file
and mount them with relative paths.

Everything in the stack folder, except `minato.yml` and the secrets file, is
copied to the deploy folder `/var/lib/minato/stacks/<stack>/`, next to the
rendered compose file.

```yaml title="stacks/blog/compose.yml"
services:
  web:
    image: nginx:1.27
    volumes:
      - ./files/nginx.conf:/etc/nginx/conf.d/default.conf:ro
      - ./files/static:/usr/share/nginx/html:ro
```

## Templates

Files ending in `.tmpl` are [interpolated](interpolation.md) and written
without the extension: `files/nginx.conf.tmpl` becomes `files/nginx.conf`.
Disable it with `files.templates: false`.

## When a file changes

- Files are rewritten **in place**, so single file bind mounts keep working.
- Services mounting a changed file (bind mount, compose `configs` or
  `secrets`) are restarted after the deploy. Choose the behaviour with
  `files.on_change`: `restart` (default), `recreate` or `none`.
- Only files written by Minato are ever removed: data created by containers in
  `./data` survives.
- Symlinks are not copied.

## Swarm

In swarm mode, file based `configs` and `secrets` are immutable. Minato adds a
content hash to their name so an update rolls out a new version, and prunes the
old ones.
