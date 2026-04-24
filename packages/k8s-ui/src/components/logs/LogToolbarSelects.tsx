import { ChevronDown } from 'lucide-react'
import { Tooltip } from '../ui/Tooltip'
import { getLogPalette, type LogPalette } from './log-palette'

// See log-palette.ts for why the viewer uses explicit palette classes
// instead of theme-* tokens.
function selectClass(palette: LogPalette): string {
  return `appearance-none ${palette.elevatedBg} ${palette.textPrimary} text-xs rounded px-2 py-1.5 border ${palette.borderLight} focus:outline-none focus:ring-1 focus:ring-blue-500`
}

// ── ContainerSelect ───────────────────────────────────────────────────────────

interface ContainerSelectProps {
  containers: string[]
  value: string
  onChange: (value: string) => void
  /** If true, prepend an "All containers" option with value="" */
  includeAll?: boolean
  /** Dark/light palette selector. Defaults to `true` (dark viewer). */
  isDark?: boolean
}

export function ContainerSelect({ containers, value, onChange, includeAll = false, isDark = true }: ContainerSelectProps) {
  if (containers.length <= 1 && !includeAll) return null
  const palette = getLogPalette(isDark)
  return (
    <div className="relative">
      <select
        value={value}
        onChange={(e) => onChange(e.target.value)}
        className={`${selectClass(palette)} pr-6`}
      >
        {includeAll && <option value="">All containers</option>}
        {containers.map(c => <option key={c} value={c}>{c}</option>)}
      </select>
      <ChevronDown className={`absolute right-1.5 top-1/2 -translate-y-1/2 w-3 h-3 ${palette.textSecondary} pointer-events-none`} />
    </div>
  )
}

// ── LogRangeSelect ────────────────────────────────────────────────────────────

interface LogRangeSelectProps {
  value: string
  onChange: (value: string) => void
  /** Line count options to show. Defaults to [100, 500, 1000, 5000]. */
  lineOptions?: number[]
  tooltip?: string
  /** Dark/light palette selector. Defaults to `true` (dark viewer). */
  isDark?: boolean
}

export function LogRangeSelect({
  value,
  onChange,
  lineOptions = [100, 500, 1000, 5000],
  tooltip = 'How many logs to load — by line count or time range',
  isDark = true,
}: LogRangeSelectProps) {
  const palette = getLogPalette(isDark)
  return (
    <Tooltip content={tooltip} position="bottom">
      <select
        value={value}
        onChange={(e) => onChange(e.target.value)}
        className={`${selectClass(palette)} pr-5`}
      >
        <optgroup label="Lines">
          {lineOptions.map(n => (
            <option key={n} value={String(n)}>
              {n.toLocaleString()} lines
            </option>
          ))}
        </optgroup>
        <optgroup label="Time">
          <option value="since:60">Last 1 min</option>
          <option value="since:300">Last 5 min</option>
          <option value="since:900">Last 15 min</option>
          <option value="since:1800">Last 30 min</option>
          <option value="since:3600">Last 1 hour</option>
        </optgroup>
      </select>
    </Tooltip>
  )
}
