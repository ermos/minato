---
title: Security
---

# Security

- `/etc/minato` and `/var/lib/minato` are root only (0700), the local socket is
  0600.
- Decrypted secrets never touch the disk, except files you request with
  `${secretfile.X}`, inside the root only deploy folder.
- Secret changes made through the API are regular git commits authored by the
  remote user: the history is your audit log.
- Tokens and signatures are compared in constant time. Unknown stacks and hooks
  answer 401, like a wrong token.
- SSH login challenges are single use, bound to the user and expire after one
  minute.
- Sessions are stored as SHA-256 hashes. Removing a user or changing a password
  revokes its sessions.

## Trust boundaries

The daemon runs as root because it drives Docker. Anyone who can make the
server run a compose file can get root on it. Treat these as root access to the
server:

- write access to the repository,
- the `operator` and `admin` roles,
- the webhook tokens of stacks.

## Reporting a vulnerability

Please open a [private security advisory](https://github.com/ermos/minato/security/advisories/new)
rather than a public issue.
