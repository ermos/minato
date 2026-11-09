import React, { useState } from 'react';
import Link from '@docusaurus/Link';
import useBaseUrl from '@docusaurus/useBaseUrl';
import Layout from '@theme/Layout';
import Waves from '../components/Waves';
import styles from './index.module.css';

const INSTALL = 'curl -fsSL https://raw.githubusercontent.com/ermos/minato/main/install.sh | sudo sh';

const TREE = [
  ['infra/', 'Your repository, the single source of truth.'],
  ['├── .minato.yml', 'The keys allowed to read the secrets.'],
  ['└── stacks/blog/', 'One folder, one stack.'],
  ['    ├── compose.yml', 'A regular compose file. Nothing new to learn.'],
  ['    ├── minato.yml', 'Variables, environment, deploy and webhook settings.'],
  ['    ├── secrets.enc.yml', 'Encrypted values, readable keys, clean diffs.'],
  ['    └── files/', 'Mounted with ./files. Services restart when a file changes.'],
];

const STEPS = [
  ['Commit', 'Edit a compose file, a template or a secret, and push it.'],
  [
    'Render',
    'Minato pulls the branch, decrypts and interpolates every stack in memory, and fingerprints the result.',
  ],
  [
    'Deploy',
    'Only the stacks that changed are deployed. Services that mount a changed file are restarted, the rest keep running.',
  ],
];

const FEATURES = [
  {
    title: 'Secrets live in git, encrypted',
    text: 'Keys stay readable so reviews make sense. Values are encrypted with age, for the server and the people you list. A diff only shows the values that really changed.',
    file: 'stacks/blog/secrets.enc.yml',
    code: `DB_PASSWORD: ENC[v1,ziT+AOT4N9OAQvyg5Iv7...]
HOOK_TOKEN: ENC[v1,6FIrISb26J9nj6tHA60A...]
_minato:
  recipients: [age1server..., age1alice...]
  mac: a30163217d1efac2ba21a4e78e0bf48a...`,
    link: ['/docs/guides/secrets', 'How secrets work'],
  },
  {
    title: 'Change a secret without touching the server',
    text: 'The server decrypts with its own key, commits the change with you as author, pushes it and redeploys. The git history is the audit log.',
    file: 'your laptop',
    code: `minato secrets set blog DB_PASSWORD --random 32
minato secrets ls blog
minato secrets rotate blog --value HOOK_TOKEN`,
    link: ['/docs/guides/secrets#two-ways-to-edit', 'Remote secrets'],
  },
  {
    title: 'Webhooks for your CI',
    text: 'Pull a new image and restart one service right after your build, and let the job fail if the deploy fails.',
    file: '.github/workflows/release.yml',
    code: `- run: |
    curl -fsS -X POST \\
      -H "X-Minato-Token: \${{ secrets.MINATO_HOOK }}" \\
      "https://vps.example.com:7373/hooks/blog/release?wait=1"`,
    link: ['/docs/guides/webhooks', 'Webhooks'],
  },
  {
    title: 'Log in with the SSH key you already have',
    text: 'SSH keys, SSH certificates from your CA, or passwords you can turn off. Three roles decide who can read, deploy, or see secret values.',
    file: 'your laptop',
    code: `minato login vps.example.com -u alice
minato status
minato logs blog -f`,
    link: ['/docs/remote/users', 'Users and login'],
  },
];

function CopyCommand({ command }) {
  const [copied, setCopied] = useState(false);
  const copy = async () => {
    try {
      await navigator.clipboard.writeText(command);
      setCopied(true);
      setTimeout(() => setCopied(false), 2000);
    } catch {
      setCopied(false);
    }
  };
  return (
    <div className={styles.install}>
      <code className={styles.installCode}>{command}</code>
      <button type="button" className={styles.copy} onClick={copy} aria-live="polite">
        {copied ? 'Copied' : 'Copy'}
      </button>
    </div>
  );
}

function Hero() {
  return (
    <header className={styles.hero}>
      <div className={styles.heroInner}>
        <div className={styles.heroText}>
          <h1 className={styles.headline}>
            Push to git.
            <br />
            Your server follows.
          </h1>
          <p className={styles.lead}>
            Minato keeps the Docker Compose and Swarm stacks of your server in sync with a git
            repository. Secrets stay encrypted in the repo, files travel with the compose file, and
            a single binary does the rest.
          </p>
          <CopyCommand command={INSTALL} />
          <div className={styles.actions}>
            <Link className={styles.primary} to="/docs/getting-started/quick-start">
              Follow the quick start
            </Link>
            <Link className={styles.secondary} href="https://github.com/ermos/minato">
              View on GitHub
            </Link>
          </div>
        </div>
        <div className={styles.mark} aria-hidden="true">
          <span className={styles.kanji}>港</span>
          <span className={styles.reading}>みなと</span>
          <img className={styles.seal} src={useBaseUrl('/img/seal.svg')} alt="" />
        </div>
      </div>
      <Waves className={styles.waves} />
    </header>
  );
}

