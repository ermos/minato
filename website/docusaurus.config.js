// @ts-check
import { themes as prismThemes } from 'prism-react-renderer';

const repo = 'https://github.com/ermos/minato';

/** @type {import('@docusaurus/types').Config} */
const config = {
  title: 'Minato',
  tagline: 'GitOps for Docker Compose and Swarm. Push to git, your server follows.',
  favicon: 'img/seal.svg',

  url: 'https://ermos.github.io',
  baseUrl: '/minato/',
  organizationName: 'ermos',
  projectName: 'minato',
  trailingSlash: false,

  onBrokenLinks: 'throw',
  markdown: {
    hooks: { onBrokenMarkdownLinks: 'throw' },
  },

  i18n: { defaultLocale: 'en', locales: ['en'] },

  headTags: [
    { tagName: 'link', attributes: { rel: 'preconnect', href: 'https://fonts.googleapis.com' } },
    { tagName: 'link', attributes: { rel: 'preconnect', href: 'https://fonts.gstatic.com', crossorigin: 'anonymous' } },
  ],
  stylesheets: [
    'https://fonts.googleapis.com/css2?family=IBM+Plex+Mono:wght@400;500&family=Shippori+Mincho+B1:wght@500;700;800&family=Zen+Kaku+Gothic+New:wght@400;500;700&display=swap',
  ],

  presets: [
    [
      'classic',
      /** @type {import('@docusaurus/preset-classic').Options} */
      ({
        docs: {
          sidebarPath: './sidebars.js',
          editUrl: `${repo}/edit/main/website/`,
        },
        blog: false,
        theme: { customCss: './src/css/custom.css' },
      }),
    ],
  ],

  themeConfig:
    /** @type {import('@docusaurus/preset-classic').ThemeConfig} */
    ({
      image: 'img/social-card.png',
      metadata: [
        { name: 'keywords', content: 'gitops, docker, docker compose, docker swarm, self-hosted, vps, secrets, sops, age, webhooks' },
      ],
      colorMode: { respectPrefersColorScheme: true },
      navbar: {
        title: 'Minato',
        logo: { alt: 'Minato seal', src: 'img/seal.svg' },
        items: [
          { type: 'docSidebar', sidebarId: 'docs', position: 'left', label: 'Docs' },
          { to: '/docs/reference/cli', label: 'CLI', position: 'left' },
          { href: `${repo}/releases`, label: 'Releases', position: 'right' },
          { href: repo, label: 'GitHub', position: 'right' },
        ],
      },
      footer: {
        links: [
          {
            title: 'Learn',
            items: [
              { label: 'Introduction', to: '/docs/intro' },
              { label: 'Quick start', to: '/docs/getting-started/quick-start' },
              { label: 'Secrets', to: '/docs/guides/secrets' },
            ],
          },
          {
            title: 'Project',
            items: [
              { label: 'GitHub', href: repo },
              { label: 'Releases', href: `${repo}/releases` },
              { label: 'Contributing', to: '/docs/contributing' },
            ],
          },
        ],
        copyright: `Minato is free software under the GNU AGPL v3.`,
      },
      prism: {
        theme: prismThemes.github,
        darkTheme: prismThemes.vsDark,
        additionalLanguages: ['bash', 'yaml', 'json'],
      },
    }),
};

export default config;
