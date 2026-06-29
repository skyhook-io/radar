# Design System

Radar's design system for AI coding agents. Read this before writing or modifying frontend code.

Source files: `packages/k8s-ui/src/theme/variables.css` (tokens), `tailwind-theme.css` (Tailwind mapping), `components.css` (shared classes), `Badge.tsx` (badge component + color definitions).

## 1. Visual Theme & Atmosphere

Cool, professional, data-dense. A barely-blue-tinted neutral palette — not generic gray, not flashy. Light mode defaults to cool off-white surfaces; dark mode to deep navy-black. The brand accent is a refined mid-blue (`#4A7CC9` light / `#6A9FE0` dark) — saturated enough to stand out, restrained enough for a tool you stare at all day. Information density is high (K8s dashboards), so the palette relies on subtle background layering and muted borders rather than heavy visual chrome.

**Key characteristics:**
- Light/dark via `light-dark()` CSS function + `.dark` class toggle
- Cool blue-gray tint in backgrounds and borders (not pure gray)
- Single brand accent color — no secondary brand colors
- DM Sans Variable for UI, DM Mono for code/terminal
- Rounder than Tailwind defaults (6px base, 14px cards/modals)
- Status communicated via emerald/amber/red/sky semantic colors

## 2. Color Palette & Roles

### Backgrounds
| Token | CSS Variable | Light | Dark | Use for |
|-------|-------------|-------|------|---------|
| `bg-theme-base` | `--bg-base` | `#F0F3F9` | `#0B0F1E` | Page background, header, toolbar chrome |
| `bg-theme-surface` | `--bg-surface` | `#F8FBFE` | `#14192A` | Sidebars, cards, panels, drawers |
| `bg-theme-elevated` | `--bg-elevated` | `#E6ECF5` | `#1E2338` | Inputs, count badges, dropdowns |
| `bg-theme-hover` | `--bg-hover` | `#DDE5F2` | `#282E48` | Hover states on interactive elements |
| `bg-theme-active` | `--bg-active` | `#D3DCEC` | `#1C2840` | Active/pressed states |

### Text
| Token | CSS Variable | Light | Dark | Use for |
|-------|-------------|-------|------|---------|
| `text-theme-text-primary` | `--text-primary` | `#1A1C20` | `#F5F6F8` | Headings, primary content |
| `text-theme-text-secondary` | `--text-secondary` | `#555860` | `#D8DAE0` | Body text, descriptions |
| `text-theme-text-tertiary` | `--text-tertiary` | `#6B7080` | `#B0B5C0` | Placeholders, metadata, timestamps |
| `text-theme-text-disabled` | `--text-disabled` | `#A0A4AC` | `#6A7080` | Disabled states |

### Borders
| Token | CSS Variable | Light | Dark | Use for |
|-------|-------------|-------|------|---------|
| `border-theme-border` | `--border-default` | `#D8E0EE` | `#222840` | Standard borders (cards, inputs, dividers) |
| `border-theme-border-light` | `--border-light` | `#CDD6E8` | `#2C324A` | Slightly heavier borders |
| `border-theme-border-subtle` | `--border-subtle` | `rgba(74,100,180,0.07)` | `rgba(255,255,255,0.04)` | Very subtle separators |

### Brand & Accent
| Token | CSS Variable | Light | Dark | Use for |
|-------|-------------|-------|------|---------|
| `bg-accent` / `text-accent` | `--accent` | `#4A7CC9` | `#6A9FE0` | Primary actions, active states, links |
| `bg-accent-light` | `--accent-light` | `#3B66A8` | `#8BB5EA` | Hover on accent elements |
| `bg-accent-muted` | `--accent-muted` | `rgba(74,124,201,0.12)` | `rgba(106,159,224,0.18)` | Subtle accent backgrounds |
| `text-accent-text` | `--accent-text` | `#3B66A8` | `#8BB5EA` | Accent-colored text |

Full Skyhook brand scale: `--color-skyhook-50` (`#EBF2FA`) through `--color-skyhook-900` (`#1F365C`), centered on `--color-skyhook` (`#5088CC`). Available as `bg-skyhook-*` / `text-skyhook-*` utilities.