function Anatomy() {
  return (
    <section className={styles.section}>
      <div className={styles.container}>
        <h2 className={styles.title}>A stack is a folder</h2>
        <p className={styles.intro}>
          No new format to learn. Your compose files stay compose files, and everything a stack
          needs sits right next to it.
        </p>
        <dl className={styles.tree}>
          {TREE.map(([path, note]) => (
            <div className={styles.treeRow} key={path}>
              <dt className={styles.treePath}>{path}</dt>
              <dd className={styles.treeNote}>{note}</dd>
            </div>
          ))}
        </dl>
      </div>
    </section>
  );
}

function Flow() {
  return (
    <section className={`${styles.section} ${styles.flowSection}`}>
      <div className={styles.container}>
        <h2 className={styles.title}>From push to running</h2>
        <ol className={styles.flow}>
          {STEPS.map(([name, text]) => (
            <li className={styles.step} key={name}>
              <h3 className={styles.stepName}>{name}</h3>
              <p>{text}</p>
            </li>
          ))}
        </ol>
        <p className={styles.note}>
          Every minute by default, or right away when a webhook calls. A stack that fails to render
          keeps running its last good version.
        </p>
      </div>
    </section>
  );
}

function Features() {
  return (
    <section className={styles.section}>
      <div className={styles.container}>
        <h2 className={styles.title}>Built for a server you look after yourself</h2>
        <div className={styles.features}>
          {FEATURES.map((f) => (
            <article className={styles.feature} key={f.title}>
              <div className={styles.featureText}>
                <h3>{f.title}</h3>
                <p>{f.text}</p>
                <Link to={f.link[0]}>{f.link[1]}</Link>
              </div>
              <figure className={styles.snippet}>
                <figcaption>{f.file}</figcaption>
                <pre>
                  <code>{f.code}</code>
                </pre>
              </figure>
            </article>
          ))}
        </div>
      </div>
    </section>
  );
}

function Without() {
  return (
    <section className={styles.section}>
      <div className={`${styles.container} ${styles.without}`}>
        <h2 className={styles.title}>What you will not run</h2>
        <p className={styles.withoutText}>
          No dashboard exposed to the internet. No database to back up. No agent on every host, no
          Kubernetes. One binary, one git repository, and the Docker you already have.
        </p>
      </div>
    </section>
  );
}

function Dock() {
  return (
    <section className={styles.dock}>
      <div className={styles.container}>
        <h2 className={styles.dockTitle}>Dock your first stack</h2>
        <div className={styles.dockGrid}>
          <figure className={styles.snippet}>
            <figcaption>your laptop</figcaption>
            <pre>
              <code>{`minato init infra
cd infra && git remote add origin git@github.com:you/infra.git
git push -u origin main`}</code>
            </pre>
          </figure>
          <figure className={styles.snippet}>
            <figcaption>a fresh VPS: hardened, with Docker, Swarm and Minato</figcaption>
            <pre>
              <code>{`curl -fsSL https://ermos.github.io/minato/bootstrap.sh -o bootstrap.sh
sudo bash bootstrap.sh --user you \\
  --repo git@github.com:you/infra.git`}</code>
            </pre>
          </figure>
        </div>
        <div className={styles.actions}>
          <Link className={styles.primaryOnDark} to="/docs/getting-started/quick-start">
            Follow the quick start
          </Link>
          <Link className={styles.secondaryOnDark} to="/docs/intro">
            Read the docs
          </Link>
        </div>
      </div>
    </section>
  );
}

export default function Home() {
  return (
    <Layout
      title="GitOps for Docker Compose and Swarm"
      description="Minato keeps the Docker Compose and Swarm stacks of your VPS in sync with a git repository, with encrypted secrets, attached files, webhooks and a remote CLI.">
      <Hero />
      <main>
        <Anatomy />
        <Flow />
        <Features />
        <Without />
        <Dock />
      </main>
    </Layout>
  );
}
