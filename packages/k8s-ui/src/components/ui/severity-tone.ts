// Shared severity color vocabulary. The Issue card (2-tier: critical/warning)
// and the Check card (4-tier: critical/high/medium/low) are different severity
// axes, but every tier resolves to one of four hues — so the color strings live
// here once and each card maps its own severity enum onto a tone. A palette
// change lands in one place instead of drifting between the two cards.
//
// Literal class strings (no template interpolation) so Tailwind's scanner sees
// every utility.

export type SeverityTone = 'red' | 'orange' | 'amber' | 'yellow' | 'slate'

// Text color — leading severity glyph + emphasis text. Yellow takes the darker
// -700 step in light mode: its low luminance is illegible at -600 on a light
// surface, where red/orange/amber still clear.
export const TONE_TEXT_CLASS: Record<SeverityTone, string> = {
  red: 'text-red-600 dark:text-red-400',
  orange: 'text-orange-600 dark:text-orange-400',
  amber: 'text-amber-600 dark:text-amber-400',
  yellow: 'text-yellow-700 dark:text-yellow-400',
  slate: 'text-slate-500 dark:text-slate-400',
}

// Solid fill — dots + the proportional distribution-bar segments.
export const TONE_FILL_CLASS: Record<SeverityTone, string> = {
  red: 'bg-red-500',
  orange: 'bg-orange-500',
  amber: 'bg-amber-500',
  yellow: 'bg-yellow-500',
  slate: 'bg-slate-400',
}

// The card's left severity rail — the scan-down cue in a queue. It belongs to
// the whole card, so an open card's rail runs past the header down its body.
export const TONE_RAIL_CLASS: Record<SeverityTone, string> = {
  red: '[--rail:var(--color-red-500)]',
  orange: '[--rail:var(--color-orange-500)]',
  amber: '[--rail:var(--color-amber-500)]',
  yellow: '[--rail:var(--color-yellow-500)]',
  slate: '[--rail:var(--color-slate-300)] dark:[--rail:var(--color-slate-600)]',
}

// A collapsed card's header: a faint hover tint in the card's tone.
export const TONE_ROW_HOVER_CLASS: Record<SeverityTone, string> = {
  red: 'hover:bg-red-50/40 dark:hover:bg-red-950/20',
  orange: 'hover:bg-orange-50/40 dark:hover:bg-orange-950/20',
  amber: 'hover:bg-amber-50/30 dark:hover:bg-amber-950/15',
  yellow: 'hover:bg-yellow-50/30 dark:hover:bg-yellow-950/15',
  slate: 'hover:bg-theme-hover/40',
}

// Solid severity pill — the loud focus signal on an expanded card's header band
// (collapsed cards keep the soft BADGE pill so a long queue stays calm).
// The warm mid-tones (orange/amber/yellow) take dark text: white on them sits
// below WCAG for the 12px pill text.
export const TONE_SOLID_CLASS: Record<SeverityTone, string> = {
  red: 'bg-red-600 text-white',
  orange: 'bg-orange-500 text-orange-950',
  amber: 'bg-amber-400 text-amber-950',
  yellow: 'bg-yellow-400 text-yellow-950',
  slate: 'bg-slate-500 text-white dark:bg-slate-600',
}

// Severity-tinted header band, shown only when a card is expanded: the
// open-state signal. No hover variant, so it doesn't lighten on hover.
export const TONE_HEADER_BAND_CLASS: Record<SeverityTone, string> = {
  red: 'bg-red-50 dark:bg-red-950/40',
  orange: 'bg-orange-50 dark:bg-orange-950/30',
  amber: 'bg-amber-50 dark:bg-amber-950/30',
  yellow: 'bg-yellow-50 dark:bg-yellow-950/30',
  slate: 'bg-slate-50 dark:bg-slate-900/40',
}
