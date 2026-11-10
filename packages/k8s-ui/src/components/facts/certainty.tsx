import { WithTooltip } from '../ui/Tooltip'

/**
 * How exactly a value is known. A value read only in part is a lower bound,
 * never shown as if exact.
 */
export type Certainty = 'exact' | 'lower_bound' | 'upper_bound' | 'unknown'

export function certaintyGlyph(certainty: Certainty): string {
  if (certainty === 'exact') return '='
  if (certainty === 'lower_bound') return '≥'
  if (certainty === 'upper_bound') return '≤'
  return '?'
}

export function certaintyValueLabel(certainty: Certainty): string {
  if (certainty === 'exact') return 'Exact'
  if (certainty === 'lower_bound') return 'Lower bound'
  if (certainty === 'upper_bound') return 'Upper bound'
  return 'Unknown certainty'
}

export function CertaintyGlyph({ certainty, title }: { certainty: Certainty; title?: string }) {
  return (
    <WithTooltip tip={title ?? certaintyValueLabel(certainty)}>
      <span
        tabIndex={0}
        role="note"
        aria-label={title ?? certaintyValueLabel(certainty)}
        className="cursor-help rounded border border-theme-border-light px-1 font-mono text-[10px] leading-tight text-theme-text-tertiary focus-visible:outline focus-visible:outline-2 focus-visible:outline-skyhook-500"
      >
        {certaintyGlyph(certainty)}
      </span>
    </WithTooltip>
  )
}
