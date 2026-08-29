import { clsx } from 'clsx'
import { HelpCircle } from 'lucide-react'
import { Tooltip } from '../ui/Tooltip'
import { formatCostPerHour, formatProjectedMonthlyRate } from './format'
import { costRateLabels } from './source'

interface CurrentAllocationUseProps {
  currency: string
  dataAvailable: boolean
  cpuCost: number
  memoryCost: number
  hourlyCost: number
  cpuAllocationUse: number
  memoryAllocationUse: number
  cpuUsageAvailable: boolean
  memoryUsageAvailable: boolean
  scopeNote?: string
  window?: string
}

export const ALLOCATION_USE_TOOLTIP =
  'Cost allocation uses the greater of requested or observed CPU and memory. The percentages compare observed use with that allocated amount; they are not request headroom or a right-sizing recommendation.'

export function formatAllocatedUse(
  allocationCost: number,
  allocationUse: number,
  usageAvailable: boolean,
  dataAvailable = true,
): string {
  if (!dataAvailable) return '—'
  if (allocationCost <= 0) return 'No allocation'
  if (!usageAvailable) return 'Usage unavailable'
  const percentage = Math.min(100, Math.max(0, Math.round(allocationUse)))
  return `${percentage}% allocated use`
}

export function CurrentAllocationUse({
  currency,
  dataAvailable,
  cpuCost,
  memoryCost,
  hourlyCost,
  cpuAllocationUse,
  memoryAllocationUse,
  cpuUsageAvailable,
  memoryUsageAvailable,
  scopeNote,
  window,
}: CurrentAllocationUseProps) {
  const splitTotal = cpuCost + memoryCost
  const cpuPct = splitTotal > 0 ? (cpuCost / splitTotal) * 100 : 0
  const memoryPct = splitTotal > 0 ? (memoryCost / splitTotal) * 100 : 0
  const labels = costRateLabels(window)

  return (
    <section className="rounded-lg border border-theme-border bg-theme-surface/50 p-4">
      <div className="mb-3 flex items-center justify-between gap-3">
        <div>
          <div className="flex items-center gap-1.5">
            <div className="text-sm font-semibold text-theme-text-primary">{labels.allocationTitle}</div>
            <Tooltip content={ALLOCATION_USE_TOOLTIP} className="max-w-[320px] whitespace-normal text-left" delay={150}>
              <HelpCircle className="h-3.5 w-3.5 cursor-help text-theme-text-tertiary transition-colors hover:text-theme-text-secondary" />
            </Tooltip>
          </div>
          <div className="text-xs text-theme-text-tertiary">
            {labels.allocationPeriod}
            {scopeNote ? ` · ${scopeNote}` : ''}
          </div>
        </div>
        <div className="text-right">
          <div className="text-sm font-medium text-theme-text-primary tabular-nums">
            {dataAvailable ? formatProjectedMonthlyRate(hourlyCost, currency) : '—'}
          </div>
          {dataAvailable && (
            <div className="text-[10px] text-theme-text-tertiary tabular-nums">
              {formatCostPerHour(hourlyCost, currency)} · {labels.rate}
            </div>
          )}
        </div>
      </div>
      <div className="h-3 overflow-hidden rounded-full bg-theme-hover">
        <div className="flex h-full">
          {dataAvailable && (
            <>
              <div className="h-full bg-accent" style={{ width: `${cpuPct}%` }} />
              <div className="h-full bg-amber-500" style={{ width: `${memoryPct}%` }} />
            </>
          )}
        </div>
      </div>
      <div className="mt-3 grid gap-2 text-xs text-theme-text-secondary sm:grid-cols-2">
        <AllocationUseItem
          colorClass="bg-accent"
          label="CPU"
          allocation={dataAvailable ? formatProjectedMonthlyRate(cpuCost, currency) : '—'}
          use={formatAllocatedUse(cpuCost, cpuAllocationUse, cpuUsageAvailable, dataAvailable)}
        />
        <AllocationUseItem
          colorClass="bg-amber-500"
          label="Memory"
          allocation={dataAvailable ? formatProjectedMonthlyRate(memoryCost, currency) : '—'}
          use={formatAllocatedUse(memoryCost, memoryAllocationUse, memoryUsageAvailable, dataAvailable)}
        />
      </div>
    </section>
  )
}

function AllocationUseItem({
  colorClass,
  label,
  allocation,
  use,
}: {
  colorClass: string
  label: string
  allocation: string
  use: string
}) {
  return (
    <div className="flex items-center justify-between gap-3 rounded-md bg-theme-base px-2.5 py-2">
      <span className="flex items-center gap-2">
        <span className={clsx('h-2.5 w-2.5 rounded-sm', colorClass)} />
        {label}
      </span>
      <span className="text-right">
        <span className="block font-medium tabular-nums text-theme-text-primary">{allocation}</span>
        <span className="block text-[10px] tabular-nums text-theme-text-tertiary">{use}</span>
      </span>
    </div>
  )
}
