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
 * Returns null when the trigger has no layout (zero rect at origin) so the
 * caller can render hidden until a real measurement is available, instead
 * of painting at (0, 0).
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
