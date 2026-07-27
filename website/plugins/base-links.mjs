/**
 * Prefix root-relative links with the site's base path.
 *
 * Astro serves this site under a base (`/semantic-operator` on GitHub Pages).
 * Starlight rewrites the links it generates itself, such as the sidebar, but a
 * plain markdown link written as `/start/quickstart` is emitted verbatim and
 * 404s once a base is in play.
 *
 * Authors should keep writing root-relative links, because they read well in
 * source and survive a change of base. This plugin does the rewriting at build
 * time so nobody has to remember.
 *
 * Left alone: absolute URLs, protocol-relative URLs, anchors, and anything
 * already carrying the base.
 */
export function rehypeBaseLinks(base) {
  const prefix = base === '/' ? '' : base.replace(/\/$/, '');
  if (!prefix) return () => () => {};

  const needsPrefix = (href) =>
    typeof href === 'string' &&
    href.startsWith('/') &&
    !href.startsWith('//') &&
    !href.startsWith(prefix + '/') &&
    href !== prefix;

  return () => (tree) => {
    const walk = (node) => {
      if (node.type === 'element') {
        if (node.tagName === 'a' && needsPrefix(node.properties?.href)) {
          node.properties.href = prefix + node.properties.href;
        }
        // Images and other src-bearing elements need the same treatment.
        if (needsPrefix(node.properties?.src)) {
          node.properties.src = prefix + node.properties.src;
        }
      }
      for (const child of node.children ?? []) walk(child);
    };
    walk(tree);
  };
}
