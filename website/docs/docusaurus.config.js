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

// The static marketing site lives one level above the docs base path.
//
// The `pathname://` prefix marks the link as "not part of this app", so
// Docusaurus emits a plain <a> instead of a React Router link. Without it the
// click is handled client-side, the site root does not match any docs route,
// and the docs 404 page renders over the marketing home. `autoAddBaseUrl`
// must be off or Docusaurus prepends the docs base path back onto the path.
const siteRootHref = `pathname://${baseUrl.replace(/[^/]+\/$/, '')}`;
const homeLinkProps = {autoAddBaseUrl: false, target: '_self', className: 'enclave-home-link'};

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

  // Links that leave the docs route tree must use `pathname://` (see
  // siteRootHref); anything else Docusaurus reports here is a genuine break.
  onBrokenLinks: 'throw',
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
      image: 'img/social-card.png',
      colorMode: {
        // Match the light marketing site; the toggle is still available.
        defaultMode: 'light',
        respectPrefersColorScheme: false,
      },
      navbar: {
        title: 'Eclipse Enclave',
        logo: {
          alt: 'Eclipse Enclave logo',
          src: 'img/enclave-mark-lightbg.svg',
          srcDark: 'img/enclave-mark-darkbg.svg',
        },
        items: [
          {
            type: 'docSidebar',
            sidebarId: 'docsSidebar',
            position: 'left',
            label: 'Docs',
          },
          {
            href: siteRootHref,
            label: 'Home',
            position: 'right',
            ...homeLinkProps,
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
        // The footer is always dark, so it uses the dark-background lockup
        // regardless of the active color mode. It stays unlinked: the footer
        // logo cannot opt out of `autoAddBaseUrl`, so a `pathname://` href
        // would be rewritten back under the docs base path.
        logo: {
          alt: 'Eclipse Enclave',
          src: 'img/enclave-logo-horizontal-darkbg.svg',
          width: 168,
        },
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
              {label: 'Home', href: siteRootHref, ...homeLinkProps},
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
