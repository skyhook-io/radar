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
  /** Keep the wrapper mounted while disabled when child layout measurement requires stable DOM ancestry. */
  preserveWrapperWhenDisabled?: boolean
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
  preserveWrapperWhenDisabled = false,
  wrapperClassName,
  wrapperStyle,
}: TooltipProps) {
  const [isVisible, setIsVisible] = useState(false)
  // null until measured. Portal renders with visibility:hidden until coords
  // resolve so the user never sees a frame painted at default (0,0).
  const [coords, setCoords] = useState<{ top: number; left: number } | null>(
    null
  )
  const triggerRef = useRef<HTMLSpanElement>(null)
  const tooltipRef = useRef<HTMLSpanElement>(null)
  const timeoutRef = useRef<number | null>(null)
  const hideTimeoutRef = useRef<number | null>(null)
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
    if (hideTimeoutRef.current) {
      clearTimeout(hideTimeoutRef.current)
      hideTimeoutRef.current = null
    }
    if (activeHide === hideRef.current) {
      activeHide = null
    }
    setIsVisible(false)
    setCoords(null)
  }

  const cancelHide = () => {
    if (hideTimeoutRef.current) {
      clearTimeout(hideTimeoutRef.current)
      hideTimeoutRef.current = null
    }
  }

  const showTooltip = () => {
    if (disabled || !content) return
    cancelHide()
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
    if (hideTimeoutRef.current) {
      clearTimeout(hideTimeoutRef.current)
      hideTimeoutRef.current = null
    }
    if (activeHide === hideRef.current) {
      activeHide = null
    }
    setIsVisible(false)
    setCoords(null)
  }

  const scheduleHideTooltip = () => {
    if (timeoutRef.current) {
      clearTimeout(timeoutRef.current)
      timeoutRef.current = null
    }
    if (hideTimeoutRef.current) return
    hideTimeoutRef.current = window.setTimeout(hideTooltip, 80)
  }

  useEffect(() => {
    if (isVisible) {
      // Second rAF re-centers using the measured tooltip size; without
      // it the first frame centers against size 0 and visibly jumps.
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
      if (hideTimeoutRef.current) {
        clearTimeout(hideTimeoutRef.current)
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
      hideRef.current()
    }
  }, [disabled])

  // Mouseleave doesn't fire when the trigger stays mounted across a
  // navigation and the cursor is still over it; popstate + Escape give
  // a deterministic dismissal path for those cases.
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

  if ((disabled && !preserveWrapperWhenDisabled) || !content) {
    return <>{children}</>
  }

  return (
    <>
      <span
        ref={triggerRef}
        className={clsx('inline-flex max-w-full', wrapperClassName)}
        style={wrapperStyle}
        onMouseEnter={showTooltip}
        onMouseLeave={scheduleHideTooltip}
        onFocus={showTooltip}
        onBlur={hideTooltip}
        // pointerdown fires before click, so the tooltip is gone before
        // the trigger's action runs. Required when the child handles
        // navigation/state changes that don't unmount the trigger.
        onPointerDown={hideTooltip}
      >
        {children}
      </span>
      {isVisible && !disabled &&
        createPortal(
          <span
            ref={tooltipRef}
            className={clsx(
              'fixed z-[9999] px-2 py-1 text-xs text-theme-text-primary bg-theme-base rounded shadow-lg border border-theme-border',
              // Cap width + allow wrapping. Long tooltips (multi-sentence
              // disabled-reason explanations) used to render with
              // whitespace-nowrap, producing 700+ px wide single-line
              // tooltips that the viewport collision logic then pushed
              // away from their trigger to fit on screen — visually
              // detached from the element they were describing. With
              // max-w-xs (320px) + whitespace-normal, short tooltips
              // still fit on one line (content shorter than max-width)
              // and long ones wrap naturally near the trigger.
              'max-w-xs whitespace-normal break-words',
              className
            )}
            style={{
              top: coords?.top ?? 0,
              left: coords?.left ?? 0,
              // Hide until first measurement so we never paint at (0,0).
              visibility: coords ? 'visible' : 'hidden',
            }}
            role="tooltip"
            aria-hidden={coords ? undefined : true}
            onMouseEnter={cancelHide}
            onMouseLeave={scheduleHideTooltip}
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
