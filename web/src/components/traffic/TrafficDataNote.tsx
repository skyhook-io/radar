import { useId } from 'react'
import { Info } from 'lucide-react'
import { Tooltip } from '../ui/Tooltip'

export function TrafficDataNote({ dataNote }: { dataNote?: string }) {
  const descriptionId = useId()
  if (!dataNote) return null

  return (
    <Tooltip content={dataNote}>
      <div tabIndex={0} aria-describedby={descriptionId} className="flex items-center gap-1 px-2 py-1 rounded-lg bg-theme-surface/90 backdrop-blur border border-theme-border text-[10px] text-theme-text-secondary tabular-nums">
        <Info className="w-3 h-3" aria-hidden="true" /> Data may double-count
        <span id={descriptionId} className="sr-only">{dataNote}</span>
      </div>
    </Tooltip>
  )
}
