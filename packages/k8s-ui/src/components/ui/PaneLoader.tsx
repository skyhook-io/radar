import radarLoadingIcon from '../../assets/radar/radar-icon-loading.svg'

// PaneLoader — center-of-pane loading state. Animated radar icon stacked
// above a label so swapping the label across the loading chain doesn't
// shift the icon horizontally. Pin to the parent's fill via `className`
// (`flex-1`, `h-full`, `h-32`, `absolute inset-0`, etc.). The SVG self-
// animates (sweep arm + blips, `prefers-reduced-motion` honored).
export function PaneLoader({
  label = 'Loading…',
  className = '',
}: {
  label?: string
  className?: string
}) {
  return (
    <div className={`flex flex-col items-center justify-center gap-3 ${className}`}>
      <img src={radarLoadingIcon} alt="" aria-hidden className="w-11 h-11" />
      <span className="text-sm text-theme-text-tertiary">{label}</span>
    </div>
  )
}
