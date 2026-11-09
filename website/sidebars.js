// @ts-check

/** @type {import('@docusaurus/plugin-content-docs').SidebarsConfig} */
const sidebars = {
  docs: [
    'intro',
    {
      type: 'category',
      label: 'Getting started',
      collapsed: false,
      items: ['getting-started/installation', 'getting-started/quick-start'],
    },
    {
      type: 'category',
      label: 'Guides',
      collapsed: false,
      items: [
        'guides/repository',
        'guides/stack-settings',
        'guides/interpolation',
        'guides/secrets',
        'guides/files',
        'guides/webhooks',
      ],
    },
    {
      type: 'category',
      label: 'Remote access',
      items: ['remote/server', 'remote/users', 'remote/client'],
    },
    {
      type: 'category',
      label: 'Reference',
      items: ['reference/cli', 'reference/server-config', 'reference/reconciliation', 'reference/security'],
    },
    'contributing',
  ],
};

export default sidebars;
