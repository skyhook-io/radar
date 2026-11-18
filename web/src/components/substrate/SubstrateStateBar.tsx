import { clsx } from 'clsx'
import type { SubstrateActorState, SubstrateStateCounts } from '../../api/substrateWorkspace'
import { ACTOR_STATE_LABEL, actorStateFill, stateBarSegments } from './readings'

/**
 * The actors by state as one bar: a mark per actor while there are few enough
 * to count, then segments in proportion. The bar is a picture; the legend
 * beneath carries the numbers, and each state opens the actors in it.
 */
export function SubstrateStateBar({
  counts,
  onSelectState,
  compact,
}: {
  counts: SubstrateStateCounts
  onSelectState?: (state: SubstrateActorState) => void
  /** A small bar without the legend, for a table cell; its numbers go in the label. */
  compact?: boolean
}) {
  const { segments, countable } = stateBarSegments(counts)
  const label = segments.map((s) => `${ACTOR_STATE_LABEL[s.state]} ${s.count.toLocaleString('en-US')}`).join(', ')
  if (counts.total === 0) {
    return compact ? <span className="text-theme-text-tertiary">none</span> : <p className="text-sm text-theme-text-tertiary">No actors.</p>
  }
  const bar = (
    <div
      role="img"
      aria-label={`${counts.total.toLocaleString('en-US')} actors: ${label}`}
      className={clsx('flex w-full overflow-hidden rounded', compact ? 'h-1.5 gap-px' : 'h-3', countable && !compact ? 'gap-0.5' : !compact && 'gap-px')}
    >
      {countable
        ? segments.flatMap((s) => Array.from({ length: s.count }, (_, i) => <span key={`${s.state}-${i}`} className={clsx('min-w-0 flex-1 rounded-[2px]', actorStateFill(s.state))} />))
        : segments.map((s) => (
            // A state with a handful of actors among thousands keeps a visible sliver.
            <span key={s.state} style={{ flexGrow: s.share, flexBasis: 0, minWidth: 3 }} className={clsx('h-full', actorStateFill(s.state))} />
          ))}
    </div>
  )
  if (compact) return bar
  return (
    <div className="space-y-2">
      {bar}
      <ul className="flex flex-wrap gap-x-5 gap-y-1 text-sm" aria-label="Actors by state">
        {segments.map((s) => (
          <li key={s.state} className="flex items-center gap-1.5">
            <span aria-hidden className={clsx('inline-block h-1.5 w-1.5 rounded-full', actorStateFill(s.state))} />
            {onSelectState ? (
              <button type="button" onClick={() => onSelectState(s.state)} className="text-theme-text-secondary hover:text-theme-text-primary hover:underline">
                {ACTOR_STATE_LABEL[s.state]} <span className="font-medium text-theme-text-primary">{s.count.toLocaleString('en-US')}</span>
              </button>
            ) : (
              <span className="text-theme-text-secondary">
                {ACTOR_STATE_LABEL[s.state]} <span className="font-medium text-theme-text-primary">{s.count.toLocaleString('en-US')}</span>
              </span>
            )}
          </li>
        ))}
      </ul>
    </div>
  )
}
