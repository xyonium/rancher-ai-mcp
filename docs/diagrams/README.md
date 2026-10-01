# Diagrams

The two `.drawio` files are the editable sources for the diagrams referenced in
the top-level [README](../../README.md).

| Source | Rendered as | Shows |
|--------|-------------|-------|
| `tool-surface.drawio` | `tool-surface.svg` | the consolidated 7-tool surface and the plan → approve → execute confirmation flow |
| `consolidation-variants.drawio` | `consolidation-variants.svg` | 39 upstream tools → 7, and the four image variants by auto-write × exec |

## Regenerating the SVGs

Open a `.drawio` file in [draw.io](https://app.diagrams.net) (or the desktop
app), then **File → Export as → SVG…** and save over the matching `.svg` name in
this directory. Keep **"Include a copy of my diagram"** enabled so the SVG stays
editable later; GitHub renders the SVG inline either way.

> The README references the `.svg` files. If you export to PNG instead, update
> the two `<img src>` paths in the top-level README accordingly.
