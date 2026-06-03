import { useEffect, useRef, useState } from 'react'

// Renders text on a single line, fitted to its parent's width. When the full
// string overflows, it's truncated from the middle (`gke_koala…us-east1`)
// rather than the end, so cluster context strings like
// `gke_koalabackend_us-east1-b_prod-cluster-us-east1` keep both the
// identifying prefix and the region/role suffix.
//
// The trick: a "ghost" copy of the full text sits in normal flow but is
// visually hidden, while the visible truncated text overlays it absolutely.
// The ghost lets flex layout know our PREFERRED width is the full text — so
// when an ancestor's max-width grows (e.g. on a viewport breakpoint change),
// the wrapper re-grows back instead of staying trapped at the truncated
// render's width. Without it the layout settles into a fixed point: the
// flex parent shrink-wraps to the truncated content, MiddleEllipsis sees
// the shrunken width, keeps truncating; widening doesn't recover.
//
// Concretely, removing the ghost makes the trigger render full-width on
// first paint, then collapse to the truncated width on the first
// ResizeObserver tick, and stay collapsed forever even when the viewport
// widens. The ghost is load-bearing — don't simplify it away.
//
// Place inside a width-constrained container (e.g. a button child with
// `max-w-[…]`). The wrapper itself takes `width: 100%` of that container.

const ELLIPSIS = '…'

export interface MiddleEllipsisProps {
  text: string
  className?: string
  /** Native browser tooltip. Opt-in: defaulting to the full text would
   *  duplicate any tooltip wrapper (e.g. `<Tooltip>`) higher up the tree. */
  title?: string
  /** Fires whenever the rendered text changes between full and truncated
   *  (edge-triggered, not level — won't fire on every render). Lets a parent
   *  gate behavior on actual truncation, e.g. show a custom tooltip only
   *  when the visible text isn't already the full string.
   *
   *  Do NOT use the value to alter the width of the measured container — a
   *  truncated→untruncated swap that resizes the parent would oscillate
   *  through the ResizeObserver. Tooltips, badges, and other overlays/sibling
   *  affordances are fine; layout-affecting changes are not. */
  onTruncatedChange?: (truncated: boolean) => void
}

