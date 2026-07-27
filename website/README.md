# Documentation site

The docs at `docs.semantic-operator` are an [Astro](https://astro.build/) site using the
[Starlight](https://starlight.astro.build/) theme. Everything here is MIT licensed and runs
offline. There is no account, no CMS, and no build service involved.

The site is also the build spec for the project. If behaviour and these pages disagree, one
of the two is a bug.

## Run it locally

You need Node 22.12 or newer. Check with `node --version`.

```bash
cd website
npm install
npm run dev
```

That serves the site at http://localhost:4321/semantic-operator with hot reload. Edit any
page and the browser updates without a restart. The `/semantic-operator` path is there
because the published site lives under a repository subpath on GitHub Pages, and keeping it
locally means links behave the same in both places.

To see exactly what CI publishes, build and preview the real output instead.

```bash
npm run build
npm run preview
```

One dev server message is expected and harmless. Browsers ask the origin root
for `/favicon.ico` no matter which icons a page declares, and that path sits
outside the base, so the router logs that it could not match it. The page
itself returns 200. Vite serves the public directory before any hook the site
can register, so there is nowhere to intercept the request, and it does not
happen in production because the origin root is a different host.

## Check your links before pushing

The build does not fail on a link to a page that no longer exists, so there is a separate
checker. It runs against `dist/`, so build first.

```bash
npm run build
npm run check-links
```

It verifies two things. Every internal link points at a page that was actually built, and
every `#anchor` matches a heading that actually exists on the target page. Renaming a page
or a heading is the usual way to break these, and both are silent otherwise.

Outbound links are checked separately because they touch the network and an unrelated site
being slow should not fail your change.

```bash
npm run check-links:external
```

CI runs the internal check on every pull request and the external check weekly.

## Where things live

Pages are Markdown in `src/content/docs/`, and the file path becomes the URL. A file at
`src/content/docs/guides/authoring.md` serves at `/guides/authoring`. Sidebar order comes
from `astro.config.mjs` rather than from the filenames, so adding a page means adding it
there too.

`src/components/` holds the hand written SVG diagrams. They are inline SVG rather than
images so they stay editable text, they diff properly in review, and they pick up the site
colours in both light and dark mode. Colours come from the tokens in `src/styles/theme.css`
and are never hardcoded in a diagram.

`plugins/base-links.mjs` rewrites root relative links so they keep working under the
`/semantic-operator` subpath. Write links as `/guides/authoring` and the plugin handles the
prefix. In a component prop, where the plugin does not reach, use
`import.meta.env.BASE_URL` instead.

`public/` is copied to the site root as is.

## House style

Short declarative sentences. No semicolons, no colons inside a sentence, no em dashes.
Plain words over impressive ones. Link text names the page it goes to rather than the file
it lives in.

Every command shown should be one somebody can paste and run. If a step needs a value the
reader has to supply, use an obvious placeholder such as `<acct>` rather than a real looking
value they might paste by mistake.
