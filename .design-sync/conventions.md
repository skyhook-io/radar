# Building with Skyhook Radar (`@skyhook-io/k8s-ui`)

This is Radar's Kubernetes UI library — the components behind Skyhook's cluster-visibility product. Every component is on `window.K8sUI.*` (import as `import { Badge, StatusDot } from '@skyhook-io/k8s-ui'`). Build Kubernetes/infra dashboards, resource views, and operational surfaces from these real parts; they map 1:1 onto shippable Radar code.

## Setup & wrapping
- **No app-wide provider.** Most components (Badge, StatusDot, HealthRing, DistributionBar, Property, Section, CardSection, PageHeader, EmptyState, Facet, charts, every `*Renderer` resource view…) render standalone.
- **Theme is CSS, not a React provider.** Tokens live on `:root`; default is **light**. For dark mode add `class="dark"` to a root element (`document.documentElement.classList.toggle('dark', isDark)`).
- Only these need their own provider, and only when used: `ToastProvider` (toasts), `DockProvider` (logs/terminal dock).
- Icons: `lucide-react` (Radar's icon set) — e.g. `RefreshCw`, `Trash2`, `CirclePause`. `RowActionMenu` items take an `icon` component.

## Styling idiom — Tailwind v4 utilities over theme tokens
Style your own layout with these theme classes (never hard-code hex — they adapt to light/dark):

| Purpose | Classes |
|---|---|
| Backgrounds | `bg-theme-base` (page) · `bg-theme-surface` (cards/panels) · `bg-theme-elevated` (inputs/dropdowns) · `bg-theme-hover` |
| Text | `text-theme-text-primary` · `text-theme-text-secondary` · `text-theme-text-tertiary` |
| Borders | `border-theme-border` · `border-theme-border-light` |
| Accent | `text-accent` · `bg-accent` · `border-accent` |
| Fonts | DM Sans is the root default (no class needed) · `font-mono` (DM Mono — IDs, images, metrics, YAML) |
| Shape | `rounded-md` / `rounded-lg` · `shadow-theme-sm` / `shadow-theme-md` / `shadow-theme-lg` |

Component-layer classes: `.badge` / `.badge-sm`, `.btn-brand` / `.btn-brand-muted` (color + radius only — **add your own padding**, e.g. `px-3 py-1.5 text-sm`), `.card-inner` / `.card-inner-lg`, `.dialog`.

**Status vocabulary is first-class — never invent status colors.** Health tones: `healthy` (green) → `degraded` (amber) → `alert` (orange) → `unhealthy` (red), plus `neutral` and `unknown`. Use `<StatusDot tone="degraded" />`, the `.status-healthy|degraded|alert|unhealthy|neutral|unknown` classes, or `<Badge severity="success|info|warning|alert|error|neutral">`. For Kubernetes kinds use `<Badge kind="Deployment">` (per-kind colors).

## Where the truth lives
`styles.css` imports the compiled stylesheet (`_ds_bundle.css`) that defines every token (`--bg-*`, `--text-*`, `--border-*`, `--accent`) and the classes above. Per-component API is in each `<Name>.d.ts`; usage with realistic examples in `<Name>.prompt.md`.

## Idiomatic example
```tsx
import { PageHeader, Badge, StatusDot, Property } from '@skyhook-io/k8s-ui'

export function WorkloadPanel() {
  return (
    <div className="bg-theme-surface border border-theme-border rounded-lg p-4">
      <PageHeader title="checkout-api" description="Deployment · namespace payments" />
      <div className="flex items-center gap-2 mt-2">
        <Badge kind="Deployment">Deployment</Badge>
        <Badge severity="alert">Degraded</Badge>
      </div>
      <div className="flex items-center gap-2 mt-3 text-sm text-theme-text-secondary">
        <StatusDot tone="degraded" /> 2 / 3 pods ready
      </div>
      <Property label="Image" value="ghcr.io/acme/checkout:1.14.2" />
    </div>
  )
}
```
Compose Radar's real parts for the UI; use the theme classes above only for your own layout glue.

Only utilities Radar itself uses are compiled into the stylesheet; prefer the classes above and common layout utilities (`flex`, `gap-*`, `p-*`, `text-sm`/`text-xs`), and fall back to inline `style` for anything unusual.