### Semantic Status
| Color | Variable | Hex | Use for |
|-------|----------|-----|---------|
| Success | `--color-success` | `#22C55E` | Healthy, running, active, ready |
| Warning | `--color-warning` | `#F59E0B` | Degraded, pending, suspended |
| Error | `--color-error` | `#EF4444` | Unhealthy, failed, crashloop |
| Info | `--color-info` | `#3B82F6` | Completed, informational |

### Selection/Highlight
| Token | CSS Variable | Use for |
|-------|-------------|---------|
| `.selection` | `--selection-bg` | Selected rows, items |
| `.selection-strong` | `--selection-bg-strong` | Strongly selected items |
| `.selection-text` | `--selection-text` | Text in selected items |
| `.selection-ring` | `--selection-border` | Box-shadow ring on selected items |

### Shadows
| Token | CSS Variable | Use for |
|-------|-------------|---------|
| `shadow-theme-sm` | `--shadow-sm` | Subtle elevation (buttons, small elements) |
| `shadow-theme-md` | `--shadow-md` | Medium elevation (cards, dropdowns) |
| `shadow-theme-lg` | `--shadow-lg` | High elevation (modals, popovers) |
| `shadow-drawer` | `--shadow-drawer` | Slide-out drawers |
| `shadow-glow-brand-sm` | `--glow-brand-sm` | Subtle brand glow |
| `shadow-glow-brand-md` | `--glow-brand-md` | Medium brand glow |

Dark mode overrides shadows with heavier values + subtle white inset borders.

## 3. Typography Rules

### Font Families
| Role | Family | Tailwind class |
|------|--------|---------------|
| UI text | DM Sans Variable | `font-sans` (default) |
| Code, terminal, monospace | DM Mono | `font-mono` |

### Scale
Standard Tailwind type scale. No custom sizes or tracking. Use Tailwind utilities (`text-sm`, `text-base`, `text-lg`, `font-medium`, etc.) as normal.

## 4. Component Stylings

### Buttons
| Class | Use for |
|-------|---------|
| `.btn-brand` | Primary CTAs — brand-colored bg, white text, 10px radius |
| `.btn-brand-muted` | Secondary brand actions — dimmed brand bg, white text |
| `.btn-brand-toggle` | Toggle buttons — 50% brand bg, primary text |

Hover/disabled states are built into the classes. For non-brand buttons, use shadcn/ui `<Button>` variants.

