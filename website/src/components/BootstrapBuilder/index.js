import React, { useMemo, useState } from 'react';
import CodeBlock from '@theme/CodeBlock';
import styles from './styles.module.css';

const SCRIPT_URL = 'https://ermos.github.io/minato/bootstrap.sh';

const API_MODES = [
  {
    value: 'proxy',
    label: 'Domain, behind my reverse proxy',
    help: 'Minato listens on 127.0.0.1:7373, your Traefik or Caddy serves it on the domain. Recommended.',
  },
  {
    value: 'acme',
    label: 'Domain, certificate by Minato',
    help: 'Minato gets a Let’s Encrypt certificate and takes ports 80 and 443. Only without a reverse proxy.',
  },
  {
    value: 'port',
    label: 'Direct port 7373',
    help: 'Self-signed certificate, pinned by minato login. The port is opened in the firewall.',
  },
  {
    value: 'private',
    label: 'Private',
    help: 'Port 7373 stays closed. Reach the API through an SSH tunnel, webhooks are unreachable.',
  },
];

// Quotes a value for the shell when it holds anything else than safe characters.
function quote(value) {
  if (/^[A-Za-z0-9_@%+=:,./-]+$/.test(value)) return value;
  return `'${value.replace(/'/g, `'\\''`)}'`;
}

function splitPorts(text) {
  return text
    .split(/[\s,]+/)
    .map((p) => p.trim())
    .filter(Boolean);
}

function Field({ label, help, children }) {
  return (
    <label className={styles.field}>
      <span className={styles.label}>{label}</span>
      {children}
      {help && <span className={styles.help}>{help}</span>}
    </label>
  );
}

function Toggle({ checked, onChange, label, help }) {
  return (
    <label className={styles.toggle}>
      <input type="checkbox" checked={checked} onChange={(e) => onChange(e.target.checked)} />
      <span>
        <span className={styles.label}>{label}</span>
        {help && <span className={styles.help}>{help}</span>}
      </span>
    </label>
  );
}

export default function BootstrapBuilder() {
  const [s, setState] = useState({
    user: '',
    sshKey: '',
    sshPort: '22',
    minato: true,
    repo: '',
    branch: '',
    apiMode: 'proxy',
    domain: '',
    hostProxy: false,
    ports: '',
    filterDocker: false,
    swarm: true,
    dockerHardening: true,
    timezone: 'UTC',
    autoReboot: true,
    rebootTime: '04:00',
    rebootAtEnd: false,
    swap: 'auto',
  });
  const set = (key) => (value) => setState((prev) => ({ ...prev, [key]: value }));
  const onText = (key) => (e) => set(key)(e.target.value);

  const usesDomain = s.apiMode === 'proxy' || s.apiMode === 'acme';

  const { command, notes } = useMemo(() => {
    const args = [['--user', s.user.trim() || 'you']];
    const notes = [];
    if (s.sshKey.trim()) args.push(['--ssh-key', s.sshKey.trim()]);
    if (s.sshPort.trim() && s.sshPort.trim() !== '22') args.push(['--ssh-port', s.sshPort.trim()]);

    const ports = splitPorts(s.ports);
    if (s.hostProxy) ports.unshift('80/tcp', '443/tcp');
    [...new Set(ports)].forEach((p) => args.push(['--allow-port', p]));
    if (s.filterDocker) args.push(['--filter-docker-ports']);

    if (!s.swarm) args.push(['--no-swarm']);
    if (!s.dockerHardening) args.push(['--no-docker-hardening']);

    if (s.timezone.trim() && s.timezone.trim() !== 'UTC') args.push(['--timezone', s.timezone.trim()]);
    if (s.swap !== 'auto') args.push(['--swap', s.swap]);
    if (!s.autoReboot) args.push(['--reboot-time', 'off']);
    else if (s.rebootTime && s.rebootTime !== '04:00') args.push(['--reboot-time', s.rebootTime]);
    if (s.rebootAtEnd) args.push(['--reboot']);

    if (!s.minato) {
      args.push(['--no-minato']);
    } else {
      if (s.repo.trim()) {
        args.push(['--repo', s.repo.trim()]);
        if (s.branch.trim() && s.branch.trim() !== 'main') args.push(['--branch', s.branch.trim()]);
        if (usesDomain) {
          args.push(['--domain', s.domain.trim() || 'minato.example.com']);
          if (s.apiMode === 'acme') args.push(['--tls', 'acme']);
        }
      } else {
        notes.push({ text: 'Without a repository, Minato is installed but not configured.' });
      }
      if (s.apiMode === 'port') args.push(['--open-api-port']);
      if (s.apiMode === 'acme' && (s.hostProxy || ports.includes('443/tcp'))) {
        notes.push({ text: 'Minato with its own certificate and a reverse proxy both want port 443: pick one.', warn: true });
      }
    }

    const lines = args.map(([flag, value]) => `  ${flag}${value === undefined ? '' : ` ${quote(value)}`}`);
    return {
      command: `curl -fsSL ${SCRIPT_URL} -o bootstrap.sh\nsudo bash bootstrap.sh \\\n${lines.join(' \\\n')}`,
      notes,
    };
  }, [s, usesDomain]);

  return (
    <div className={styles.builder}>
      <div className={styles.output}>
        <CodeBlock language="bash" title="Run on the server as root">
          {command}
        </CodeBlock>
        {notes.map((n) => (
          <p key={n.text} className={n.warn ? `${styles.note} ${styles.warn}` : styles.note}>
            {n.text}
          </p>
        ))}
      </div>
      <form className={styles.form} onSubmit={(e) => e.preventDefault()}>
        <fieldset className={styles.group}>
          <legend>Access</legend>
          <Field label="Admin user" help="Created with sudo. Root login over SSH is disabled.">
            <input type="text" value={s.user} onChange={onText('user')} placeholder="you" autoComplete="off" spellCheck="false" />
          </Field>
          <Field label="SSH public key" help="Empty: the keys already installed for root.">
            <input type="text" value={s.sshKey} onChange={onText('sshKey')} placeholder="ssh-ed25519 AAAA... you@laptop" autoComplete="off" spellCheck="false" />
          </Field>
          <Field label="SSH port">
            <input type="number" min="1" max="65535" value={s.sshPort} onChange={onText('sshPort')} />
          </Field>
        </fieldset>

        <fieldset className={styles.group}>
          <legend>Minato</legend>
          <Toggle checked={s.minato} onChange={set('minato')} label="Install Minato" />
          {s.minato && (
            <>
              <Field label="GitOps repository" help="Runs minato server init with a deploy key and the admin user.">
                <input type="text" value={s.repo} onChange={onText('repo')} placeholder="git@github.com:you/infra.git" autoComplete="off" spellCheck="false" />
              </Field>
              {s.repo.trim() && (
                <Field label="Branch">
                  <input type="text" value={s.branch} onChange={onText('branch')} placeholder="main" spellCheck="false" />
                </Field>
              )}
              <div className={styles.field} role="radiogroup" aria-label="Remote API and webhooks">
                <span className={styles.label}>Remote API and webhooks</span>
                {API_MODES.map((m) => (
                  <label key={m.value} className={styles.radio}>
                    <input type="radio" name="apiMode" value={m.value} checked={s.apiMode === m.value} onChange={() => set('apiMode')(m.value)} />
                    <span>
                      <span>{m.label}</span>
                      <span className={styles.help}>{m.help}</span>
                    </span>
                  </label>
                ))}
              </div>
              {usesDomain && s.repo.trim() && (
                <Field label="API domain" help="Points to this server, served by your reverse proxy or by Minato.">
                  <input type="text" value={s.domain} onChange={onText('domain')} placeholder="minato.example.com" spellCheck="false" />
                </Field>
              )}
            </>
          )}
        </fieldset>

        <fieldset className={styles.group}>
          <legend>Firewall</legend>
          <Toggle
            checked={s.hostProxy}
            onChange={set('hostProxy')}
            label="Open 80 and 443 on the server"
            help="For a reverse proxy in host network mode. A proxy publishing its ports with ports: does not need it."
          />
          <Field label="Other ports" help="Separated by commas, for example 8443/tcp, 51820/udp.">
            <input type="text" value={s.ports} onChange={onText('ports')} placeholder="none" spellCheck="false" />
          </Field>
          <Toggle
            checked={s.filterDocker}
            onChange={set('filterDocker')}
            label="Filter container ports too"
            help="Off: the ports: of your compose files decide. On: only the ports above are reachable."
          />
        </fieldset>

        <fieldset className={styles.group}>
          <legend>Docker</legend>
          <Toggle checked={s.swarm} onChange={set('swarm')} label="Single node Swarm" />
          <Toggle
            checked={s.dockerHardening}
            onChange={set('dockerHardening')}
            label="Container hardening"
            help="no-new-privileges and icc=false for every container."
          />
        </fieldset>

        <fieldset className={styles.group}>
          <legend>System</legend>
          <Field label="Time zone">
            <input type="text" value={s.timezone} onChange={onText('timezone')} placeholder="UTC" spellCheck="false" />
          </Field>
          <Field label="Swap file">
            <select value={s.swap} onChange={onText('swap')}>
              <option value="auto">Auto (2G under 4 GB of RAM)</option>
              <option value="0">None</option>
              <option value="1G">1G</option>
              <option value="2G">2G</option>
              <option value="4G">4G</option>
            </select>
          </Field>
          <Toggle checked={s.autoReboot} onChange={set('autoReboot')} label="Reboot automatically after updates that need it" />
          {s.autoReboot && (
            <Field label="Reboot time">
              <input type="time" value={s.rebootTime} onChange={onText('rebootTime')} />
            </Field>
          )}
          <Toggle checked={s.rebootAtEnd} onChange={set('rebootAtEnd')} label="Reboot at the end of the script if needed" />
        </fieldset>
      </form>

    </div>
  );
}
