# Diagram sources

The docs reference rendered images in [`../img/`](../img/). The original Mermaid
sources are kept here as `.mmd` files so the intent is preserved and the images
can be regenerated or redrawn.

| Source | Rendered image | Used in |
|---|---|---|
| `architecture-overview.mmd` | `../img/architecture-overview.svg` | `README.md` |
| `system-overview.mmd` | `../img/system-overview.svg` | `docs/ARCHITECTURE.md` |
| `reconcile-loop.mmd` | `../img/reconcile-loop.svg` | `docs/ARCHITECTURE.md` |
| `serving-sequence.mmd` | `../img/serving-sequence.svg` | `docs/ARCHITECTURE.md` |

The `img/*.svg` files are hand-authored SVGs (self-framed panels that read on
light and dark pages). Edit those directly to tweak the artwork; keep the `.mmd`
sources here in sync as the canonical description of each diagram. To render a
`.mmd` source to an image instead, use the Mermaid CLI:

```bash
npx @mermaid-js/mermaid-cli -i architecture-overview.mmd -o ../img/architecture-overview.svg
```
