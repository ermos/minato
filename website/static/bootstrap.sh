#!/usr/bin/env bash
# Minato VPS bootstrap: turns a freshly created Debian or Ubuntu server into a
# hardened Docker (Swarm) host running Minato.
#
#   curl -fsSL https://ermos.github.io/minato/bootstrap.sh -o bootstrap.sh
#   less bootstrap.sh
#   sudo bash bootstrap.sh --user alice --ssh-key "ssh-ed25519 AAAA... alice@laptop"
#
# Run `bash bootstrap.sh --help` for every option. Safe to run again: each step
# checks what is already in place.
#
# Copyright (C) 2026 Minato contributors. GNU AGPL v3.

set -Eeuo pipefail

readonly LOG_FILE=/var/log/minato-bootstrap.log
readonly MARK="# Managed by the Minato bootstrap script"

# ---------------------------------------------------------------------------
# Options
# ---------------------------------------------------------------------------

ADMIN_USER=""
SSH_KEYS=()
SSH_PORT=22
EXTRA_PORTS=()
FILTER_DOCKER_PORTS=false
OPEN_API_PORT=false
TIMEZONE="UTC"
SWAP_SIZE="auto"
REBOOT_TIME="04:00"
REBOOT_AT_END=false
WITH_SWARM=true
SWARM_ADDR=""
DOCKER_HARDENING=true
WITH_MINATO=true
AUTO_UPDATE=true
MINATO_VERSION="${MINATO_VERSION:-}"
MINATO_BINARY=""
REPO=""
BRANCH=""
DOMAIN=""
TLS=""

usage() {
  cat <<'EOF'
Minato VPS bootstrap: upgrade, harden and set up Docker, Swarm and Minato on a
fresh Debian 12+ or Ubuntu 22.04+ server. Run it as root.

Usage: bootstrap.sh --user NAME [options]

Access
  --user NAME              admin user to create, with sudo. SSH login as root is disabled.
  --ssh-key KEY            public key allowed to log in as the admin user, or @file.
                           Repeatable. Default: the keys of root (and of the sudo user).
  --ssh-port PORT          SSH port (default 22).
  --allow-port PORT[/tcp|udp]
                           open a port of the server in the firewall, repeatable.
                           Only SSH is open otherwise. Needed for services listening
                           on the host itself, like a reverse proxy in host network
                           mode (--allow-port 80/tcp --allow-port 443/tcp).
  --filter-docker-ports    also apply the firewall to ports published by containers:
                           only --allow-port ones stay reachable. By default the
                           `ports:` of your compose files decide, as Docker does.

System
  --timezone TZ            time zone (default UTC).
  --swap SIZE              swap file size, e.g. 2G, 0 to skip (default: 2G when the
                           server has less than 4 GB of RAM and no swap).
  --reboot-time HH:MM      when unattended upgrades may reboot (default 04:00),
                           "off" to never reboot automatically.
  --reboot                 reboot at the end when an upgrade needs it.

Docker
  --no-swarm               plain Docker, without initializing a single node Swarm.
  --swarm-addr IP          Swarm advertise address (default: the main IPv4 address).
  --no-docker-hardening    keep Docker defaults (no-new-privileges and icc are
                           otherwise enforced for every container).

Minato
  --no-minato              do not install Minato.
  --no-auto-update         do not update Minato automatically (by default a daily
                           systemd timer installs new releases of the same major).
  --minato-version VER     version to install, e.g. v0.1.0 (default: latest release).
  --minato-binary PATH     install this binary instead of downloading a release.
  --repo URL               GitOps repository: runs `minato server init` with a
                           generated deploy key, the admin user and systemd.
  --branch NAME            branch to track (default main).
  --domain NAME            domain name of the Minato API and webhooks. The API then
                           listens on 127.0.0.1:7373 (--tls off): route the domain
                           to it from your reverse proxy.
  --tls MODE               off (default with --domain), self-signed (default
                           without), or acme (Minato gets a Let's Encrypt
                           certificate itself and opens 80 and 443).
  --open-api-port          open port 7373 to reach the API directly, without a domain.
                           Closed by default: use a domain or an SSH tunnel.

  -h, --help               show this help.
EOF
}

die() { printf '\n\033[31merror:\033[0m %s\n' "$*" >&2; exit 1; }
step() { printf '\n\033[1;34m==> %s\033[0m\n' "$*"; }
info() { printf '    %s\n' "$*"; }
warn() { printf '    \033[33mwarning:\033[0m %s\n' "$*" >&2; }

