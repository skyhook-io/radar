# design-sync notes — @skyhook-io/k8s-ui → claude.ai/design "Skyhook Radar"

## Re-sync (from repo root)

```sh
# stage converter (skill base dir) into .ds-sync/, then:
(cd .ds-sync && npm i esbuild ts-morph @types/react playwright@<repo playwright version>)
node .design-sync/build-pkg.mjs
# fetch the project's _ds_sync.json -> .design-sync/.cache/remote-sync.json
node .ds-sync/resync.mjs --config .design-sync/config.json --node-modules ./node_modules \
  --entry ./packages/k8s-ui/.ds-sync-pkg/dist/index.js --out ./ds-bundle \
  --remote .design-sync/.cache/remote-sync.json
```

Last synced 2026-09-28: 377 components, 66 authored previews (all graded good), bundle 11.65 MB.

## Build

- App-level theme (`@theme` font stacks, radii, shadows, breakpoints + `:root`/`body` base rules)
  lives in `web/src/index.css`; build-pkg extracts those blocks into the Tailwind input. Without
  it everything renders in the system font.

- k8s-ui ships source only. `node .design-sync/build-pkg.mjs` (cfg.buildCmd) builds a sync-only
  package at `packages/k8s-ui/.ds-sync-pkg/` (gitignored): minified ESM `dist/`, `.d.ts` via
  `typescript-7` tsc, Tailwind v4 compiled over the theme layers, one merged `styles.css`.
  Converter command: `--node-modules ./node_modules --entry ./packages/k8s-ui/.ds-sync-pkg/dist/index.js`.
  Config paths (`srcDir`, `cssEntry`, `extraFonts`, `componentSrcMap`) are relative to that dir.
- Entry = main barrel + `components/charts` + `components/resources/renderers` (the last two are
  only reachable through package.json subpath exports). A new subpath-only component group must
  be added to `entry.ts` in build-pkg.mjs or it silently drops out of the sync.
- React must come from the page (`window.React`): build-pkg reuses the converter's `reactShim`.
  Externalizing react instead leaves CJS deps doing `require("react")` → every card throws
  "Dynamic require of react is not supported".
- shiki is stubbed (dynamic grammar imports can't live in one IIFE) — CodeViewer renders plain `<pre>`.
- monaco deep imports are extensionless (Vite-only); build-pkg resolves them and inlines the
  codicon `.ttf` as a data URL.
- `*Cell` table-cell renderers (127) are excluded via `componentSrcMap: null` — they only make
  sense inside ResourcesView tables. They stay in the bundle.
- Renderers living in multi-component files (Kueue*, Knative*, Kyverno*, CNPG declarative…) are
  pinned in `componentSrcMap` so they group under `renderers` instead of `general`.

## Authoring previews

- Port canonical usages first; the old previews were recovered from the project's compiled
  `_preview/*.js` (the tsx follows the `// .design-sync/previews/<Name>.tsx` marker).
- `.btn-brand` carries no padding — give preview buttons explicit padding.
- Tailwind classes used only in a preview are compiled by the full build (build-pkg `@source`s
  `.design-sync/previews`), not by a targeted `preview-rebuild` — use inline style while iterating.
- `position: fixed` surfaces (EventDetailPanel) stay inside the card under a
  `transform: translateZ(0)` wrapper.
- Only one Tooltip can be open at a time (module singleton); SelectMenu's open state closes if
  another cell steals focus — keep one auto-open cell per card.
- `GitOpsFilterSection`/`GitOpsFacetButton` alias `FacetSection`/`FacetButton` (identical render).
- The capture clock is frozen, so `Date.now()`-relative fixtures are stable.
- Monaco editors (YamlEditor, YamlDiffEditor, YamlReview) DO render in the capture sandbox;
  YamlReview switches to side-by-side above 900px, hence its 1100x700 single-card viewport.
- Trace fixtures (`reachFixtures.ts`) aren't exported; TraceSummary/ReachabilityView previews
  inline their fixture builders.

## Re-sync risks

- **Bundle size**: `_ds_bundle.js` is ~11.6 MB against the 12 MB upload cap. The converter
  re-prints (un-minifies) the dist, so minification only partly survives. Monaco is ~half of it,
  elkjs ~3.5 MB. The next heavy dependency pushes it over; the fix then is stubbing Monaco
  (YamlEditor/YamlDiffEditor would degrade) or dropping elkjs-backed topology.
- New `*Cell` exports and new multi-component renderer files need config entries (exclusion /
  srcMap pin) or they show up as cards in `general`.
- Authored previews under `previews/` import the current props; a renamed prop shows as a
  compile failure (`! preview build failed`) and the card drops to the floor.
- ResourceBar `layout="inline"` + `tooltip` collapses the track to 0 width (Tooltip wrapper is
  `inline-flex`, inline bar row isn't `w-full`) — product bug; the preview omits the tooltip.
- Previous sync (2026-07) never committed its config or previews; the 42 previews here were
  ported back from the project's compiled `_preview/*.js`.

## Known render warns

- `[TOKENS_MISSING]`: `--vscode-*` (Monaco runtime-injected), `--xy-*` (xyflow defaults with
  fallbacks). `--color-brand-50`, `--color-brand-950`, `--color-radar-accent` are referenced by
  FilterPill (`brand` tone), ChecksView and AuditFindingsTable but defined nowhere in Radar OSS —
  a real product gap, not a sync issue.
- `[FONT_MISSING]` "DM Sans": fallback name in the app stack `"DM Sans Variable", "DM Sans", …`;
  the variable family ships and resolves first.
- `[FONT_MISSING]` "Segoe WPC", "Ubuntu Mono": platform fallbacks in Monaco's font stacks, not
  brand fonts. Brand fonts (DM Sans, DM Mono) ship via extraFonts.
- YamlEditor: `SecurityError: Failed to construct 'Worker'` (9 pageerrors) — the monaco-yaml
  schema worker (`new URL('./monacoYaml.worker.ts', import.meta.url)`) can't load from the preview
  origin. The editor renders and grades good, but validate counts it `bad` (authored card with
  pageerrors). Same in designs: editing works, YAML schema validation is inactive.
- `import.meta.env` is defined as production in build-pkg; otherwise the converter's DEV=true
  leaks dev-only UI (ReachabilityView's "DEV STATE" switcher) into cards and designs.
