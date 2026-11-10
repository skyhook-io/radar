// A tap fires a burst of mouse-compatibility events (mouseover, mouseenter)
// and focuses the tapped control, and when the tap changes the layout the
// browser can report a hover on whatever now sits under the finger. Hover and
// focus affordances (tooltips, hover cards) that react to that burst pop open
// over whatever the tap just opened. This records the last touch or pen
// pointer-down so they can tell a tap's side effects from a real hover or a
// keyboard focus.
const TOUCH_INTENT_MS = 800

let lastTouchAt = Number.NEGATIVE_INFINITY

if (typeof document !== 'undefined') {
  document.addEventListener(
    'pointerdown',
    (e) => {
      if (e.pointerType !== 'mouse') lastTouchAt = performance.now()
    },
    { capture: true, passive: true },
  )
  // Keyboard use ends the tap's window: a Tab right after a tap (a touchscreen
  // laptop) is real keyboard focus.
  document.addEventListener(
    'keydown',
    () => {
      lastTouchAt = Number.NEGATIVE_INFINITY
    },
    { capture: true, passive: true },
  )
}

/** True right after a touch or pen press: a mouseenter or focus now is the
 *  tap's side effect, not a hover or keyboard navigation. */
export function isTouchIntent(now = performance.now()): boolean {
  return now - lastTouchAt < TOUCH_INTENT_MS
}
