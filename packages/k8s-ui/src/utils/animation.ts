/**
 * Central animation presets for consistent motion across the UI.
 *
 * One curve, a handful of durations, and class fragments that apply them.
 * Every surface that opens, closes, expands or collapses takes its timing
 * from here — the hub (radar-hub-web) re-exports this module so Radar Cloud
 * chrome and the embedded Radar views move alike.
 *
 * Property choice: geometry that has to move (grid-template-rows on a
 * disclosure, width on a drawer) is animated directly; everything else is
 * transform + opacity so it stays GPU-composited.
 */

// -- Easing ------------------------------------------------------------------

/** The one UI curve. iOS-style decelerate: fast out of the gate, gentle
 *  settle. Used for disclosures, menus, dialogs, sheets and drawers alike so
 *  a click reads as the cause of the motion and the content is legible early.
 *  Chosen over Material's standard curve in a side-by-side on real content. */
export const CSS_EASE = 'cubic-bezier(0.32, 0.72, 0, 1)'
/** Alias with the intent in the name; prefer it in new code. */
export const EASE_UI = CSS_EASE

// Tailwind-safe easing (no spaces — Tailwind splits class strings on whitespace)
const TW_EASE = 'ease-[cubic-bezier(0.32,0.72,0,1)]'
/** The same curve as a Tailwind class fragment, for class-string composition. */
export const TW_EASE_UI = TW_EASE

// -- Durations (ms) ----------------------------------------------------------

/** Standard UI transitions: drawers, toasts. */
export const DURATION_NORMAL = 300
/** Dock height, subtle layout shifts. Deliberately fast: the dock is direct
 *  manipulation the user performs dozens of times a day. */
export const DURATION_DOCK = 150
/** Toast exit animation */
export const DURATION_TOAST_EXIT = 200

/** In-flow disclosure (Collapse / drawer sections / row expanders): panel
 *  and caret share this, open and close alike. Symmetric on purpose — an
 *  interrupted toggle reverses cleanly when both directions run one clock. */
export const DURATION_DISCLOSURE = 300

/** Menus, popovers, listboxes. Exits are shorter than entrances: a dismissal
 *  should feel immediate. */
export const DURATION_MENU_IN = 140
export const DURATION_MENU_OUT = 100
/** Hover-revealed row actions (HOVER_REVEAL_*). In the range of Radar's other
 *  hover effects (150–200ms) rather than the menu clock, which reads as a
 *  flicker on something the pointer merely passes over. The exit is shorter
 *  so leaving a row feels immediate. The preset class strings spell these
 *  values out for Tailwind's scanner; a test keeps them in step. */
export const DURATION_HOVER_REVEAL_IN = 200
export const DURATION_HOVER_REVEAL_OUT = 120
/** Hover intent before a row action comes in: a pointer sweeping down a long
 *  list passes each row in well under this, so actions don't ripple in behind
 *  it. Entrance only; leaving a row hides its action at once. */
export const HOVER_INTENT_DELAY = 60
/** Modal dialogs and command palettes. */
export const DURATION_DIALOG_IN = 220
export const DURATION_DIALOG_OUT = 160
/** Bottom sheets: more travel, a little more time. */
export const DURATION_SHEET_IN = 280
export const DURATION_SHEET_OUT = 220

/** Drawer ↔ fullscreen expand/collapse morph. Longer than DURATION_NORMAL so a
 *  large width change advances in small per-frame steps (reads as fluid, not
 *  abrupt). Drives the frame width, the content crossfade, and the JS window
 *  together — keep them in lockstep. */
export const DURATION_DRAWER_MORPH = 260
/** Decelerate curve — fast start, gentle settle. Reads as snappy/responsive (the
 *  earlier ease-in-out's slow start felt sluggish). Safe now that the pinned-layer
 *  crossfade fixed the reflow that originally needed the gentle start. */
export const EASE_DRAWER_MORPH = 'cubic-bezier(0.2, 0.8, 0.2, 1)'

// -- Reduced motion -----------------------------------------------------------

/** True when the OS asks for reduced motion. Presence hooks use it to dispose
 *  overlays at logical close instead of after an exit transition that the app
 *  stylesheet has already zeroed. SSR- and test-safe. */
export function prefersReducedMotion(): boolean {
  if (typeof window === 'undefined' || typeof window.matchMedia !== 'function') return false
  return window.matchMedia('(prefers-reduced-motion: reduce)').matches
}

// -- Tailwind class presets ---------------------------------------------------
// Reusable class fragments — import and spread into clsx() calls.

/** Disclosure panel: grid-template-rows 0fr ↔ 1fr on the wrapper. Pair with
 *  TRANSITION_CHEVRON on the caret so both run one clock. */
export const TRANSITION_DISCLOSURE =
  `transition-[grid-template-rows] duration-300 ${TW_EASE} motion-reduce:transition-none`

/** Disclosure caret (rotate). Same duration and curve as the panel it opens. */
export const TRANSITION_CHEVRON =
  `transition-transform duration-300 ${TW_EASE} motion-reduce:transition-none`