need_value() { [[ $# -ge 2 && -n "$2" ]] || die "$1 needs a value"; }

parse_args() {
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --user) need_value "$@"; ADMIN_USER="$2"; shift 2 ;;
      --ssh-key) need_value "$@"; SSH_KEYS+=("$2"); shift 2 ;;
      --ssh-port) need_value "$@"; SSH_PORT="$2"; shift 2 ;;
      --allow-port) need_value "$@"; EXTRA_PORTS+=("$2"); shift 2 ;;
      --filter-docker-ports) FILTER_DOCKER_PORTS=true; shift ;;
      --open-api-port) OPEN_API_PORT=true; shift ;;
      --timezone) need_value "$@"; TIMEZONE="$2"; shift 2 ;;
      --swap) need_value "$@"; SWAP_SIZE="$2"; shift 2 ;;
      --reboot-time) need_value "$@"; REBOOT_TIME="$2"; shift 2 ;;
      --reboot) REBOOT_AT_END=true; shift ;;
      --no-swarm) WITH_SWARM=false; shift ;;
      --swarm-addr) need_value "$@"; SWARM_ADDR="$2"; shift 2 ;;
      --no-docker-hardening) DOCKER_HARDENING=false; shift ;;
      --no-minato) WITH_MINATO=false; shift ;;
      --no-auto-update) AUTO_UPDATE=false; shift ;;
      --minato-version) need_value "$@"; MINATO_VERSION="$2"; shift 2 ;;
      --minato-binary) need_value "$@"; MINATO_BINARY="$2"; shift 2 ;;
      --repo) need_value "$@"; REPO="$2"; shift 2 ;;
      --branch) need_value "$@"; BRANCH="$2"; shift 2 ;;
      --domain) need_value "$@"; DOMAIN="$2"; shift 2 ;;
      --tls) need_value "$@"; TLS="$2"; shift 2 ;;
      -h|--help) usage; exit 0 ;;
      *) die "unknown option $1 (see --help)" ;;
    esac
  done
}

# ---------------------------------------------------------------------------
# Checks
# ---------------------------------------------------------------------------

OS_ID=""
OS_CODENAME=""

preflight() {
  step "Checking the server"
  [[ $EUID -eq 0 ]] || die "run this script as root (sudo bash bootstrap.sh ...)"
  command -v systemctl >/dev/null || die "systemd is required"

  # shellcheck disable=SC1091
  . /etc/os-release
  OS_ID="$ID"
  OS_CODENAME="${VERSION_CODENAME:-}"
  case "$OS_ID" in
    debian) [[ "${VERSION_ID%%.*}" -ge 12 ]] || die "Debian 12 or newer is required" ;;
    ubuntu) [[ "${VERSION_ID%%.*}" -ge 22 ]] || die "Ubuntu 22.04 or newer is required" ;;
    *) die "unsupported system $PRETTY_NAME (Debian 12+ or Ubuntu 22.04+)" ;;
  esac
  [[ -n "$OS_CODENAME" ]] || die "cannot find the release code name in /etc/os-release"
  info "$PRETTY_NAME, $(uname -m)"

  if [[ -z "$ADMIN_USER" ]]; then
    if [[ -n "${SUDO_USER:-}" && "$SUDO_USER" != root ]]; then
      ADMIN_USER="$SUDO_USER"
    else
      die "--user NAME is required: root login over SSH is disabled at the end"
    fi
  fi
  [[ "$ADMIN_USER" =~ ^[a-z_][a-z0-9_-]{0,31}$ ]] || die "invalid user name $ADMIN_USER"
  [[ "$ADMIN_USER" != root ]] || die "--user must not be root"
  [[ "$SSH_PORT" =~ ^[0-9]+$ && "$SSH_PORT" -ge 1 && "$SSH_PORT" -le 65535 ]] || die "invalid SSH port $SSH_PORT"
  [[ "$REBOOT_TIME" == off || "$REBOOT_TIME" =~ ^[0-2][0-9]:[0-5][0-9]$ ]] || die "invalid --reboot-time $REBOOT_TIME"
  case "${TLS:-self-signed}" in self-signed|acme|off) ;; *) die "invalid --tls $TLS" ;; esac
  [[ -z "$TLS$DOMAIN$BRANCH" || -n "$REPO" ]] || die "--domain, --tls and --branch need --repo"
  [[ "$TLS" != acme || -n "$DOMAIN" ]] || die "--tls acme needs --domain"
  # With a domain, the reverse proxy of the server handles TLS by default.
  [[ -z "$DOMAIN" || -n "$TLS" ]] || TLS=off
  [[ "$OPEN_API_PORT" == false || "$TLS" != off ]] || die "--open-api-port is useless with --tls off (the API listens on 127.0.0.1)"
  [[ -z "$MINATO_BINARY" || -f "$MINATO_BINARY" ]] || die "$MINATO_BINARY not found"
  [[ -e "/usr/share/zoneinfo/$TIMEZONE" ]] || die "unknown time zone $TIMEZONE"

  collect_keys
}

