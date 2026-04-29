import { ReactNode, useState, useRef, useEffect, useCallback } from 'react'
import { createPortal } from 'react-dom'
import { clsx } from 'clsx'
import { computeTooltipPosition } from './tooltip-position'

// Module-level singleton coordinator: only one Tooltip can be visible
// at a time across the whole app. Without this, two Tooltip instances
// could both render their portal simultaneously — happens when a
// trigger element unmounts/remounts during an in-progress hover (React
// re-render, HMR, rapid cursor movement between adjacent triggers),
// because the old trigger's mouseleave never fires so its visible
// state stays stuck. Observed in multi-cluster visual tests on
// densely-populated source-chip hovers.
//
// Each visible Tooltip registers a `hide` callback. When the next
// Tooltip becomes visible, it calls the previous active tooltip's
// hide(), guaranteeing a single visible portal. Registry clears on
// hide or unmount.
let activeHide: (() => void) | null = null

interface TooltipProps {
  content: ReactNode
  children: ReactNode
  /** Delay before showing tooltip in ms (default: 300) */
  delay?: number
  /** Position of tooltip (default: 'top') */
  position?: 'top' | 'bottom' | 'left' | 'right'
  /** Additional class for the tooltip content */
  className?: string
  /** Whether tooltip is disabled */
  disabled?: boolean
  /** Additional class for the wrapper span (useful for positioning) */
  wrapperClassName?: string
  /** Inline styles for the wrapper span (useful for absolute positioning) */
  wrapperStyle?: React.CSSProperties
}

