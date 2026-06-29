import { assetUrl } from '../../utils/asset-url'
import radarLoadingIconAsset from '../../assets/radar/radar-icon-loading.svg'

// assetUrl normalizes the bundler-specific asset-import type (string under Vite,
// StaticImageData under webpack/Next) to a URL string usable in `<img src>`.
const radarLoadingIcon = assetUrl(radarLoadingIconAsset)

// PaneLoader — center-of-pane loading state. The animated radar icon is
// pinned to the pane's exact center; the label hangs at a fixed offset
// BELOW it, absolutely positioned (not a flex sibling), so the label never
// affects where the icon sits. The icon therefore holds a single position
// while only the text under it appears/changes — and it lands at the same
// point as the host/connect splash surfaces (which center the icon at 50%
// with the label decoupled below), so a splash → PaneLoader hand-off no
// longer makes the logo jump. Pin to the parent's fill via `className`
// (`flex-1`, `h-full`, `h-32`, `absolute inset-0`, etc.). The SVG self-
// animates (sweep arm + blips, `prefers-reduced-motion` honored).
export function PaneLoader({
  label = 'Loading…',
  className = '',
}: {
  label?: string
  className?: string
}) {
  // No `relative` on the root: the label anchors to the inner `relative` span
  // below, and callers may pass a positioning class (e.g. `absolute inset-0`,
  // for topology panes) — a root `relative` would conflict with it.
  return (
    <div className={`flex items-center justify-center ${className}`} aria-live="polite">
      {/* The icon is the only in-flow child, so it centers in the pane. The
          label is absolutely positioned below the icon and so never shifts it. */}
      <span className="relative">
        <img src={radarLoadingIcon} alt="" aria-hidden className="w-11 h-11" />
        {/* Label style matches the splash surfaces (17px semibold tracking-tight,
            primary) so the whole loading family — boot splash, connect splash,
            PaneLoader — reads as one continuous state, not a font change at the
            hand-off. */}
        <span className="absolute left-1/2 top-full mt-3 -translate-x-1/2 whitespace-nowrap text-[17px] font-semibold tracking-tight text-theme-text-primary">
          {label}
        </span>
      </span>
    </div>
  )
}