# KEYS holds the validated public keys of the admin user.
KEYS=()

collect_keys() {
  local raw=() k
  for k in "${SSH_KEYS[@]}"; do
    if [[ "$k" == @* ]]; then
      [[ -f "${k#@}" ]] || die "key file ${k#@} not found"
      mapfile -t -O "${#raw[@]}" raw < "${k#@}"
    else
      raw+=("$k")
    fi
  done
  if [[ ${#SSH_KEYS[@]} -eq 0 ]]; then
    local f
    for f in /root/.ssh/authorized_keys "/home/$ADMIN_USER/.ssh/authorized_keys" \
             ${SUDO_USER:+"/home/$SUDO_USER/.ssh/authorized_keys"}; do
      [[ -f "$f" ]] && mapfile -t -O "${#raw[@]}" raw < "$f"
    done
  fi

  local tmp
  tmp="$(mktemp)"
  for k in "${raw[@]}"; do
    # Cloud images prefix root keys with options that refuse the login.
    k="$(sed -E 's/^.*(ssh-(ed25519|rsa)|ecdsa-sha2-nistp[0-9]+|sk-(ssh-ed25519|ecdsa-sha2-nistp256)@openssh\.com) /\1 /' <<<"$k")"
    [[ -z "$k" || "$k" == \#* ]] && continue
    printf '%s\n' "$k" > "$tmp"
    if ssh-keygen -l -f "$tmp" >/dev/null 2>&1; then
      [[ " ${KEYS[*]} " == *" $k "* ]] || KEYS+=("$k")
    else
      warn "ignoring an invalid SSH key: ${k:0:40}..."
    fi
  done
  rm -f "$tmp"
  [[ ${#KEYS[@]} -gt 0 ]] || die "no SSH public key: pass --ssh-key, password login is disabled and you would be locked out"
  info "${#KEYS[@]} SSH key(s) for $ADMIN_USER"
}

# ---------------------------------------------------------------------------
# Packages
# ---------------------------------------------------------------------------

export DEBIAN_FRONTEND=noninteractive
APT_OPTS=(-y -q -o Dpkg::Options::=--force-confdef -o Dpkg::Options::=--force-confold)

upgrade_system() {
  step "Upgrading the system"
  # Restart services after library upgrades without asking (Ubuntu needrestart).
  mkdir -p /etc/needrestart/conf.d
  printf "%s\n\$nrconf{restart} = 'a';\n" "$MARK" > /etc/needrestart/conf.d/50-minato.conf
  apt-get update -q
  apt-get "${APT_OPTS[@]}" full-upgrade
  apt-get "${APT_OPTS[@]}" install \
    ca-certificates curl gnupg git sudo openssh-server \
    ufw fail2ban python3-systemd \
    unattended-upgrades apt-listchanges needrestart \
    apparmor apparmor-utils logrotate
  apt-get "${APT_OPTS[@]}" autoremove --purge
}

setup_time() {
  step "Time zone and clock"
  timedatectl set-timezone "$TIMEZONE"
  timedatectl set-ntp true 2>/dev/null || warn "cannot enable NTP synchronization"
  info "$TIMEZONE, NTP on"
}

setup_swap() {
  step "Swap"
  local size="$SWAP_SIZE"
  if [[ -n "$(swapon --noheadings 2>/dev/null)" ]]; then
    info "swap already present"
    return
  fi
  if [[ "$size" == auto ]]; then
    local mem_kb
    mem_kb="$(awk '/MemTotal/ {print $2}' /proc/meminfo)"
    if (( mem_kb < 4 * 1024 * 1024 )); then size=2G; else size=0; fi
  fi
  if [[ "$size" == 0 ]]; then
    info "no swap"
    return
  fi
  fallocate -l "$size" /swapfile 2>/dev/null || dd if=/dev/zero of=/swapfile bs=1M count="$(numfmt --from=iec "$size" | awk '{print int($1/1048576)}')" status=none
  chmod 600 /swapfile
  mkswap /swapfile >/dev/null
  swapon /swapfile
  grep -q '^/swapfile ' /etc/fstab || echo '/swapfile none swap sw 0 0' >> /etc/fstab
  printf '%s\nvm.swappiness = 10\n' "$MARK" > /etc/sysctl.d/90-minato-swap.conf
  info "$size swap file"
}

setup_unattended_upgrades() {
  step "Automatic security updates"
  cat > /etc/apt/apt.conf.d/20auto-upgrades <<EOF
$MARK
APT::Periodic::Update-Package-Lists "1";
APT::Periodic::Unattended-Upgrade "1";
APT::Periodic::AutocleanInterval "7";
EOF
  local updates reboot=true
  # ${distro_codename} is expanded by apt, not by the shell.
  # shellcheck disable=SC2016
  if [[ "$OS_ID" == debian ]]; then
    updates='"origin=Debian,codename=${distro_codename}-updates";'
  else
    # shellcheck disable=SC2016
    updates='"origin=Ubuntu,archive=${distro_codename}-updates";'
  fi
  [[ "$REBOOT_TIME" != off ]] || reboot=false
  cat > /etc/apt/apt.conf.d/52minato-unattended-upgrades <<EOF
$MARK
// Security updates come from the distribution defaults (50unattended-upgrades).
Unattended-Upgrade::Origins-Pattern {
        $updates
        "origin=Docker,label=Docker CE";
};
Unattended-Upgrade::Remove-Unused-Kernel-Packages "true";
Unattended-Upgrade::Remove-New-Unused-Dependencies "true";
Unattended-Upgrade::Remove-Unused-Dependencies "true";
Unattended-Upgrade::Automatic-Reboot "$reboot";
Unattended-Upgrade::Automatic-Reboot-WithUsers "true";
Unattended-Upgrade::Automatic-Reboot-Time "${REBOOT_TIME/off/04:00}";
EOF
  systemctl enable --now unattended-upgrades >/dev/null 2>&1 || true
  systemctl enable --now apt-daily.timer apt-daily-upgrade.timer >/dev/null 2>&1 || true
  if [[ "$reboot" == true ]]; then info "daily upgrades, reboot at $REBOOT_TIME when needed"; else info "daily upgrades, no automatic reboot"; fi
}

# ---------------------------------------------------------------------------
# Access
# ---------------------------------------------------------------------------

setup_admin_user() {
  step "Admin user $ADMIN_USER"
  if id "$ADMIN_USER" >/dev/null 2>&1; then
    info "user exists"
  else
    useradd --create-home --shell /bin/bash "$ADMIN_USER"
    info "user created"
  fi
  usermod -aG sudo "$ADMIN_USER"

  # No password exists, sudo must not ask for one.
  local sudoers="/etc/sudoers.d/90-minato-$ADMIN_USER"
  printf '%s\n%s ALL=(ALL:ALL) NOPASSWD: ALL\n' "$MARK" "$ADMIN_USER" > "$sudoers.tmp"
  chmod 440 "$sudoers.tmp"
  visudo -cqf "$sudoers.tmp" || die "invalid sudoers file"
  mv "$sudoers.tmp" "$sudoers"

  local home ssh_dir
  home="$(getent passwd "$ADMIN_USER" | cut -d: -f6)"
  ssh_dir="$home/.ssh"
  install -d -m 700 -o "$ADMIN_USER" -g "$ADMIN_USER" "$ssh_dir"
  touch "$ssh_dir/authorized_keys"
  local k
  for k in "${KEYS[@]}"; do
    grep -qxF "$k" "$ssh_dir/authorized_keys" || printf '%s\n' "$k" >> "$ssh_dir/authorized_keys"
  done
  chown "$ADMIN_USER:$ADMIN_USER" "$ssh_dir/authorized_keys"
  chmod 600 "$ssh_dir/authorized_keys"
  info "sudo without password, $(grep -c . "$ssh_dir/authorized_keys") authorized key(s)"
}

setup_ssh() {
  step "SSH hardening"
  local conf=/etc/ssh/sshd_config.d/00-minato-hardening.conf
  # The first value wins in sshd_config: 00- takes precedence over cloud-init.
  cat > "$conf" <<EOF
$MARK
Port $SSH_PORT
PermitRootLogin no
PasswordAuthentication no
KbdInteractiveAuthentication no
PermitEmptyPasswords no
PubkeyAuthentication yes
AuthenticationMethods publickey
AllowUsers $ADMIN_USER
MaxAuthTries 3
MaxSessions 4
LoginGraceTime 30
ClientAliveInterval 300
ClientAliveCountMax 2
X11Forwarding no
AllowAgentForwarding no
PermitUserEnvironment no
PermitTunnel no
Compression no
LogLevel VERBOSE
KexAlgorithms sntrup761x25519-sha512@openssh.com,curve25519-sha256,curve25519-sha256@libssh.org
Ciphers chacha20-poly1305@openssh.com,aes256-gcm@openssh.com,aes128-gcm@openssh.com
MACs hmac-sha2-512-etm@openssh.com,hmac-sha2-256-etm@openssh.com
EOF
  chmod 644 "$conf"
  mkdir -p /run/sshd
  if ! sshd -t 2>/tmp/minato-sshd-test; then
    rm -f "$conf"
    die "the SSH configuration is invalid, nothing changed: $(cat /tmp/minato-sshd-test)"
  fi

  # Ubuntu 22.10+ starts sshd from ssh.socket, which ignores the Port option.
  if systemctl is-enabled ssh.socket >/dev/null 2>&1; then
    systemctl disable --now ssh.socket >/dev/null 2>&1 || true
    systemctl enable ssh.service >/dev/null 2>&1
  fi
  systemctl restart ssh.service
  info "keys only, root login off, only $ADMIN_USER, port $SSH_PORT"
}

# ---------------------------------------------------------------------------
# Kernel and system
# ---------------------------------------------------------------------------

setup_sysctl() {
  step "Kernel hardening"
  cat > /etc/sysctl.d/90-minato-hardening.conf <<EOF
$MARK
# Network: no redirects or source routing, log spoofed packets, SYN cookies.
# rp_filter is loose (2): strict mode breaks Docker overlay networks.
net.ipv4.conf.all.rp_filter = 2
net.ipv4.conf.default.rp_filter = 2
net.ipv4.conf.all.accept_redirects = 0
net.ipv4.conf.default.accept_redirects = 0
net.ipv4.conf.all.secure_redirects = 0
net.ipv4.conf.default.secure_redirects = 0
net.ipv4.conf.all.send_redirects = 0
net.ipv4.conf.default.send_redirects = 0
net.ipv4.conf.all.accept_source_route = 0
net.ipv4.conf.default.accept_source_route = 0
net.ipv6.conf.all.accept_redirects = 0
net.ipv6.conf.default.accept_redirects = 0
net.ipv6.conf.all.accept_source_route = 0
net.ipv6.conf.default.accept_source_route = 0
net.ipv4.conf.all.log_martians = 1
net.ipv4.conf.default.log_martians = 1
net.ipv4.icmp_echo_ignore_broadcasts = 1
net.ipv4.icmp_ignore_bogus_error_responses = 1
net.ipv4.tcp_syncookies = 1
net.ipv4.tcp_rfc1337 = 1
# Kernel: hide pointers and logs, restrict ptrace, eBPF and core dumps.
kernel.kptr_restrict = 2
kernel.dmesg_restrict = 1
kernel.yama.ptrace_scope = 1
kernel.unprivileged_bpf_disabled = 1
net.core.bpf_jit_harden = 2
kernel.kexec_load_disabled = 1
kernel.sysrq = 0
kernel.randomize_va_space = 2
fs.suid_dumpable = 0
fs.protected_hardlinks = 1
fs.protected_symlinks = 1
fs.protected_fifos = 2
fs.protected_regular = 2
EOF
  sysctl --system >/dev/null 2>&1 || warn "some kernel settings are not supported here"

  # No core dumps: they can hold secrets.
  printf '%s\n* hard core 0\n' "$MARK" > /etc/security/limits.d/90-minato-nocore.conf
  mkdir -p /etc/systemd/coredump.conf.d
  printf '%s\n[Coredump]\nStorage=none\nProcessSizeMax=0\n' "$MARK" > /etc/systemd/coredump.conf.d/90-minato.conf

  # Rare file systems and network protocols, a common source of kernel bugs.
  cat > /etc/modprobe.d/90-minato-blacklist.conf <<EOF
$MARK
install cramfs /bin/false
install freevxfs /bin/false
install jffs2 /bin/false
install hfs /bin/false
install hfsplus /bin/false
install udf /bin/false
install dccp /bin/false
install sctp /bin/false
install rds /bin/false
install tipc /bin/false
install usb-storage /bin/false
EOF
  info "network, kernel and core dump settings applied"
}

setup_journald() {
  step "Logs"
  mkdir -p /etc/systemd/journald.conf.d
  printf '%s\n[Journal]\nStorage=persistent\nSystemMaxUse=500M\nMaxRetentionSec=1month\n' "$MARK" \
    > /etc/systemd/journald.conf.d/90-minato.conf
  systemctl restart systemd-journald
  info "persistent journal, 500 MB, one month"
}

setup_apparmor() {
  step "AppArmor"
  systemctl enable --now apparmor >/dev/null 2>&1 || true
  if aa-enabled >/dev/null 2>&1; then
    info "enabled"
  else
    warn "AppArmor is not active (needs kernel support)"
  fi
}

# ---------------------------------------------------------------------------
# Firewall and intrusion prevention
# ---------------------------------------------------------------------------

# Docker publishes ports with its own iptables rules, before the ones of UFW:
# a published port is reachable even if UFW denies it. With
# --filter-docker-ports, this block drops new connections from outside to
# containers, except on the --allow-port ports, matched on the port the client
# connected to, before Docker's translation. Adapted from
# https://github.com/chaifeng/ufw-docker (GPL v3).
ufw_docker_rules() {
  local spec port protos proto
  printf '%s\n' '# BEGIN UFW AND DOCKER (minato)
*filter
:ufw-user-forward - [0:0]
:ufw-docker-logging-deny - [0:0]
:DOCKER-USER - [0:0]
-A DOCKER-USER -j ufw-user-forward

-A DOCKER-USER -m conntrack --ctstate RELATED,ESTABLISHED -j RETURN
-A DOCKER-USER -m conntrack --ctstate INVALID -j DROP
-A DOCKER-USER -i docker0 -o docker0 -j ACCEPT
'
  for spec in "${EXTRA_PORTS[@]}"; do
    port="${spec%/*}"
    protos="tcp udp"
    [[ "$spec" != */* ]] || protos="${spec#*/}"
    for proto in $protos; do
      printf -- '-A DOCKER-USER -p %s -m conntrack --ctstate NEW --ctorigdstport %s -j RETURN\n' "$proto" "$port"
    done
  done
  printf '%s\n' '
-A DOCKER-USER -j RETURN -s 10.0.0.0/8
-A DOCKER-USER -j RETURN -s 172.16.0.0/12
-A DOCKER-USER -j RETURN -s 192.168.0.0/16

-A DOCKER-USER -p udp -m udp --sport 53 --dport 1024:65535 -j RETURN

-A DOCKER-USER -j ufw-docker-logging-deny -m conntrack --ctstate NEW -d 10.0.0.0/8
-A DOCKER-USER -j ufw-docker-logging-deny -m conntrack --ctstate NEW -d 172.16.0.0/12
-A DOCKER-USER -j ufw-docker-logging-deny -m conntrack --ctstate NEW -d 192.168.0.0/16

-A DOCKER-USER -j RETURN

-A ufw-docker-logging-deny -m limit --limit 3/min --limit-burst 10 -j LOG --log-prefix "[UFW DOCKER BLOCK] "
-A ufw-docker-logging-deny -j DROP

COMMIT
# END UFW AND DOCKER (minato)'
}

# Comment of every UFW rule added by this script: each run deletes them and
# recreates the ones the options ask for, so the firewall follows the last run.
readonly UFW_TAG="minato-bootstrap"

# Prints the TLS mode of the Minato API: the existing configuration wins over
# the options, which only apply to a first server init.
api_tls_mode() {
  if [[ -f /etc/minato/config.yml ]]; then
    sed -n 's/^  tls: *"\{0,1\}\([a-z-]*\)"\{0,1\}.*/\1/p' /etc/minato/config.yml | head -n1
  elif [[ "$WITH_MINATO" == true && -n "$REPO" ]]; then
    echo "${TLS:-self-signed}"
  fi
}

setup_firewall() {
  step "Firewall"
  local p
  for p in "${EXTRA_PORTS[@]}"; do
    [[ "$p" =~ ^[0-9]+(/(tcp|udp))?$ ]] || die "invalid --allow-port $p"
  done

  # Delete the rules of a previous run, last first so the numbers stay valid.
  local n
  while read -r n; do
    ufw --force delete "$n" >/dev/null
  done < <(ufw status numbered 2>/dev/null | grep "# $UFW_TAG" | sed -E 's/^\[ *([0-9]+)\].*/\1/' | sort -rn)

  ufw default deny incoming >/dev/null
  ufw default allow outgoing >/dev/null
  ufw limit "$SSH_PORT/tcp" comment "$UFW_TAG ssh" >/dev/null
  local open="${SSH_PORT}/tcp (SSH, rate limited)"
  for p in "${EXTRA_PORTS[@]}"; do
    ufw allow "$p" comment "$UFW_TAG" >/dev/null
    open+=", $p"
  done
  case "$(api_tls_mode)" in
    acme)
      ufw allow 80/tcp comment "$UFW_TAG minato acme" >/dev/null
      ufw allow 443/tcp comment "$UFW_TAG minato" >/dev/null
      open+=", 80/tcp and 443/tcp (Minato, Let's Encrypt)" ;;
    off)
      [[ "$OPEN_API_PORT" == false ]] || warn "--open-api-port ignored: Minato listens on 127.0.0.1 (tls off)" ;;
    *)
      if [[ "$OPEN_API_PORT" == true ]]; then
        ufw allow 7373/tcp comment "$UFW_TAG minato" >/dev/null
        open+=", 7373/tcp (Minato)"
      fi ;;
  esac

  # Rewrite the Docker block on every run so it follows the options.
  local had_block=false
  if grep -q '^# BEGIN UFW AND DOCKER (minato)$' /etc/ufw/after.rules; then had_block=true; fi
  sed -i '/^# BEGIN UFW AND DOCKER (minato)$/,/^# END UFW AND DOCKER (minato)$/d' /etc/ufw/after.rules
  if [[ "$FILTER_DOCKER_PORTS" == true ]]; then
    printf '%s\n' "$(ufw_docker_rules)" >> /etc/ufw/after.rules
  elif [[ "$had_block" == true ]]; then
    # ufw reload leaves the rules it no longer declares in place.
    iptables -F DOCKER-USER 2>/dev/null || true
  fi
  ufw --force enable >/dev/null
  ufw reload >/dev/null
  info "deny incoming, allow $open"
  if [[ "$FILTER_DOCKER_PORTS" == true ]]; then
    info "container ports: only the --allow-port ones are reachable"
  else
    info "container ports: the ports: of your compose files decide"
  fi
}

setup_fail2ban() {
  step "Fail2ban"
  cat > /etc/fail2ban/jail.d/90-minato.local <<EOF
$MARK
[DEFAULT]
bantime = 1h
bantime.increment = true
bantime.maxtime = 1w
findtime = 10m
maxretry = 5
banaction = ufw[blocktype=deny]
backend = systemd

[sshd]
enabled = true
port = $SSH_PORT
mode = aggressive

[recidive]
enabled = true
backend = auto
logpath = /var/log/fail2ban.log
bantime = 1w
findtime = 1d
EOF
  systemctl enable fail2ban >/dev/null 2>&1
  systemctl restart fail2ban
  info "SSH bans from 1 hour, growing up to 1 week for repeat offenders"
}

# ---------------------------------------------------------------------------
# Docker
# ---------------------------------------------------------------------------

setup_docker() {
  step "Docker"
  if ! command -v docker >/dev/null || ! docker compose version >/dev/null 2>&1; then
    local pkg
    for pkg in docker.io docker-doc docker-compose docker-compose-v2 podman-docker containerd runc; do
      if dpkg -s "$pkg" >/dev/null 2>&1; then apt-get "${APT_OPTS[@]}" remove "$pkg"; fi
    done
    install -m 0755 -d /etc/apt/keyrings
    curl -fsSL "https://download.docker.com/linux/$OS_ID/gpg" -o /etc/apt/keyrings/docker.asc
    chmod a+r /etc/apt/keyrings/docker.asc
    printf 'deb [arch=%s signed-by=/etc/apt/keyrings/docker.asc] https://download.docker.com/linux/%s %s stable\n' \
      "$(dpkg --print-architecture)" "$OS_ID" "$OS_CODENAME" > /etc/apt/sources.list.d/docker.list
    apt-get update -q
    apt-get "${APT_OPTS[@]}" install docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin
  fi

  # Live restore keeps containers running while dockerd restarts, but Swarm
  # refuses it.
  local live_restore=true hardening=""
  [[ "$WITH_SWARM" == false ]] || live_restore=false
  if [[ "$DOCKER_HARDENING" == true ]]; then
    hardening='
  "no-new-privileges": true,
  "icc": false,'
  fi
  mkdir -p /etc/docker
  cat > /etc/docker/daemon.json.new <<EOF
{${hardening}
  "userland-proxy": false,
  "live-restore": ${live_restore},
  "log-driver": "local",
  "log-opts": { "max-size": "20m", "max-file": "5" }
}
EOF
  if ! cmp -s /etc/docker/daemon.json.new /etc/docker/daemon.json 2>/dev/null; then
    [[ ! -f /etc/docker/daemon.json ]] || cp /etc/docker/daemon.json "/etc/docker/daemon.json.$(date +%s).bak"
    mv /etc/docker/daemon.json.new /etc/docker/daemon.json
    systemctl enable docker >/dev/null 2>&1
    systemctl restart docker
  else
    rm -f /etc/docker/daemon.json.new
    systemctl enable --now docker >/dev/null 2>&1
  fi
  docker info >/dev/null || die "Docker does not start, see journalctl -u docker"
  info "$(docker --version), $(docker compose version --short 2>/dev/null | sed 's/^/compose /')"
  [[ "$DOCKER_HARDENING" == false ]] || info "no-new-privileges and icc=false for every container"
}

main_ipv4() {
  ip -4 route get 1.1.1.1 2>/dev/null | awk '{for (i = 1; i < NF; i++) if ($i == "src") { print $(i + 1); exit }}'
}

setup_swarm() {
  [[ "$WITH_SWARM" == true ]] || return 0
  step "Docker Swarm"
  if [[ "$(docker info --format '{{.Swarm.LocalNodeState}}')" == active ]]; then
    info "already part of a swarm"
    return
  fi
  local addr="${SWARM_ADDR:-$(main_ipv4)}"
  [[ -n "$addr" ]] || die "cannot find the main IP address, pass --swarm-addr"
  docker swarm init --advertise-addr "$addr" >/dev/null
  info "single node swarm on $addr (manager ports stay closed in the firewall)"
}

# ---------------------------------------------------------------------------
# Minato
# ---------------------------------------------------------------------------

# Daily automatic updates of Minato, through `minato update auto`.
setup_minato_auto_update() {
  if ! /usr/local/bin/minato update auto --help >/dev/null 2>&1; then
    warn "this Minato version cannot update itself, automatic updates skipped"
    return
  fi
  if [[ "$AUTO_UPDATE" == true ]]; then
    /usr/local/bin/minato update auto on --at 05:00 >/dev/null
    info "automatic updates every day around 05:00 (same major version)"
  else
    /usr/local/bin/minato update auto off >/dev/null
    info "automatic updates off"
  fi
}

setup_minato() {
  [[ "$WITH_MINATO" == true ]] || return 0
  step "Minato"
  if [[ -n "$MINATO_BINARY" ]]; then
    install -m 0755 "$MINATO_BINARY" /usr/local/bin/minato
  else
    curl -fsSL https://raw.githubusercontent.com/ermos/minato/main/install.sh \
      | MINATO_VERSION="$MINATO_VERSION" sh >/dev/null
  fi
  info "$(/usr/local/bin/minato --version)"
  setup_minato_auto_update

  [[ -n "$REPO" ]] || return 0
  if [[ -f /etc/minato/config.yml ]]; then
    info "/etc/minato/config.yml exists, server init skipped"
    return
  fi
  local args=(server init --repo "$REPO" --generate-ssh-key --systemd
    --admin "$ADMIN_USER" --admin-ssh-key "${KEYS[0]}" --no-password)
  [[ -z "$BRANCH" ]] || args+=(--branch "$BRANCH")
  [[ -z "$DOMAIN" ]] || args+=(--domain "$DOMAIN")
  [[ -z "$TLS" ]] || args+=(--tls "$TLS")
  /usr/local/bin/minato "${args[@]}"
  local k
  for k in "${KEYS[@]:1}"; do
    /usr/local/bin/minato user key add "$ADMIN_USER" "$k" >/dev/null
  done
  case "${TLS:-self-signed}" in
    off)
      info "API and webhooks on http://127.0.0.1:7373: route https://$DOMAIN to it from your reverse proxy" ;;
    acme)
      ufw allow 80/tcp comment "$UFW_TAG minato acme" >/dev/null
      ufw allow 443/tcp comment "$UFW_TAG minato" >/dev/null
      info "API and webhooks on https://$DOMAIN, ports 80 and 443 open" ;;
    *)
      if [[ "$OPEN_API_PORT" == true ]]; then
        info "API and webhooks on port 7373, open"
      else
        info "API on port 7373, closed: use a domain, --open-api-port or an SSH tunnel"
      fi ;;
  esac
}

# ---------------------------------------------------------------------------

summary() {
  step "Done"
  local ip
  ip="$(main_ipv4)"
  info "Log in from a NEW terminal before closing this one:"
  info ""
  local port_opt=""
  [[ "$SSH_PORT" == 22 ]] || port_opt="-p $SSH_PORT "
  info "    ssh ${port_opt}$ADMIN_USER@${ip:-<server>}"
  info ""
  if [[ "$WITH_MINATO" == true && -z "$REPO" ]]; then
    info "Then set up Minato:"
    info "    sudo minato server init --repo git@github.com:you/infra.git --generate-ssh-key \\"
    info "         --domain minato.example.com --tls off \\"
    info "         --admin $ADMIN_USER --admin-ssh-key @~/.ssh/authorized_keys --no-password --systemd"
    info "and route https://minato.example.com to http://127.0.0.1:7373 from your reverse proxy."
    info ""
  fi
  info "Full log: $LOG_FILE"
  if [[ -f /var/run/reboot-required ]]; then
    if [[ "$REBOOT_AT_END" == true ]]; then
      info "Rebooting to finish the upgrade..."
      systemctl reboot
    else
      warn "a reboot is needed to finish the upgrade: sudo reboot"
    fi
  fi
}

main() {
  parse_args "$@"
  touch "$LOG_FILE" 2>/dev/null && chmod 600 "$LOG_FILE" && exec > >(tee -a "$LOG_FILE") 2>&1
  trap 'die "failed at line $LINENO, see $LOG_FILE"' ERR
  printf '\n\033[1mMinato VPS bootstrap\033[0m (%s)\n' "$(date -u +%FT%TZ)"
  preflight
  upgrade_system
  setup_time
  setup_swap
  setup_unattended_upgrades
  setup_admin_user
  setup_sysctl
  setup_journald
  setup_apparmor
  setup_docker
  setup_swarm
  setup_firewall
  setup_fail2ban
  setup_minato
  setup_ssh
  summary
}

main "$@"
