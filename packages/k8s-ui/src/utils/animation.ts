/**
 * Central animation presets for consistent motion across the UI.
 *
 * All animations use GPU-composited properties only (translate, scale, opacity)
 * to guarantee 60fps. The iOS-like spring easing gives a fast-in, gentle-out feel.
 */

// -- Easing ------------------------------------------------------------------

// Tailwind-safe easing (no spaces — Tailwind splits class strings on whitespace)
const TW_EASE = 'ease-[cubic-bezier(0.32,0.72,0,1)]'

/** Raw CSS easing for inline transition strings */
export const CSS_EASE = 'cubic-bezier(0.32, 0.72, 0, 1)'

// -- Durations (ms) ----------------------------------------------------------

/** Standard UI transitions: overlays, panels, toasts */
export const DURATION_NORMAL = 300
/** Dock height, subtle layout shifts */
export const DURATION_DOCK = 150
/** Toast exit animation */
export const DURATION_TOAST_EXIT = 200

/** Drawer ↔ fullscreen expand/collapse morph. Longer than DURATION_NORMAL so a
 *  large width change advances in small per-frame steps (reads as fluid, not
 *  abrupt). Drives the frame width, the content crossfade, and the JS window
 *  together — keep them in lockstep. */
export const DURATION_DRAWER_MORPH = 260
/** Decelerate curve — fast start, gentle settle. Reads as snappy/responsive (the
 *  earlier ease-in-out's slow start felt sluggish). Safe now that the pinned-layer
 *  crossfade fixed the reflow that originally needed the gentle start. */
export const EASE_DRAWER_MORPH = 'cubic-bezier(0.2, 0.8, 0.2, 1)'

// -- Tailwind class presets ---------------------------------------------------
// Reusable class fragments — import and spread into clsx() calls.

/** Drawer slide from right — use with translate-x-0/translate-x-full + opacity.
 *  Also handles expand/collapse (width) so a single transition covers both. */
export const TRANSITION_DRAWER =
  `transition-[translate,opacity,width] duration-300 ${TW_EASE} will-change-[transform,width]`


/** Overlay backdrop fade */
export const TRANSITION_BACKDROP =
  'transition-opacity duration-200'

/** Overlay panel scale + fade — use with scale-100/scale-[0.97] + opacity */
export const TRANSITION_PANEL =
  `transition-[translate,scale,opacity] duration-300 ${TW_EASE}`
