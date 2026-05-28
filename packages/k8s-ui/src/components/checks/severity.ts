import type { CheckSeverity } from './types'

// The visual language for the 4-tier Checks severity ladder. Pale-pastel tints
// with hand-rolled dark variants (no theme token covers these). One hue per
// tier: red=critical, orange=high, amber=medium, slate=low — read the queue's
// left rail top-to-bottom and severity is obvious without reading a word.
//
// Class strings are literal so each consuming app's Tailwind @source scan emits
// them.

export const SEVERITY_LABEL: Record<CheckSeverity, string> = {
  critical: 'Critical',
  high: 'High',
  medium: 'Medium',
  low: 'Low',
}

// Pill badge — the loud, explicit severity signal on rows + drawer header.
export const SEVERITY_BADGE_CLASS: Record<CheckSeverity, string> = {
  critical: 'bg-red-50 text-red-700 ring-1 ring-red-200 dark:bg-red-950/50 dark:text-red-300 dark:ring-red-900',
  high: 'bg-orange-50 text-orange-700 ring-1 ring-orange-200 dark:bg-orange-950/50 dark:text-orange-300 dark:ring-orange-900',
  medium: 'bg-amber-50 text-amber-700 ring-1 ring-amber-200 dark:bg-amber-950/50 dark:text-amber-300 dark:ring-amber-900',
  low: 'bg-slate-100 text-slate-600 ring-1 ring-slate-200 dark:bg-slate-800/60 dark:text-slate-300 dark:ring-slate-700',
}

// Solid fill — dots + the proportional distribution bar segments.
export const SEVERITY_FILL_CLASS: Record<CheckSeverity, string> = {
  critical: 'bg-red-500',
  high: 'bg-orange-500',
  medium: 'bg-amber-500',
  low: 'bg-slate-400',
}

export const SEVERITY_TEXT_CLASS: Record<CheckSeverity, string> = {
  critical: 'text-red-600 dark:text-red-400',
  high: 'text-orange-600 dark:text-orange-400',
  medium: 'text-amber-600 dark:text-amber-400',
  low: 'text-slate-500 dark:text-slate-400',
}

// Left accent rail on a queue row — the scan-down severity cue. Pairs a colored
// 2px border with a faint severity-tinted background that deepens on hover.
export const SEVERITY_RAIL_CLASS: Record<CheckSeverity, string> = {
  critical: 'border-l-red-500 hover:bg-red-50/40 dark:hover:bg-red-950/20',
  high: 'border-l-orange-500 hover:bg-orange-50/40 dark:hover:bg-orange-950/20',
  medium: 'border-l-amber-500 hover:bg-amber-50/30 dark:hover:bg-amber-950/15',
  low: 'border-l-slate-300 dark:border-l-slate-600 hover:bg-theme-hover/40',
}

// Category accent — a quiet tag (severity is the loud one). Security is the
// headline beat, so it gets the most distinct hue.
const CATEGORY_BADGE_CLASS: Record<string, string> = {
  Security: 'bg-violet-50 text-violet-700 ring-1 ring-violet-200 dark:bg-violet-950/40 dark:text-violet-300 dark:ring-violet-900',
  Reliability: 'bg-sky-50 text-sky-700 ring-1 ring-sky-200 dark:bg-sky-950/40 dark:text-sky-300 dark:ring-sky-900',
  Efficiency: 'bg-teal-50 text-teal-700 ring-1 ring-teal-200 dark:bg-teal-950/40 dark:text-teal-300 dark:ring-teal-900',
}

export function categoryBadgeClass(category: string): string {
  return (
    CATEGORY_BADGE_CLASS[category] ??
    'bg-theme-elevated text-theme-text-secondary ring-1 ring-theme-border'
  )
}
