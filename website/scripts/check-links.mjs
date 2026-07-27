#!/usr/bin/env node
/**
 * Link checker for the built site.
 *
 * Run after `astro build`, against `dist/`:
 *   node scripts/check-links.mjs              internal links and anchors
 *   node scripts/check-links.mjs --external   also probe outbound URLs
 *
 * Internal checking is fast and deterministic, so it runs on every build. It
 * catches the failure mode that actually bites here, which is a page being
 * renamed or removed while something still links to it, and an anchor that no
 * longer matches a heading.
 *
 * External checking touches the network, so it is opt in and runs on a
 * schedule rather than on every pull request. Otherwise an unrelated site
 * being slow would fail an unrelated change.
 */
import { readFileSync, existsSync } from 'node:fs';
import { readdir } from 'node:fs/promises';
import { join, relative, dirname } from 'node:path';

const DIST = join(import.meta.dirname, '..', 'dist');
const BASE = process.env.BASE_PATH ?? '/semantic-operator';
const CHECK_EXTERNAL = process.argv.includes('--external');

// Hosts that legitimately fail from CI. The project's own repository is
// private until launch, so every link to it 404s for an anonymous fetch.
const EXTERNAL_IGNORE = [
  /^https:\/\/github\.com\/KubedAI\/semantic-operator/,
  // The site's own canonical and sitemap URLs, which only resolve once the
  // site is actually published.
  /^https:\/\/kubedai\.github\.io/,
  /<[a-z-]+>/i, // template URLs containing placeholders such as <region>
  /^https?:\/\/localhost/,
  /^https?:\/\/127\./,
];

const ASSET = /\.(svg|png|jpe?g|webp|ico|css|js|xml|json|txt|woff2?)$/i;

if (!existsSync(DIST)) {
  console.error('dist/ not found. Run `npm run build` first.');
  process.exit(1);
}

async function htmlFiles(dir) {
  const out = [];
  for (const entry of await readdir(dir, { withFileTypes: true })) {
    const full = join(dir, entry.name);
    if (entry.isDirectory()) out.push(...(await htmlFiles(full)));
    else if (entry.name.endsWith('.html')) out.push(full);
  }
  return out;
}

const files = await htmlFiles(DIST);

/** Built page URL -> the set of element ids it contains. */
const pages = new Map();
for (const file of files) {
  const rel = relative(DIST, dirname(file));
  const url = BASE + (rel === '' ? '' : '/' + rel);
  const html = readFileSync(file, 'utf8');
  const ids = new Set([...html.matchAll(/\sid="([^"]+)"/g)].map((m) => m[1]));
  pages.set(url, ids);
}

const decode = (s) =>
  s.replace(/&amp;/g, '&').replace(/&lt;/g, '<').replace(/&gt;/g, '>').replace(/&quot;/g, '"');

const problems = [];
const external = new Set();

for (const file of files) {
  const from = relative(DIST, dirname(file)) || '(home)';
  const html = readFileSync(file, 'utf8');
  for (const m of html.matchAll(/href="([^"]+)"/g)) {
    const href = decode(m[1]);

    if (/^(https?:)?\/\//.test(href)) {
      if (href.startsWith('http')) external.add(href);
      continue;
    }
    if (href.startsWith('mailto:') || href.startsWith('#') || !href.startsWith('/')) continue;
    if (ASSET.test(href)) continue;

    const [rawPath, fragment] = href.split('#');
    const path = rawPath.replace(/\/$/, '') || BASE;

    if (!pages.has(path)) {
      problems.push({ from, href, why: 'page does not exist' });
    } else if (fragment && !pages.get(path).has(fragment)) {
      problems.push({ from, href, why: 'anchor does not exist on that page' });
    }
  }
}

console.log(`checked ${files.length} pages for internal links`);

if (CHECK_EXTERNAL) {
  const targets = [...external].filter((u) => !EXTERNAL_IGNORE.some((re) => re.test(u)));
  console.log(`probing ${targets.length} external links`);
  await Promise.all(
    targets.map(async (url) => {
      try {
        const res = await fetch(url, {
          redirect: 'follow',
          signal: AbortSignal.timeout(15000),
          headers: { 'user-agent': 'semantic-operator-docs-linkcheck' },
        });
        // Some sites reject HEAD-ish automated traffic; only hard 4xx matters.
        if (res.status >= 400 && res.status !== 403 && res.status !== 429) {
          problems.push({ from: '(external)', href: url, why: `HTTP ${res.status}` });
        }
      } catch (err) {
        problems.push({ from: '(external)', href: url, why: err.name ?? 'request failed' });
      }
    })
  );
}

if (problems.length === 0) {
  console.log('no broken links');
  process.exit(0);
}

console.error(`\n${problems.length} broken link(s):\n`);
for (const p of problems) {
  console.error(`  ${p.from}`);
  console.error(`    ${p.href}`);
  console.error(`    ${p.why}\n`);
}
process.exit(1);
