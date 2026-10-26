import { useLayoutEffect, type RefObject } from 'react'

/** Keeps an open menu or popover horizontally inside the viewport. Anchored
 *  menus open at their trigger's edge with a fixed minimum width, so on a
 *  narrow screen a trigger near the right edge pushes the menu off-screen.
 *  After each layout this shifts the element back by `margin-left` (not
 *  `translate`: Tailwind's translate utilities, used by the overlay motion,
 *  set that same property) and caps its width to the viewport.
 *
 *  Runs after every render while `active`, so a menu whose content changes
 *  width (a filter narrowing a list) is re-measured. */
export function useKeepInViewport(ref: RefObject<HTMLElement | null>, active: boolean, margin = 8): void {
  useLayoutEffect(() => {
    const el = ref.current
    if (!active || !el) return
    el.style.marginLeft = ''
    el.style.maxWidth = `calc(100vw - ${2 * margin}px)`
    const viewport = document.documentElement.clientWidth
    if (el.offsetWidth > viewport - 2 * margin) el.style.minWidth = '0'
    const { left, right } = el.getBoundingClientRect()
    const shift = keepInViewportShift(left, right, viewport, margin)
    if (shift !== 0) el.style.marginLeft = `${shift}px`
  })
}

/** Horizontal shift (px) that brings [left, right] inside [margin, viewport -
 *  margin], preferring the left edge when the element is wider than both. */
export function keepInViewportShift(left: number, right: number, viewport: number, margin: number): number {
  let shift = 0
  if (right > viewport - margin) shift = viewport - margin - right
  if (left + shift < margin) shift = margin - left
  return shift
}
