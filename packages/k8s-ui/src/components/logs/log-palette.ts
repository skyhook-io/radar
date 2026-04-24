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
  /** Standalone error text (inline "error=..." in structured lines, inline validation errors). */
  textError: string
  /** Accent/link text — blue-family. Used for toolbar toggles, "select all" etc. */
  textAccent: string

  // Placeholder (plain class, applied via `placeholder-*` below)
  placeholder: string

  // Hover states
  hoverBg: string
  hoverSurface: string
  hoverText: string
  /** Active/selected toolbar controls inside the viewer. */
  toolbarActive: string

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

  /**
   * Pod label colors for WorkloadLogsViewer (aggregated logs across pods).
   * Pairs: one class for the name text, one for the filter-list dot.
   * Pods are round-robined through this array.
   */
  podColors: Array<{ text: string; bg: string }>

  /**
   * Syntax-highlight colors for structured JSON/logfmt renderings.
   * Applied as inline `style={{ color }}` (not classes) because the values
   * come from log-format.ts and feed React's style prop.
   */
  syntaxKey: string
  syntaxString: string
  syntaxNumber: string
  syntaxBoolean: string
  syntaxNull: string
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
  textError: 'text-red-400',
  textAccent: 'text-blue-400',

  placeholder: 'placeholder-slate-600',

  hoverBg: 'hover:bg-slate-800',
  hoverSurface: 'hover:bg-slate-800/50',
  hoverText: 'hover:text-slate-100',
  toolbarActive: 'bg-slate-700 text-slate-100 hover:bg-slate-600',

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

  podColors: [
    { text: 'text-blue-400', bg: 'bg-blue-400' },
    { text: 'text-emerald-400', bg: 'bg-emerald-400' },
    { text: 'text-amber-400', bg: 'bg-amber-400' },
    { text: 'text-purple-400', bg: 'bg-purple-400' },
    { text: 'text-pink-400', bg: 'bg-pink-400' },
    { text: 'text-cyan-400', bg: 'bg-cyan-400' },
    { text: 'text-orange-400', bg: 'bg-orange-400' },
    { text: 'text-lime-400', bg: 'bg-lime-400' },
  ],

  // Hex values tuned for the `bg-slate-950` container.
  syntaxKey: '#7cacf8',
  syntaxString: '#73c991',
  syntaxNumber: '#e5c07b',
  syntaxBoolean: '#c678dd',
  syntaxNull: '#808080',
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
  textError: 'text-red-700',
  textAccent: 'text-blue-700',

  placeholder: 'placeholder-slate-400',

  hoverBg: 'hover:bg-slate-200',
  hoverSurface: 'hover:bg-slate-200/60',
  hoverText: 'hover:text-slate-900',
  toolbarActive: 'bg-slate-200 text-slate-900 hover:bg-slate-300',

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

  podColors: [
    { text: 'text-blue-700', bg: 'bg-blue-700' },
    { text: 'text-emerald-700', bg: 'bg-emerald-700' },
    { text: 'text-amber-700', bg: 'bg-amber-700' },
    { text: 'text-purple-700', bg: 'bg-purple-700' },
    { text: 'text-pink-700', bg: 'bg-pink-700' },
    { text: 'text-cyan-700', bg: 'bg-cyan-700' },
    { text: 'text-orange-700', bg: 'bg-orange-700' },
    { text: 'text-lime-700', bg: 'bg-lime-700' },
  ],

  // Hex values tuned for the `bg-slate-50` container — darker so they read
  // on a near-white background.
  syntaxKey: '#0b63c0',
  syntaxString: '#2b8a3e',
  syntaxNumber: '#b95f00',
  syntaxBoolean: '#7c3aed',
  syntaxNull: '#6b7280',
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
