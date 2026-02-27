/**
 * Central animation presets for consistent motion across the UI.
 *
 * All animations use GPU-composited properties only (translate, scale, opacity)
 * to guarantee 60fps. The iOS-like spring easing gives a fast-in, gentle-out feel.
 */

// -- Easing ------------------------------------------------------------------

// Tailwind-safe easing (no spaces — Tailwind splits class strings on whitespace)
const TW_EASE = 'ease-[cubic-bezier(0.32,0.72,0,1)]'

// -- Durations (ms) ----------------------------------------------------------

/** Standard UI transitions: overlays, panels, toasts */
export const DURATION_NORMAL = 300
/** Dock height, subtle layout shifts */
export const DURATION_DOCK = 250
/** Toast exit animation */
export const DURATION_TOAST_EXIT = 200

// -- Tailwind class presets ---------------------------------------------------
// Reusable class fragments — import and spread into clsx() calls.

/** Drawer slide from right — use with translate-x-0/translate-x-full + opacity */
export const TRANSITION_DRAWER =
  `transition-[translate,opacity] duration-300 ${TW_EASE} will-change-transform`

/** Overlay backdrop fade */
export const TRANSITION_BACKDROP =
  'transition-opacity duration-200'

/** Overlay panel scale + fade — use with scale-100/scale-[0.97] + opacity */
export const TRANSITION_PANEL =
  `transition-[translate,scale,opacity] duration-300 ${TW_EASE}`
