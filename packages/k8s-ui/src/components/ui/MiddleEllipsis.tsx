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
// Place inside a width-constrained container (e.g. a button child with
// `max-w-[…]`). The wrapper itself takes `width: 100%` of that container.

const ELLIPSIS = '…'

export interface MiddleEllipsisProps {
  text: string
  className?: string
  /** Native browser tooltip. Opt-in: defaulting to the full text would
   *  duplicate any tooltip wrapper (e.g. `<Tooltip>`) higher up the tree. */
  title?: string
  /** Fires whenever the rendered text changes between full and truncated.
   *  Lets a parent gate behavior on actual truncation — e.g. show a custom
   *  tooltip only when the visible text isn't already the full string. */
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
      const width = node.clientWidth
      if (width <= 0) return
      const cs = window.getComputedStyle(node)
      ctx.font = `${cs.fontWeight} ${cs.fontSize} ${cs.fontFamily}`
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
        display: 'inline-block',
        position: 'relative',
        overflow: 'hidden',
        whiteSpace: 'nowrap',
        minWidth: 0,
        verticalAlign: 'bottom',
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
