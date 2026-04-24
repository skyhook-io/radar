/**
 * Explicit color palettes for the log viewer.
 *
 * The log viewer is intentionally self-contained: it does NOT use theme tokens
 * (`text-theme-*`, `bg-theme-*`, etc.) because those are driven by CSS
 * `light-dark()` and don't resolve correctly through the log viewer's forced
 * `color-scheme` container. Instead it flips between two explicit palettes
 * based on its own `isDark` state (toggled by a Sun/Moon button in the
 * toolbar, persisted to `localStorage['radar-logs-dark']`).
 *
 * All class strings are static literals so Tailwind's class scanner picks them
 * up — do not construct them dynamically.
 */

/** All color-related class strings used inside the log viewer. */
export interface LogPalette {
  // Container / surfaces
  containerBg: string
  toolbarBg: string
  toolbarBgMuted: string
  menuBg: string
  elevatedBg: string

  // Borders
  border: string
  borderLight: string

  // Text
  textPrimary: string
  textSecondary: string
  textTertiary: string
  textDisabled: string

  // Placeholder (plain class, applied via `placeholder-*` below)
  placeholder: string

  // Hover states
  hoverBg: string
  hoverSurface: string
  hoverText: string

  // Row highlight (current search match)
  currentMatchBg: string

  // Level-filter button active colors (per-level, full className)
  levelActiveError: string
  levelActiveWarn: string
  levelActiveInfo: string
  levelActiveDebug: string

  // Level-badge colors used inside StructuredLogLine
  levelBadgeError: string
  levelBadgeWarn: string
  levelBadgeInfo: string
  levelBadgeDebug: string
  levelBadgeNeutral: string
}

const DARK_PALETTE: LogPalette = {
  containerBg: 'bg-slate-950',
  toolbarBg: 'bg-slate-900',
  toolbarBgMuted: 'bg-slate-900/60',
  menuBg: 'bg-slate-900',
  elevatedBg: 'bg-slate-800',

  border: 'border-slate-800',
  borderLight: 'border-slate-700',

  textPrimary: 'text-slate-100',
  textSecondary: 'text-slate-400',
  textTertiary: 'text-slate-500',
  textDisabled: 'text-slate-600',

  placeholder: 'placeholder-slate-600',

  hoverBg: 'hover:bg-slate-800',
  hoverSurface: 'hover:bg-slate-800/50',
  hoverText: 'hover:text-slate-100',

  currentMatchBg: 'bg-yellow-500/10',

  levelActiveError: 'bg-red-500/20 text-red-400 border-red-500/40',
  levelActiveWarn: 'bg-amber-500/20 text-amber-400 border-amber-500/40',
  levelActiveInfo: 'bg-blue-500/20 text-blue-400 border-blue-500/40',
  levelActiveDebug: 'bg-slate-700 text-slate-300 border-slate-600',

  levelBadgeError: 'bg-red-500/20 text-red-400 border border-red-500/40',
  levelBadgeWarn: 'bg-amber-500/20 text-amber-400 border border-amber-500/40',
  levelBadgeInfo: 'bg-blue-500/20 text-blue-400 border border-blue-500/40',
  levelBadgeDebug: 'bg-slate-700 text-slate-300 border border-slate-600',
  levelBadgeNeutral: 'bg-slate-800 text-slate-400 border border-slate-700',
}

const LIGHT_PALETTE: LogPalette = {
  containerBg: 'bg-slate-50',
  toolbarBg: 'bg-slate-100',
  toolbarBgMuted: 'bg-slate-100/60',
  menuBg: 'bg-white',
  elevatedBg: 'bg-white',

  border: 'border-slate-200',
  borderLight: 'border-slate-300',

  textPrimary: 'text-slate-900',
  textSecondary: 'text-slate-600',
  textTertiary: 'text-slate-400',
  textDisabled: 'text-slate-300',

  placeholder: 'placeholder-slate-400',

  hoverBg: 'hover:bg-slate-200',
  hoverSurface: 'hover:bg-slate-200/60',
  hoverText: 'hover:text-slate-900',

  currentMatchBg: 'bg-yellow-200/60',

  levelActiveError: 'bg-red-100 text-red-700 border-red-400',
  levelActiveWarn: 'bg-amber-100 text-amber-700 border-amber-400',
  levelActiveInfo: 'bg-blue-100 text-blue-700 border-blue-400',
  levelActiveDebug: 'bg-slate-200 text-slate-700 border-slate-400',

  levelBadgeError: 'bg-red-100 text-red-700 border border-red-400',
  levelBadgeWarn: 'bg-amber-100 text-amber-700 border border-amber-400',
  levelBadgeInfo: 'bg-blue-100 text-blue-700 border border-blue-400',
  levelBadgeDebug: 'bg-slate-200 text-slate-700 border border-slate-400',
  levelBadgeNeutral: 'bg-slate-100 text-slate-600 border border-slate-300',
}

/** Get the palette for the current dark/light mode. */
export function getLogPalette(isDark: boolean): LogPalette {
  return isDark ? DARK_PALETTE : LIGHT_PALETTE
}

/** Per-level content color (log text body). */
export function getLogLevelColor(
  level: 'error' | 'warn' | 'info' | 'debug' | 'unknown',
  isDark: boolean,
): string {
  if (isDark) {
    switch (level) {
      case 'error': return 'text-red-400'
      case 'warn': return 'text-amber-400'
      case 'info': return 'text-blue-400'
      case 'debug': return 'text-slate-400'
      default: return 'text-slate-100'
    }
  }
  switch (level) {
    case 'error': return 'text-red-700'
    case 'warn': return 'text-amber-600'
    case 'info': return 'text-blue-700'
    case 'debug': return 'text-slate-500'
    default: return 'text-slate-900'
  }
}