/** Drawer slide from right — use with translate-x-0/translate-x-full + opacity.
 *  Also handles expand/collapse (width) so a single transition covers both. */
export const TRANSITION_DRAWER =
  `transition-[translate,opacity,width] duration-300 ${TW_EASE} will-change-[transform,width] motion-reduce:transition-none`

/** Overlay backdrop fade. Duration comes from the presence state (see
 *  overlayTransitionStyle) so the exit can be shorter than the entrance. */
export const TRANSITION_BACKDROP =
  `transition-opacity ${TW_EASE} motion-reduce:transition-none`

/** Overlay panel scale + fade — use with scale-100/scale-[0.97] + opacity.
 *  Duration comes from overlayTransitionStyle, not from this fragment. */
export const TRANSITION_PANEL =
  `transition-[translate,scale,opacity] ${TW_EASE} motion-reduce:transition-none`

/** Menu / popover: translate + scale + opacity from its anchor corner. Pair
 *  with a transform-origin utility so it grows from the trigger. */
export const TRANSITION_MENU =
  `transition-[translate,scale,opacity] ${TW_EASE} motion-reduce:transition-none`

/** Hover-revealed row action, the resting half: the row content the action
 *  replaces (an age, a chip) lifts 3px and fades on the exit clock. Needs a
 *  `group` ancestor; keyboard reveal keys on :focus-visible inside the group,
 *  so a row left focused by a mouse click doesn't keep its action up. Timing
 *  is DURATION_HOVER_REVEAL_IN / _OUT and HOVER_INTENT_DELAY. */
export const HOVER_REVEAL_REST =
  `transition-[opacity,translate] duration-200 ${TW_EASE} motion-reduce:transition-none group-hover:opacity-0 group-hover:-translate-y-[3px] group-hover:duration-120 group-hover:delay-60 group-has-[:focus-visible]:opacity-0 group-has-[:focus-visible]:-translate-y-[3px] group-has-[:focus-visible]:duration-120 group-has-[:focus-visible]:delay-60`

/** Hover-revealed row action, the action half: rises 3px into place on the
 *  entrance clock after the hover-intent delay, drops out on the exit clock.
 *  Never a hit target itself, so its padding and any backdrop leave clicks to
 *  the row underneath; put HOVER_REVEAL_HIT on the control inside it. Pair
 *  with HOVER_REVEAL_REST in one group. */
export const HOVER_REVEAL_ACTION =
  `transition-[opacity,translate] duration-120 ${TW_EASE} motion-reduce:transition-none pointer-events-none opacity-0 translate-y-[3px] group-hover:opacity-100 group-hover:translate-y-0 group-hover:duration-200 group-hover:delay-60 group-has-[:focus-visible]:opacity-100 group-has-[:focus-visible]:translate-y-0 group-has-[:focus-visible]:duration-200 group-has-[:focus-visible]:delay-60`

/** Hover-revealed row action, the control inside it: takes clicks only while
 *  shown. pointer-events transitions as a discrete property on the same
 *  clock, so it flips partway through the entrance rather than at hover start
 *  and a click during the intent delay lands on the row. */
export const HOVER_REVEAL_HIT =
  `transition-[pointer-events] transition-discrete duration-120 ${TW_EASE} motion-reduce:transition-none pointer-events-none group-hover:pointer-events-auto group-hover:duration-200 group-hover:delay-60 group-has-[:focus-visible]:pointer-events-auto group-has-[:focus-visible]:duration-200 group-has-[:focus-visible]:delay-60`

export type OverlayKind = 'menu' | 'dialog' | 'sheet' | 'drawer'

const OVERLAY_DURATIONS: Record<OverlayKind, { in: number; out: number }> = {
  menu: { in: DURATION_MENU_IN, out: DURATION_MENU_OUT },
  dialog: { in: DURATION_DIALOG_IN, out: DURATION_DIALOG_OUT },
  sheet: { in: DURATION_SHEET_IN, out: DURATION_SHEET_OUT },
  // Side drawers keep their symmetric slide (TRANSITION_DRAWER); this entry
  // gives their backdrop the same clock.
  drawer: { in: DURATION_NORMAL, out: DURATION_NORMAL },
}

/** Exit duration for an overlay kind — what useAnimatedUnmount should wait. */
export function overlayExitMs(kind: OverlayKind): number {
  return OVERLAY_DURATIONS[kind].out
}

/** Inline style that gives an overlay its entrance or exit duration. Apply to
 *  the element that carries TRANSITION_PANEL / TRANSITION_MENU / TRANSITION_BACKDROP;
 *  the class fragments deliberately carry no duration so the two directions
 *  can differ. */
export function overlayTransitionStyle(isOpen: boolean, kind: OverlayKind): { transitionDuration: string } {
  const d = OVERLAY_DURATIONS[kind]
  return { transitionDuration: `${isOpen ? d.in : d.out}ms` }
}
