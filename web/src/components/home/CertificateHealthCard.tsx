import type { DashboardCertificateHealth } from '../../api/client'
import { Shield, ArrowRight } from 'lucide-react'
import { clsx } from 'clsx'

interface CertificateHealthCardProps {
  data: DashboardCertificateHealth
  onNavigate: () => void
}

export function CertificateHealthCard({ data, onNavigate }: CertificateHealthCardProps) {
  const hasIssues = data.expired > 0 || data.critical > 0
  const hasWarnings = data.warning > 0
  const accentColor = hasIssues ? 'text-red-500' : hasWarnings ? 'text-yellow-500' : 'text-green-500'
  const accentBg = hasIssues ? 'bg-red-500/10' : hasWarnings ? 'bg-yellow-500/10' : 'bg-green-500/10'

  return (
    <button
      onClick={onNavigate}
      className="group h-[260px] rounded-xl bg-theme-surface shadow-theme-sm hover:-translate-y-1 hover:shadow-theme-md transition-all duration-200 text-left animate-fade-in-up"
    >
      <div className="flex flex-col h-full w-full">
      <div className="flex items-center justify-between px-5 py-3 border-b border-theme-border/50">
        <div className="flex items-center gap-2">
          <Shield className={clsx('w-4 h-4', accentColor)} />
          <span className={clsx('text-xs font-semibold uppercase tracking-wider', accentColor)}>TLS Certificates</span>
          <span className={clsx('badge-sm', accentBg, accentColor)}>
            {data.total}
          </span>
        </div>
      </div>

      <div className="flex-1 min-h-0 flex flex-col items-center justify-center px-4 py-4">
        {/* Expiry distribution bar */}
        <div className="flex items-center gap-3 w-full">
          {/* Color bar showing distribution */}
          <div className="flex-1 h-3 rounded-full overflow-hidden bg-theme-hover flex">
            {data.healthy > 0 && (
              <div
                className="h-full bg-green-500"
                style={{ width: `${(data.healthy / data.total) * 100}%` }}
              />
            )}
            {data.warning > 0 && (
              <div
                className="h-full bg-yellow-500"
                style={{ width: `${(data.warning / data.total) * 100}%` }}
              />
            )}
            {data.critical > 0 && (
              <div
                className="h-full bg-orange-500"
                style={{ width: `${(data.critical / data.total) * 100}%` }}
              />
            )}
            {data.expired > 0 && (
              <div
                className="h-full bg-red-500"
                style={{ width: `${(data.expired / data.total) * 100}%` }}
              />
            )}
          </div>
        </div>

        {/* Breakdown */}
        <div className="grid grid-cols-2 gap-x-6 gap-y-2 mt-4 w-full">
          <BucketRow label="Healthy" count={data.healthy} color="text-green-400" dotColor="bg-green-500" />
          <BucketRow label="Warning" subtitle="< 30d" count={data.warning} color="text-yellow-400" dotColor="bg-yellow-500" />
          <BucketRow label="Critical" subtitle="< 7d" count={data.critical} color="text-orange-400" dotColor="bg-orange-500" />
          <BucketRow label="Expired" count={data.expired} color="text-red-400" dotColor="bg-red-500" />
        </div>
      </div>

      <div className="px-4 py-1.5 border-t border-theme-border/50 flex items-center justify-end">
        <span className={clsx(
          'flex items-center gap-1.5 text-[10px] font-semibold uppercase tracking-wider transition-colors',
          accentColor,
          hasIssues ? 'group-hover:text-red-400' : hasWarnings ? 'group-hover:text-yellow-400' : 'group-hover:text-green-400'
        )}>
          View Secrets
          <ArrowRight className="w-3.5 h-3.5 transition-transform group-hover:translate-x-0.5" />
        </span>
      </div>
      </div>
    </button>
  )
}

function BucketRow({ label, subtitle, count, color, dotColor }: {
  label: string
  subtitle?: string
  count: number
  color: string
  dotColor: string
}) {
  return (
    <div className="flex items-center gap-2">
      <span className={clsx('w-2 h-2 rounded-full shrink-0', dotColor)} />
      <span className="text-xs text-theme-text-secondary flex-1">
        {label}
        {subtitle && <span className="text-theme-text-tertiary ml-1">{subtitle}</span>}
      </span>
      <span className={clsx('text-sm font-semibold tabular-nums', count > 0 ? color : 'text-theme-text-tertiary')}>{count}</span>
    </div>
  )
}
