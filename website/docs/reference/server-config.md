---
title: Server configuration
---

# Server configuration

`/etc/minato/config.yml` is written by `minato server init`. Restart the
service after editing it: `sudo systemctl restart minato`.

```yaml
repo:
  url: git@github.com:you/infra.git   # empty: use dir as is, without git sync
  branch: main
  ssh_key: /etc/minato/deploy_key     # private key for git over SSH
  dir: /var/lib/minato/repo           # local checkout
  poll: 1m                            # 0 disables polling, webhooks only

data_dir: /var/lib/minato             # checkout, deploy folders, state, sessions
socket: /run/minato/minato.sock       # local API, root only, no authentication
age_key: /etc/minato/age.key          # server identity to decrypt secrets
prune: false                          # take down stacks removed from git

server:
  listen: ":7373"                     # empty disables remote access and webhooks
  domain: minato.example.com
  tls: self-signed                    # self-signed | acme | files | off
  cert_file: ""                       # tls: files
  key_file: ""                        # tls: files
  acme_email: ""                      # tls: acme, optional
  public_url: ""                      # shown in hook URLs, derived when empty
  trust_proxy: false                  # read the client IP from X-Forwarded-For

auth:
  users_file: /etc/minato/users.yml
  password: true                      # false: SSH keys and certificates only
  ssh_cas: []                         # trusted SSH certificate authorities
  session_ttl: 720h

webhooks:
  token: ""                           # protects POST /hooks/sync
  secret: ""                          # HMAC secret for GitHub, Gitea, Gogs

notify:
  url: ""                             # Slack or Discord compatible webhook
  on: [failure]                       # success, failure

docker:
  bin: docker
```

## Notes

- `prune: false` marks stacks removed from git as `orphaned` instead of taking
  them down. A stack that fails to parse is never considered removed.
- The deploy key needs write access to the repository only if you edit secrets
  remotely.
- `public_url` defaults to `https://<domain or host>:<port>`, or `http://` with
  `tls: off`.
