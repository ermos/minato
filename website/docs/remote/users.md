---
title: Users and login
---

# Users and login

Remote users live in `/etc/minato/users.yml` and are managed on the server, as
root.

```bash
sudo minato user add alice --role admin --ssh-key @alice.pub
sudo minato user add ci --role operator --password
sudo minato user key add alice "ssh-ed25519 AAAA... alice@desktop"
sudo minato user key rm alice alice@desktop
sudo minato user role alice operator
sudo minato user passwd ci            # revokes its sessions
sudo minato user rm ci                # revokes its sessions
sudo minato user ls
```

## Roles

| Role | Can |
| --- | --- |
| `viewer` | status, hooks, logs, ps, secret names |
| `operator` | everything a viewer can, plus sync, deploy, pull, restart, recreate, down, and set, unset or rotate secrets |
| `admin` | everything an operator can, plus read secret values and rendered compose files |

## Login methods

- **SSH key**: the client signs a single use challenge with ssh-agent,
  `~/.ssh/id_*` or `-i key`. The public key must be registered on the user.
- **SSH certificate**: an `id_ed25519-cert.pub` signed by a CA listed in
  `auth.ssh_cas`, with the user name as principal. The user must exist in
  `users.yml` to get a role, but no key needs to be registered.
- **Password**: hashed with bcrypt. Disable it with `auth.password: false`.

```yaml title="/etc/minato/config.yml"
auth:
  password: false        # SSH keys and certificates only
  ssh_cas:               # trust certificates from your SSH CA
    - ssh-ed25519 AAAA... ca@company
  session_ttl: 720h
```

Failed logins are rate limited per IP address. Sessions are random 256 bit
tokens, stored hashed on the server, and survive a daemon restart.
