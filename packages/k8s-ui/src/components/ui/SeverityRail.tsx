import type { CSSProperties } from 'react'

// The rail is a 2px ring laid over the card's outline, solid down the left
// side and fading out around the top and bottom corners, so the colour takes
// the outline's place and is gone where each curve ends. A closed card's
// outline is its 1px border, so the ring covers it and 1px inside; an open
// card's is the border plus a 1px ring outside it, so the ring covers both.
// Each radius is the card's (rounded-xl, 12px) widened by how far the ring
// sits outside the padding edge's 11px.
function ringStyle(outset: number): CSSProperties {
  const radius = 11 + outset
  return {
    inset: -outset,
    padding: 2,
    borderRadius: radius,
    background: `linear-gradient(to right, var(--rail) 0, var(--rail) 2px, transparent ${radius}px)`,
    mask: 'linear-gradient(#000 0 0) content-box, linear-gradient(#000 0 0)',
    maskComposite: 'exclude',
    WebkitMask: 'linear-gradient(#000 0 0) content-box, linear-gradient(#000 0 0)',
    WebkitMaskComposite: 'xor',
  }
}

const CLOSED = ringStyle(1)
const OPEN = ringStyle(2)

/**
 * A card's left severity rail. The card must be `relative isolate rounded-xl
 * border`, with `ring-1` when open, and must not clip: the rail paints over
 * the border, so the card's contents go in an inner `overflow-hidden
 * rounded-[11px]` wrapper instead. `isolate` keeps the rail's `z-10` (above
 * the open body, which is positioned) inside the card. `className` sets the
 * rail's colour as `--rail`.
 */
export function SeverityRail({ open, className }: { open: boolean; className: string }) {
  return <span aria-hidden className={`pointer-events-none absolute z-10 ${className}`} style={open ? OPEN : CLOSED} />
}

/** A card header's left padding: the 1px of a closed card's rail inside the border, plus the header's own 12px. */
export const SEVERITY_RAIL_HEADER_INSET_CLASS = 'pl-[13px]'