export function MiddleEllipsis({ text, className, title, onTruncatedChange }: MiddleEllipsisProps) {
  const wrapperRef = useRef<HTMLSpanElement>(null)
  const [display, setDisplay] = useState(text)
  const lastReportedTruncated = useRef<boolean | null>(null)

  useEffect(() => {
    const node = wrapperRef.current
    if (!node || typeof window === 'undefined') return
    const ctx = document.createElement('canvas').getContext('2d')
    if (!ctx) {
      setDisplay(text)
      return
    }

    const recompute = () => {
      // Use subpixel width (`getBoundingClientRect`) rather than the
      // pixel-rounded `clientWidth`. measureText returns subpixel widths,
      // and when the full text is JUST under the available space — say
      // ctx.measureText says 173.4px and clientWidth rounds to 173 — the
      // integer comparison decides we don't fit and middle-truncates a
      // name that visually would have rendered fine.
      const rawWidth = node.getBoundingClientRect().width
      if (rawWidth <= 0) return
      // getBoundingClientRect() returns the *rendered* width, which a CSS
      // transform on this node or any ancestor scales — e.g. the `scale()` in a
      // menu/popup/dialog entry animation. Measuring during that animation
      // compares a visually-shrunk container against unscaled canvas text
      // (measureText is transform-independent) and middle-truncates a name that
      // actually fits. Worse, a transform never changes the layout box, so the
      // ResizeObserver below doesn't fire to re-measure once the animation
      // settles — the wrong truncation then sticks. Divide out the cumulative
      // ancestor scaleX to recover the unscaled (still subpixel) layout width.
      // With no transform the scale is 1 and this is a no-op, so the just-fits
      // subpixel precision noted above is preserved.
      const scaleX = cumulativeScaleX(node)
      const width = scaleX > 0 ? rawWidth / scaleX : rawWidth
      const cs = window.getComputedStyle(node)
      // Include fontStyle in the shorthand so italic faces measure correctly.
      // If the assignment ever silently fails (malformed family quoting,
      // exotic computed values), the browser leaves ctx.font at its previous
      // value — on first call that's the default `10px sans-serif`, which
      // under-measures and would cause over-truncation. Detect by reading
      // back: if ctx.font normalised to the default but we didn't ask for
      // it, bail to full-text render (clipped by overflow:hidden) rather
      // than render a wrongly-truncated string.
      ctx.font = `${cs.fontStyle} ${cs.fontWeight} ${cs.fontSize} ${cs.fontFamily}`
      if (ctx.font === '10px sans-serif' && cs.fontSize !== '10px') {
        setDisplay(text)
        return
      }
      const next = fitMiddleTruncate(text, width, ctx)
      setDisplay(next)
      const truncated = next !== text
      if (truncated !== lastReportedTruncated.current) {
        lastReportedTruncated.current = truncated
        onTruncatedChange?.(truncated)
      }
    }

    recompute()
    const observer = new ResizeObserver(recompute)
    observer.observe(node)
    return () => observer.disconnect()
  }, [text, onTruncatedChange])

  return (
    <span
      ref={wrapperRef}
      className={className}
      title={title}
      style={{
        display: 'block',
        position: 'relative',
        overflow: 'hidden',
        whiteSpace: 'nowrap',
        minWidth: 0,
        // width:100% so the wrapper claims the parent's full available
        // width before measurement. Without it, a parent with extra room
        // would let the wrapper shrink to the ghost's natural width — and
        // the absolute-positioned visible overlay (inset:0) would clip to
        // that shrunken box, making text middle-truncate even when the
        // surrounding container had room to render it in full.
        width: '100%',
      }}
    >
      {/* Ghost: claims the full text's natural width in flow so flex parents
          shrink-wrap to the *full* preferred size, not the truncated render. */}
      <span aria-hidden="true" style={{ visibility: 'hidden' }}>
        {text}
      </span>
      {/* Visible: overlays the ghost with the current truncated rendering. */}
      <span style={{ position: 'absolute', inset: 0, overflow: 'hidden', whiteSpace: 'nowrap' }}>
        {display}
      </span>
    </span>
  )
}

// Cumulative horizontal scale applied to `el` by CSS transforms on it and its
// ancestors. Used to convert a transform-scaled getBoundingClientRect() width
// back to the unscaled layout width, so a `scale()` entry animation on an
// ancestor (menu/popup/dialog) can't make text middle-truncate when it would
// otherwise fit. Reads the computed transform matrix's scaleX (`a`) at each
// level — exact for the scale()/translate() animations this guards against
// (no rotation, where `a` would fold in cos θ). Returns 1 when nothing in the
// chain is transformed, making the caller a no-op in the common case.
function cumulativeScaleX(el: HTMLElement | null): number {
  let scale = 1
  for (let cur: HTMLElement | null = el; cur; cur = cur.parentElement) {
    const t = window.getComputedStyle(cur).transform
    if (t && t !== 'none') {
      try {
        scale *= new DOMMatrixReadOnly(t).a
      } catch {
        // Unparseable transform value — skip this level rather than throw.
      }
    }
  }
  return scale
}

// Binary-search the largest `n` such that `prefix(n) + … + suffix(n)` fits
// in `width`. Symmetric on purpose — keeping prefix and suffix balanced is
// the cheapest way to preserve the most identifying chars on both ends of
// names like `arn:aws:eks:…:cluster/prod` or `gke_…_prod-cluster-us-east1`.
function fitMiddleTruncate(
  text: string,
  width: number,
  ctx: CanvasRenderingContext2D,
): string {
  if (ctx.measureText(text).width <= width) return text
  if (text.length <= 2) return text

  let lo = 1
  let hi = Math.floor((text.length - 1) / 2)
  let best = 0
  while (lo <= hi) {
    const mid = (lo + hi) >> 1
    const candidate = text.slice(0, mid) + ELLIPSIS + text.slice(-mid)
    if (ctx.measureText(candidate).width <= width) {
      best = mid
      lo = mid + 1
    } else {
      hi = mid - 1
    }
  }
  return best === 0 ? ELLIPSIS : text.slice(0, best) + ELLIPSIS + text.slice(-best)
}
