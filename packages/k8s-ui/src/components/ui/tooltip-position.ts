// Pure helpers for Tooltip positioning + dismissal logic.
//
// Extracted from Tooltip.tsx so the geometry can be unit-tested without a
// DOM. The component itself stays a thin glue layer that calls these.

export type TooltipPlacement = 'top' | 'bottom' | 'left' | 'right'

export interface Rect {
  top: number
  left: number
  width: number
  height: number
}

export interface Size {
  width: number
  height: number
}

export interface Viewport {
  width: number
  height: number
}

export interface ComputeTooltipPositionInput {
  triggerRect: Rect
  tooltipSize: Size
  position: TooltipPlacement
  viewport: Viewport
  padding?: number
}

/**
 * Computes the absolute (top, left) for a tooltip portal given the trigger
 * rect, the tooltip's measured size, the desired placement, and the
 * viewport. Pure, deterministic — same input produces the same output, no
 * DOM or window access. Includes viewport-clamping with a single-flip
 * fallback when the preferred placement would clip.
 *
 * Returns null when the trigger has zero area AND zero offset — that means
 * the trigger isn't laid out yet (initial paint). The caller should skip
 * showing the tooltip until a real measurement is available, instead of
 * defaulting to (0, 0) which causes the documented "tooltip text appears
 * at viewport top-left, overlapping the logo" bug.
 */
export function computeTooltipPosition({
  triggerRect,
  tooltipSize,
  position,
  viewport,
  padding = 8,
}: ComputeTooltipPositionInput): { top: number; left: number } | null {
  const triggerHasNoLayout =
    triggerRect.width === 0 &&
    triggerRect.height === 0 &&
    triggerRect.top === 0 &&
    triggerRect.left === 0
  if (triggerHasNoLayout) {
    return null
  }

  const { width: tw, height: th } = tooltipSize

  let top = 0
  let left = 0

  switch (position) {
    case 'top':
      top = triggerRect.top - th - 6
      left = triggerRect.left + triggerRect.width / 2 - tw / 2
      break
    case 'bottom':
      top = triggerRect.top + triggerRect.height + 6
      left = triggerRect.left + triggerRect.width / 2 - tw / 2
      break
    case 'left':
      top = triggerRect.top + triggerRect.height / 2 - th / 2
      left = triggerRect.left - tw - 6
      break
    case 'right':
      top = triggerRect.top + triggerRect.height / 2 - th / 2
      left = triggerRect.left + triggerRect.width + 6
      break
  }

  if (left < padding) left = padding
  if (left + tw > viewport.width - padding) {
    left = viewport.width - tw - padding
  }
  if (top < padding) {
    top = triggerRect.top + triggerRect.height + 6
  }
  if (top + th > viewport.height - padding) {
    top = triggerRect.top - th - 6
  }
  if (top < padding) top = padding

  return { top, left }
}
