import { Info } from 'lucide-react'
import { Tooltip } from '../ui/Tooltip'

export function TrafficDataNote({ dataNote }: { dataNote?: string }) {
  if (!dataNote) return null

  return (
    <Tooltip content={dataNote}>
      <div tabIndex={0} className="flex items-center gap-1 px-2 py-1 rounded-lg bg-theme-surface/90 backdrop-blur border border-theme-border text-[10px] text-theme-text-secondary tabular-nums">
        <Info className="w-3 h-3" /> Data may double-count
      </div>
    </Tooltip>
  )
}
