// @ts-check
import { defineConfig } from 'astro/config';
import starlight from '@astrojs/starlight';

import mdx from '@astrojs/mdx';
import { rehypeBaseLinks } from './plugins/base-links.mjs';

// GitHub Pages serves a project repo under /<repo>. Override both when moving
// to a custom domain: SITE_URL=https://docs.example.com BASE_PATH=/ npm run build
const SITE = process.env.SITE_URL ?? 'https://kubedai.github.io';
const BASE = process.env.BASE_PATH ?? '/semantic-operator';

export default defineConfig({
  site: SITE,
  base: BASE,
  trailingSlash: 'ignore',
  // Root-relative links in markdown are written without the base and rewritten
  // here, so authors do not have to repeat it and a change of base is one edit.
  markdown: { rehypePlugins: [rehypeBaseLinks(BASE)] },
  integrations: [starlight({
    title: 'Semantic Operator',
    logo: { src: './src/assets/mark.svg' },
    favicon: '/favicon.svg',
    description:
      'A Kubernetes operator and server that compile certified business metrics into deterministic, governed SQL for AI agents, apps, and BI tools.',
    social: [
      { icon: 'github', label: 'GitHub', href: 'https://github.com/KubedAI/semantic-operator' },
    ],
    customCss: ['./src/styles/theme.css'],
    components: { Hero: './src/components/Hero.astro' },
    editLink: {
      baseUrl: 'https://github.com/KubedAI/semantic-operator/edit/main/website/',
    },
    lastUpdated: true,
    tableOfContents: { minHeadingLevel: 2, maxHeadingLevel: 3 },
    sidebar: [
      {
        label: 'Start here',
        items: [
          { label: 'What a semantic layer is', slug: 'start/introduction' },
          { label: 'Quickstart', slug: 'start/quickstart' },
          { label: 'How Semantic Operator compares', slug: 'start/how-this-compares' },
        ],
      },
      {
        label: 'Architecture',
        items: [
          { label: 'How it works', slug: 'architecture' },
          { label: 'The ossiectl CLI', slug: 'architecture/ossiectl' },
          { label: 'The operator', slug: 'architecture/operator' },
          { label: 'The semantic server', slug: 'architecture/server' },
          { label: 'Identity and the engine', slug: 'architecture/identity' },
          { label: 'Access and credentials', slug: 'architecture/access' },
          { label: 'Semantic layers and ontologies', slug: 'architecture/ontology' },
        ],
      },
      {
        label: 'Guides',
        items: [
          { label: 'Authoring a model', slug: 'guides/authoring' },
          { label: 'Developing and testing', slug: 'guides/developing' },
          { label: 'Adding a query engine', slug: 'guides/adding-an-engine' },
          { label: 'Adding a catalog source', slug: 'guides/adding-a-catalog' },
        ],
      },
      {
        label: 'Reference',
        items: [
          { label: 'Configuration and deployment', slug: 'reference/configuration' },
        ],
      },
      {
        label: 'Examples',
        items: [
          { label: 'Choosing an example', slug: 'examples' },
          { label: 'Prerequisites', slug: 'examples/prerequisites' },
          { label: 'Retail on Glue and StarRocks', slug: 'examples/glue-starrocks' },
          { label: 'Retail on Glue and Trino', slug: 'examples/glue-trino' },
          { label: 'DataHub, Polaris and Trino', slug: 'examples/datahub-polaris-trino' },
          { label: 'Everything on your laptop', slug: 'examples/kind' },
          { label: 'The flights model', slug: 'examples/flights' },
          { label: 'Benchmark results', slug: 'examples/benchmark-results' },
        ],
      },
    ],
  }), mdx()],
});