/********************************************************************************
 * Copyright (C) 2026 EclipseSource GmbH and others.
 *
 * This program and the accompanying materials are made available under the
 * terms of the MIT License, which is available in the project root.
 *
 * SPDX-License-Identifier: MIT
 ********************************************************************************/

// @ts-check
// Docusaurus config for the Eclipse Enclave documentation site.
// See: https://docusaurus.io/docs/api/docusaurus-config

import {themes as prismThemes} from 'prism-react-renderer';

// --------------------------------------------------------------------------
// Deployment base path.
//
// The docs are served under `<site-root>/docs/`. Production serves the site at
// the root of https://enclave.eclipse.dev, so the default '/docs/' is what
// ships. This is the ONE place to adjust the base path; nothing else hardcodes
// it.
//
// If the whole site is served under a subpath (e.g. the PR previews under
// eclipse-enclave.github.io/enclave-website-previews/pr-previews/pr-42/), set
// DOCS_BASE_URL to '<subpath>/docs/' at build time.
// --------------------------------------------------------------------------
const baseUrl = process.env.DOCS_BASE_URL || '/docs/';

/** @type {import('@docusaurus/types').Config} */
const config = {
  title: 'Eclipse Enclave',
  tagline: 'Secure sandboxing for autonomous AI coding agents',
  favicon: 'img/favicon.png',

  future: {
    v4: true,
  },

  // `url` is only used for absolute-URL generation (sitemap, canonical tags).
  // It is not hardcoded into navigation. Update alongside the real domain.
  url: 'https://enclave.eclipse.dev',
  baseUrl,

  // 'warn' (not 'throw') because the docs live at /docs/ under a larger site and
  // intentionally link "up" to the marketing home via a relative `../` link,
  // which sits outside Docusaurus's owned route tree.
  onBrokenLinks: 'warn',
  markdown: {
    hooks: {
      onBrokenMarkdownLinks: 'warn',
    },
  },

  i18n: {
    defaultLocale: 'en',
    locales: ['en'],
  },

  presets: [
    [
      'classic',
      /** @type {import('@docusaurus/preset-classic').Options} */
      ({
        docs: {
          // Serve docs at the site root (which is already /docs/ via baseUrl),
          // so pages live at /docs/<id> rather than /docs/docs/<id>.
          routeBasePath: '/',
          sidebarPath: './sidebars.js',
          editUrl: 'https://github.com/eclipse-enclave/enclave/tree/main/website/docs/',
        },
        blog: false,
        theme: {
          customCss: './src/css/custom.css',
        },
      }),
    ],
  ],

  themeConfig:
    /** @type {import('@docusaurus/preset-classic').ThemeConfig} */
    ({
      image: 'img/logo.png',
      colorMode: {
        // Match the light marketing site; the toggle is still available.
        defaultMode: 'light',
        respectPrefersColorScheme: false,
      },
      navbar: {
        title: 'Eclipse Enclave',
        logo: {
          alt: 'Eclipse Enclave logo',
          src: 'img/logo.png',
        },
        items: [
          {
            type: 'docSidebar',
            sidebarId: 'docsSidebar',
            position: 'left',
            label: 'Docs',
          },
          {
            // Relative link back to the marketing site (one level above /docs/).
            href: '../',
            label: 'Home',
            position: 'right',
            target: '_self',
          },
          {
            href: 'https://github.com/eclipse-enclave/enclave',
            label: 'GitHub',
            position: 'right',
          },
        ],
      },
      footer: {
        style: 'dark',
        links: [
          {
            title: 'Docs',
            items: [
              {label: 'Getting Started', to: '/getting-started'},
              {label: 'Introduction', to: '/'},
              {label: 'Support', to: '/support'},
            ],
          },
          {
            title: 'Project',
            items: [
              {label: 'Home', href: '../'},
              {label: 'Eclipse Project', href: 'https://projects.eclipse.org/projects/ecd.enclave'},
              {label: 'GitHub', href: 'https://github.com/eclipse-enclave/enclave'},
            ],
          },
          {
            title: 'Legal',
            items: [
              {label: 'Terms of Use', href: 'https://www.eclipse.org/legal/terms-of-use/'},
              {label: 'Privacy Policy', href: 'https://www.eclipse.org/legal/privacy/'},
              {label: 'Code of Conduct', href: 'https://www.eclipse.org/org/documents/community-code-of-conduct/'},
              {label: 'Communication Guidelines', href: 'https://www.eclipse.org/org/documents/communication-channel-guidelines/'},
            ],
          },
        ],
        copyright: `Eclipse Enclave. Open source under the MIT license.`,
      },
      prism: {
        theme: prismThemes.oneLight,
        darkTheme: prismThemes.oneDark,
        additionalLanguages: ['bash', 'yaml', 'json', 'docker'],
      },
    }),
};

export default config;
