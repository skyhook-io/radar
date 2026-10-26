import { useLayoutEffect, useEffect, type RefObject } from 'react'

/** Keeps an open menu or popover horizontally inside the visible area: the
 *  viewport, narrowed by any ancestor that clips horizontally (a content
 *  column beside a nav rail clips what slides under the rail). Anchored menus
 *  open at their trigger's edge with a fixed minimum width, so on a narrow
 *  screen a trigger near the edge pushes the menu out of view. After each
 *  layout, and on resize while open, this shifts the element back with the
 *  `translate` property (which moves it whichever side it's anchored to), and
 *  narrows it only if it can't fit. Don't use it on an element whose own
 *  motion animates `translate`: the inline value would override it.
 *
 *  Runs after every render while `active`, so a menu whose content changes
 *  width (a filter narrowing a list) is re-measured. */
export function useKeepInViewport(ref: RefObject<HTMLElement | null>, active: boolean, margin = 8): void {
  useLayoutEffect(() => {
    if (active) keepInView(ref.current, margin)
  })
  useEffect(() => {
    if (!active) return
    const onResize = () => keepInView(ref.current, margin)
    window.addEventListener('resize', onResize)
    return () => window.removeEventListener('resize', onResize)
  }, [ref, active, margin])
}

// Width overrides this hook applied, keyed by element, with the inline values
// they replaced, so a later measurement can put back exactly what the caller set
// (React won't re-apply an inline style whose value it thinks is unchanged).
const sizedBy = new WeakMap<HTMLElement, { minWidth: string; maxWidth: string }>()

function keepInView(el: HTMLElement | null, margin: number): void {
  if (!el) return
  // Measure and move with transitions off. Under reduced motion every
  // element gets a near-zero transition on all properties, and a transitioned
  // element reports its pre-change box in the same frame: a menu just moved
  // from left 0 to its trigger still measured at 0, and was pushed off its
  // trigger by the margin.
  const transition = el.style.transition
  el.style.transition = 'none'
  place(el, margin)
  el.getBoundingClientRect() // apply the move before transitions return
  el.style.transition = transition
}

function place(el: HTMLElement, margin: number): void {
  el.style.translate = ''
  const saved = sizedBy.get(el)
  if (saved) {
    el.style.minWidth = saved.minWidth
    el.style.maxWidth = saved.maxWidth
    sizedBy.delete(el)
  }
  const [boundLeft, boundRight] = visibleBounds(el, margin)
  const room = boundRight - boundLeft
  // Only a menu that can't fit gets resized; otherwise the caller's own
  // minimum and maximum widths stand.
  if (el.offsetWidth > room) {
    sizedBy.set(el, { minWidth: el.style.minWidth, maxWidth: el.style.maxWidth })
    el.style.minWidth = '0'
    el.style.maxWidth = `${room}px`
  }
  const { left, right } = el.getBoundingClientRect()
  const shift = keepInViewportShift(left, right, boundLeft, boundRight)
  if (shift !== 0) el.style.translate = `${shift}px 0`
}

function visibleBounds(el: HTMLElement, margin: number): [number, number] {
  let left = margin
  let right = document.documentElement.clientWidth - margin
  for (let p = el.parentElement; p && p !== document.body; p = p.parentElement) {
    if (getComputedStyle(p).overflowX === 'visible') continue
    const r = p.getBoundingClientRect()
    left = Math.max(left, r.left + margin)
    right = Math.min(right, r.right - margin)
  }
  return [left, Math.max(left, right)]
}

/** Horizontal shift (px) that brings [left, right] inside [boundLeft,
 *  boundRight], preferring the left edge when the element is wider. */
export function keepInViewportShift(left: number, right: number, boundLeft: number, boundRight: number): number {
  let shift = 0
  if (right > boundRight) shift = boundRight - right
  if (left + shift < boundLeft) shift = boundLeft - left
  return shift
}
