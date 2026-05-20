import { clsx } from 'clsx'
import { GitCompare, X, ArrowRight } from 'lucide-react'
import { Tooltip } from '../ui/Tooltip'
import { pluralToKind } from '../../utils/navigation'
import { SIDE_TONES, type CompareSide, type NamespacedRef } from './types'

export type CompareTrayPick = NamespacedRef

export interface CompareTrayProps {
  /** Plural kind (e.g. "deployments") — used to label the tray. */
  kind: string
  picks: CompareTrayPick[]
  onRemove: (index: number) => void
  /** Called when the user hits the Compare CTA. Only invoked when 2 picks. */
  onCompare: () => void
  /** Exit compare mode entirely (clears picks). */
  onExit: () => void
}

export function CompareTray({ kind, picks, onRemove, onCompare, onExit }: CompareTrayProps) {
  const slotA = picks[0]
  const slotB = picks[1]
  const ready = !!(slotA && slotB)
  const kindLabel = pluralToKind(kind)

  return (
    <div
      role="region"
      aria-label="Compare resources tray"
      className={clsx(
        'shrink-0 border-t border-skyhook-500/30 bg-theme-surface/95 backdrop-blur',
        'shadow-[0_-8px_24px_-12px_rgba(0,0,0,0.35)]',
      )}
    >
      <div className="h-0.5 w-full bg-gradient-to-r from-blue-400/70 via-skyhook-400/40 to-emerald-400/70" />

      {/* Right padding clears the fixed bottom-right overlay buttons (debug / shortcut-help). */}
      <div className="flex items-center gap-3 pl-4 pr-20 py-2.5">
        <div className="flex items-center gap-2 shrink-0">
          <GitCompare className="w-4 h-4 text-skyhook-400" />
          <span className="text-xs font-semibold text-theme-text-primary uppercase tracking-wider">
            Compare {kindLabel}
          </span>
          <span className="text-[10px] text-theme-text-tertiary font-medium px-1.5 py-0.5 rounded bg-theme-elevated">
            {picks.length}/2
          </span>
        </div>

        <div className="flex items-center gap-2 min-w-0 flex-1">
          <PickSlot side="a" pick={slotA} onRemove={() => onRemove(0)} />
          <ArrowRight className="w-3.5 h-3.5 text-theme-text-tertiary shrink-0" />
          <PickSlot side="b" pick={slotB} onRemove={() => onRemove(1)} />
        </div>

        <button
          onClick={onCompare}
          disabled={!ready}
          className={clsx(
            'shrink-0 flex items-center gap-1.5 px-3 py-1.5 text-xs font-medium rounded-lg transition-colors',
            ready
              ? 'btn-brand'
              : 'text-theme-text-disabled bg-theme-elevated border border-theme-border-light cursor-not-allowed',
          )}
        >
          <GitCompare className="w-3.5 h-3.5" />
          Compare
        </button>

        <Tooltip content="Exit compare mode (Esc)">
          <button
            onClick={onExit}
            className="shrink-0 p-1.5 rounded-lg text-theme-text-secondary hover:text-theme-text-primary hover:bg-theme-elevated transition-colors"
            aria-label="Exit compare mode"
          >
            <X className="w-4 h-4" />
          </button>
        </Tooltip>
      </div>
    </div>
  )
}

function PickSlot({
  side,
  pick,
  onRemove,
}: {
  side: CompareSide
  pick?: CompareTrayPick
  onRemove: () => void
}) {
  const tones = SIDE_TONES[side]

  if (!pick) {
    return (
      <div className="group flex items-center gap-2 pl-1.5 pr-2.5 py-1 rounded-lg border border-dashed border-theme-border-light text-xs font-mono min-w-0 max-w-[22rem] text-theme-text-tertiary italic flex-1">
        <span className={clsx('inline-flex items-center justify-center w-4 h-4 rounded text-[10px] font-bold leading-none shrink-0 opacity-50', tones.chipBg)}>
          {side === 'a' ? 'A' : 'B'}
        </span>
        <span className="truncate">Pick {side === 'a' ? 'a resource' : 'a second resource'} from the table…</span>
      </div>
    )
  }

  const full = pick.namespace ? `${pick.namespace}/${pick.name}` : pick.name
  return (
    <div
      className={clsx(
        'group flex items-center gap-2 pl-1.5 pr-1.5 py-1 rounded-lg border text-xs font-mono min-w-0 max-w-[22rem] flex-1',
        tones.containerBorder, tones.containerBg,
      )}
      title={full}
    >
      <span className={clsx('inline-flex items-center justify-center w-4 h-4 rounded text-[10px] font-bold leading-none shrink-0', tones.chipBg)}>
        {side === 'a' ? 'A' : 'B'}
      </span>
      <span className="text-theme-text-primary truncate min-w-0 flex-1">
        {pick.namespace && <span className="opacity-60">{pick.namespace}/</span>}
        {pick.name}
      </span>
      <button
        onClick={onRemove}
        className="shrink-0 p-0.5 rounded text-theme-text-tertiary hover:text-theme-text-primary hover:bg-theme-elevated/70"
        aria-label={`Remove ${side === 'a' ? 'A' : 'B'}`}
      >
        <X className="w-3 h-3" />
      </button>
    </div>
  )
}