### Badges
Use the `<Badge>` component from `@skyhook-io/k8s-ui` — **never hand-write badge
color strings** (`bg-<color>-NN/NN text-<color>-NN …`). Those bypass the theme
(they're dark-tuned and wash out in light mode) and, worse, *overload hues across
intents* — a green `bg-green-500/20` "HTTPS" chip becomes indistinguishable from a
green "success" chip, so retuning "success" silently recolors every HTTPS tag.

**Pick the prop by what the badge MEANS, not by the color you want** (tokens are
named by intent so a future retune moves exactly the right ones):

| The badge represents… | Use | Example |
|---|---|---|
| a status / outcome | `severity=` (`success`/`warning`/`alert`/`error`/`info`/`neutral`) | `<Badge severity="error">Failed</Badge>` |
| resource health | `severity=` via `mapHealthToTone` / `getHealthBadgeColor` | health tone |
| a K8s/CRD **resource kind** | `kind=` | `<Badge kind="Middleware">Middleware</Badge>` |
| a transport **protocol/scheme** | `protocol=` (http/https/tls/tcp/udp/grpc/h2) | `<Badge protocol="HTTPS">HTTPS</Badge>` |
| a neutral **attention/FYI** marker (cross-namespace, wildcard, default, immutable) | `tone="note"` | `<Badge tone="note">cross-namespace</Badge>` |
| **local** categorical distinction (rw/ro, spot/on-demand, control-plane/worker) — hue has no global meaning, just "tell siblings apart" | `tone="accent1"`/`"accent2"`/`"accent3"` | `<Badge tone="accent1">rw</Badge>` |
| a **structural** data fragment (port, path, host, weight, name, count) | `tone="structural"` | `<Badge tone="structural" size="sm">:8080</Badge>` |
| genuinely one-off (last resort) | `colorClass=` | `<Badge colorClass="…">Custom</Badge>` |

```tsx
<Badge severity="success">Running</Badge>
<Badge kind="Pod">my-pod</Badge>
<Badge protocol="TCP">TCP</Badge>      {/* protocol ≠ severity: TCP is indigo, not orange/success */}
<Badge tone="note">wildcard</Badge>
<Badge tone="structural" size="sm">:443</Badge>
```

If you reach for `colorClass`, first ask whether you're inventing an intent that
should be a named token here instead — if two places want "the same accent",
that's a token, not two literals. A ratchet test
(`badge-no-handrolled.test.tsx`) fails CI if a renderer **adds** a hand-rolled
chip; the existing baseline shrinks over time, never grows.

For programmatic color lookup (e.g., dynamic table cells):
- `getKindColorClass(kind)` — returns Tailwind classes for a K8s resource kind
- `getSeverityBadge(severity)` — returns classes for a severity level
- `getResourceStatusColor(status)` — maps K8s status strings ("Running", "Pending", "Failed") to badge classes
- `getHealthBadgeColor(health)` — maps health states to badge classes
- `getHelmStatusColor(status)` — maps Helm release statuses to badge classes

All from `@skyhook-io/k8s-ui/utils/badge-colors`.

### Status Badges in Tables
Use CSS classes from `components.css` for status cells in table rows:
- `.status-healthy` — emerald
- `.status-degraded` — amber
- `.status-unhealthy` — red
- `.status-neutral` — sky
- `.status-unknown` — gray (uses theme variables)
- `.status-violet`, `.status-purple`, `.status-orange`, `.status-cyan` — special cases

### Cards & Containers
| Class | Use for |
|-------|---------|
| `.card-inner` | Nested containers in drawers/renderers — base bg, subtle border, 6px radius, 8px padding |
| `.card-inner-lg` | Larger nested containers — 10px radius, 12px padding |
| `.dialog` | Modal/dialog containers — surface bg, default border, 14px radius, large shadow |

### Subtle Borders
| Class | Use for |
|-------|---------|
| `.table-divide-subtle` | Ultra-subtle dividers between table rows (apply to parent) |
| `.border-b-subtle` | Bottom border on individual elements |
| `.border-r-subtle` | Right border |
| `.border-t-subtle` | Top border |

## 5. Layout Principles

### Spacing
Standard Tailwind spacing scale. No custom base unit.

### Border Radius (rounder than Tailwind defaults)
| Tailwind | Value | Use for |
|----------|-------|---------|
| `rounded` | 6px | Badges, inline elements |
| `rounded-md` | 8px | Inputs, small containers |
| `rounded-lg` | 10px | Buttons, dropdowns |
| `rounded-xl` | 14px | Cards, modals, dialogs |

### Breakpoints
| Name | Width |
|------|-------|
| `sm` | 900px |
| `md` | 1100px |
| `lg` | 1280px |
| `xl` | 1536px |

Wider than Tailwind defaults — Radar is a desktop-first tool.

## 6. Depth & Elevation

| Level | Treatment | Use for |
|-------|-----------|---------|
| Base | `bg-theme-base`, no shadow | Page background |
| Surface | `bg-theme-surface`, optional `shadow-theme-sm` | Panels, sidebars, cards |
| Elevated | `bg-theme-elevated`, `shadow-theme-md` | Dropdowns, popovers, inputs |
| Floating | `bg-theme-surface`, `shadow-theme-lg` | Modals, command palette |
| Drawer | `bg-theme-surface`, `shadow-drawer` | Slide-out resource drawers |

Dark mode adds heavier shadows with subtle white inset borders for edge definition.

## 7. Do's and Don'ts

### Do
- Use `bg-theme-surface`, `text-theme-text-secondary`, `border-theme-border` etc. for all surfaces, text, and borders
- Use `<Badge severity="...">` or `<Badge kind="...">` for all badge rendering
- Use `.btn-brand` for primary action buttons
- Use `.card-inner` / `.card-inner-lg` for nested containers
- Use `.status-healthy` / `.status-degraded` / etc. for status cells in tables
- Use `shadow-theme-sm` / `shadow-theme-md` / `shadow-theme-lg` for elevation
- Use `font-sans` (DM Sans) and `font-mono` (DM Mono) — they're the defaults
- Use `light-dark()` in CSS when adding new custom properties that need theme awareness. Note: `light-dark()` doesn't work for comma-separated values (e.g., multi-layer shadows) — use a `.dark {}` override block instead, as done in `variables.css`

### Don't
- Don't use `bg-white`, `bg-gray-*`, `bg-slate-*` for backgrounds — use `bg-theme-base/surface/elevated/hover`
- Don't use `text-gray-*`, `text-slate-*` for text — use `text-theme-text-primary/secondary/tertiary`
- Don't use `border-gray-*`, `border-slate-*` for borders — use `border-theme-border/border-light/border-subtle`
- Don't use `bg-blue-*` / `text-blue-*` for brand/accent — use `bg-accent`, `text-accent`, `.btn-brand`
- Don't use `shadow-sm` / `shadow-md` / `shadow-lg` (raw Tailwind) for cards, panels, or dialogs — use `shadow-theme-sm/md/lg`. Raw Tailwind shadows are fine on tooltips and transient floating elements.
- Don't hand-write badge color strings like `bg-emerald-100 text-emerald-700 ...` — use `<Badge>` or `badge-colors.ts` helpers
- Don't use `dark:` variants for colors that have theme tokens — the theme handles light/dark automatically
- Don't use template literals to construct Tailwind class names — Tailwind can't scan dynamic strings

**Exceptions:** Raw Tailwind color scales (`red-*`, `emerald-*`, etc.) are acceptable in:
- `Badge.tsx` and `badge-colors.ts` — static color definitions that Tailwind scans
- One-off visual effects (e.g., topology node glow, chart highlights) where no semantic token applies — use `dark:` variants for these

## 8. Responsive Behavior

Desktop-first tool. Most views assume 1100px+ viewport. Key patterns:
- Topology graph fills available space
- Resource tables truncate columns at narrower widths
- Drawers overlay from the right at fixed width
- Bottom dock (terminal/logs) collapses vertically

## 9. Agent Quick Reference

### Most Common Tokens
```
Page background:     bg-theme-base
Panel/card bg:       bg-theme-surface
Input/dropdown bg:   bg-theme-elevated
Hover state:         bg-theme-hover

Primary text:        text-theme-text-primary
Secondary text:      text-theme-text-secondary
Muted text:          text-theme-text-tertiary

Standard border:     border-theme-border
Subtle border:       border-theme-border-subtle

Brand button:        .btn-brand
Brand accent:        bg-accent / text-accent

Card container:      .card-inner / .card-inner-lg
Dialog/modal:        .dialog

Elevation:           shadow-theme-sm / shadow-theme-md / shadow-theme-lg

Status badge:        <Badge severity="success|warning|error|info|neutral">
Kind badge:          <Badge kind="Deployment|Pod|Service|...">
Table status cell:   .status-healthy / .status-degraded / .status-unhealthy
```

### Example Component Patterns
```tsx
// Card with header
<div className="bg-theme-surface border border-theme-border rounded-xl shadow-theme-sm">
  <div className="border-b-subtle px-4 py-3">
    <h3 className="text-theme-text-primary font-medium">Title</h3>
  </div>
  <div className="p-4 text-theme-text-secondary text-sm">Content</div>
</div>

// Primary action button
<button className="btn-brand px-4 py-2 text-sm font-medium">Deploy</button>

// Status in a table cell
<span className="badge status-healthy">Running</span>

// Nested detail section
<div className="card-inner">
  <span className="text-theme-text-tertiary text-xs">Label</span>
  <span className="text-theme-text-primary text-sm">Value</span>
</div>
```