export function Tooltip({
  content,
  children,
  delay = 300,
  position = 'top',
  className,
  disabled = false,
  wrapperClassName,
  wrapperStyle,
}: TooltipProps) {
  const [isVisible, setIsVisible] = useState(false)
  // null while the tooltip is mounting and hasn't been measured yet — the
  // portal renders with `visibility: hidden` in that window so the user
  // never sees a frame painted at the default (0,0) coordinates. Bug
  // observed on app.radarhq.io: nav button tooltips ("Topology",
  // "Timeline", ...) flashed at the viewport's top-left corner and
  // overlapped the "Radar / By Skyhook" logo before snapping into place.
  const [coords, setCoords] = useState<{ top: number; left: number } | null>(
    null
  )
  const triggerRef = useRef<HTMLSpanElement>(null)
  const tooltipRef = useRef<HTMLSpanElement>(null)
  const timeoutRef = useRef<number | null>(null)
  const rafRef = useRef<number | null>(null)

  const updatePosition = useCallback(() => {
    if (!triggerRef.current) return

    const triggerRect = triggerRef.current.getBoundingClientRect()
    const tooltipRect = tooltipRef.current?.getBoundingClientRect()

    const next = computeTooltipPosition({
      triggerRect: {
        top: triggerRect.top,
        left: triggerRect.left,
        width: triggerRect.width,
        height: triggerRect.height,
      },
      tooltipSize: {
        width: tooltipRect?.width ?? 0,
        height: tooltipRect?.height ?? 0,
      },
      position,
      viewport: {
        width: window.innerWidth,
        height: window.innerHeight,
      },
    })

    if (next) {
      setCoords(next)
    }
  }, [position])

  // Stable hide function for the singleton registry — useRef so the
  // identity stays the same across renders, otherwise the registry
  // could hold a stale closure that doesn't see the latest setState.
  const hideRef = useRef<() => void>(() => {})
  hideRef.current = () => {
    if (timeoutRef.current) {
      clearTimeout(timeoutRef.current)
      timeoutRef.current = null
    }
    setIsVisible(false)
    setCoords(null)
  }

  const showTooltip = () => {
    if (disabled || !content) return
    timeoutRef.current = window.setTimeout(() => {
      // Singleton: hide whoever was visible before us, register self
      // as the new active tooltip. Guards against stuck duplicates.
      if (activeHide && activeHide !== hideRef.current) {
        activeHide()
      }
      activeHide = hideRef.current
      setIsVisible(true)
    }, delay)
  }

  const hideTooltip = () => {
    if (timeoutRef.current) {
      clearTimeout(timeoutRef.current)
      timeoutRef.current = null
    }
    if (activeHide === hideRef.current) {
      activeHide = null
    }
    setIsVisible(false)
    setCoords(null)
  }

  useEffect(() => {
    if (isVisible) {
      // Two-pass measurement. The portal is rendered with
      // `visibility: hidden` while coords are null, so we get a real
      // tooltip rect on the first rAF. A second rAF re-runs the
      // computation with the now-known tooltip width/height so the
      // anchor centering is exact. Without this second pass the
      // tooltip would show with size-0 centering for one frame and
      // then jump.
      const id1 = requestAnimationFrame(() => {
        updatePosition()
        const id2 = requestAnimationFrame(updatePosition)
        rafRef.current = id2
      })
      rafRef.current = id1
      return () => {
        if (rafRef.current !== null) cancelAnimationFrame(rafRef.current)
      }
    }
  }, [isVisible, updatePosition])

  useEffect(() => {
    return () => {
      if (timeoutRef.current) {
        clearTimeout(timeoutRef.current)
      }
      if (rafRef.current !== null) {
        cancelAnimationFrame(rafRef.current)
      }
      // Clear from singleton registry on unmount — otherwise a Tooltip
      // that unmounts while visible (e.g. row removed during hover)
      // would leave a stale entry in activeHide pointing at a torn-down
      // setState, blocking the next Tooltip from registering.
      if (activeHide === hideRef.current) {
        activeHide = null
      }
    }
  }, [])

  // When disabled flips true, proactively cancel any pending show timer and
  // clear visible state. Without this, a tooltip that was visible (or armed)
  // when disabled became true would pop back on as soon as disabled flips
  // false — even though the cursor is elsewhere and no fresh mouseenter has
  // fired. Also covers the case where the trigger becomes unreachable via
  // pointer-events-none and never fires mouseleave.
  useEffect(() => {
    if (disabled) {
      if (timeoutRef.current) {
        clearTimeout(timeoutRef.current)
        timeoutRef.current = null
      }
      setIsVisible(false)
      setCoords(null)
    }
  }, [disabled])

  // Dismiss on browser back/forward (route changes) and on Escape.
  // The mouseleave-based dismissal misses the case where a tooltip is
  // anchored to a button that itself triggers navigation (nav buttons,
  // tab pills): React keeps the button mounted across the route change
  // so no unmount cleanup fires, and the cursor is still over the
  // trigger so no mouseleave fires either. The user sees a stuck
  // tooltip overlapping the new page's chrome. Hooking popstate +
  // Escape gives a deterministic dismissal path for those cases.
  useEffect(() => {
    if (!isVisible) return
    const onPop = () => hideRef.current()
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') hideRef.current()
    }
    window.addEventListener('popstate', onPop)
    window.addEventListener('keydown', onKey)
    return () => {
      window.removeEventListener('popstate', onPop)
      window.removeEventListener('keydown', onKey)
    }
  }, [isVisible])

  if (disabled || !content) {
    return <>{children}</>
  }

  return (
    <>
      <span
        ref={triggerRef}
        className={clsx('inline-flex max-w-full', wrapperClassName)}
        style={wrapperStyle}
        onMouseEnter={showTooltip}
        onMouseLeave={hideTooltip}
        onFocus={showTooltip}
        onBlur={hideTooltip}
        // pointerdown fires before click and before any onClick the
        // child uses for navigation/state changes — guaranteeing the
        // tooltip is gone before the trigger's action runs. Covers:
        //   - nav button click navigates → tooltip would otherwise
        //     stick (button stays mounted, cursor still over it)
        //   - tab pill click → same shape, tooltip lingers under the
        //     active tab
        //   - clicking a table row to open a drawer → row's name
        //     tooltip used to remain pinned above the table header
        onPointerDown={hideTooltip}
      >
        {children}
      </span>
      {isVisible &&
        createPortal(
          <span
            ref={tooltipRef}
            className={clsx(
              'fixed z-[9999] px-2 py-1 text-xs text-theme-text-primary bg-theme-base rounded shadow-lg border border-theme-border',
              'whitespace-nowrap pointer-events-none',
              className
            )}
            style={{
              top: coords?.top ?? 0,
              left: coords?.left ?? 0,
              // Hidden until the first measurement pass writes real
              // coords. Without this guard the portal paints at (0,0)
              // for one frame, which on slower clients shows up as
              // tooltip text overlapping the top-left logo.
              visibility: coords ? 'visible' : 'hidden',
            }}
            role="tooltip"
            aria-hidden={coords ? undefined : true}
          >
            {content}
          </span>,
          document.body
        )}
    </>
  )
}

/** Simple wrapper that adds tooltip to any element - use for quick migrations from title="" */
export function WithTooltip({
  tip,
  children,
  delay = 300,
}: {
  tip: string | undefined | null
  children: ReactNode
  delay?: number
}) {
  if (!tip) return <>{children}</>
  return (
    <Tooltip content={tip} delay={delay}>
      {children}
    </Tooltip>
  )
}
