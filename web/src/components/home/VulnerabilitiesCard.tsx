import { useNavigate } from 'react-router-dom'
import { clsx } from 'clsx'
import { ShieldAlert } from 'lucide-react'
import { useAPIResources } from '../../api/apiResources'
import { useRadarFeature } from '../../api/client'
import { hasVulnerabilityReports, useVulnerabilities } from '../../api/vulnerabilities'
import { FindingTotal, findingsCertainty } from '../vulnerabilities/parts'

// The Home dashboard's door to Checks › Vulnerabilities. Present only where a
// scanner writes VulnerabilityReports and this Radar serves the rollup; an
// incomplete answer (reports the caller can't read) says so in the footer
// rather than passing for the cluster total.
export function VulnerabilitiesCard({ namespaces }: { namespaces: string[] }) {
  const navigate = useNavigate()
  const { data: resources } = useAPIResources()
  const { support } = useRadarFeature('vulnerabilities')
  const available = hasVulnerabilityReports(resources) && support !== 'unsupported'
  const { data } = useVulnerabilities({ view: 'image', namespaces }, available)
  if (!available || !data) return null
  const s = data.summary
  if (s.imagesScanned === 0 && s.imagesUnscanned === 0) return null

  const worst = data.images?.[0]
  const certainty = findingsCertainty(data)
  const tone = s.imagesWithCritical > 0 ? 'text-red-500' : s.counts.high > 0 ? 'text-amber-400' : certainty === 'exact' ? 'text-emerald-500' : 'text-theme-text-tertiary'
  const stats = [
    { label: 'images with Critical', value: s.imagesWithCritical, certainty },
    { label: 'Critical/High CVEs with a fix', value: s.fixableCriticalHighCves, certainty },
    { label: 'workloads with Critical or High', value: s.workloadsAffected, certainty },
    { label: 'running images not scanned', value: s.imagesUnscanned, certainty: 'exact' as const },
  ]
  const open = () => {
    const params = new URLSearchParams()
    if (namespaces.length > 0) params.set('namespaces', namespaces.join(','))
    navigate({ pathname: '/checks/vulnerabilities', search: params.toString() })
  }

  return (
    <button
      type="button"
      onClick={open}
      className="group h-[260px] rounded-xl bg-theme-surface shadow-theme-sm hover:-translate-y-1 hover:shadow-theme-md transition-all duration-200 text-left animate-fade-in-up"
    >
      <div className="flex flex-col h-full w-full">
        <div className="flex items-center justify-between px-5 py-3 border-b border-theme-border/50">
          <div className="flex items-center gap-2">
            <ShieldAlert className={clsx('h-4 w-4', tone)} />
            <span className={clsx('text-xs font-semibold uppercase tracking-wider', tone)}>Vulnerabilities</span>
          </div>
          <span className="text-[11px] text-theme-text-tertiary">{s.imagesScanned} running images scanned</span>
        </div>
        <div className="flex-1 min-h-0 px-5 py-3">
          <div className="grid grid-cols-2 gap-x-4 gap-y-3">
            {stats.map((stat) => (
              <div key={stat.label}>
                <FindingTotal plain value={stat.value} certainty={stat.certainty} className="text-lg font-semibold tabular-nums text-theme-text-primary" />
                <div className="text-[11px] text-theme-text-tertiary">{stat.label}</div>
              </div>
            ))}
          </div>
          {worst && worst.counts.critical + worst.counts.high > 0 && (
            <p className="mt-3 truncate text-[11px] text-theme-text-secondary">
              Worst: <span className="font-mono">{worst.image}</span> · {worst.counts.critical} Critical, {worst.counts.high} High
            </p>
          )}
        </div>
        <div className="px-5 py-2.5 border-t border-theme-border/50 text-[11px] text-theme-text-tertiary group-hover:text-theme-text-secondary transition-colors">
          {data.coverage.complete ? 'Open Vulnerabilities →' : 'Some reports aren’t readable with your access · Open Vulnerabilities →'}
        </div>
      </div>
    </button>
  )
}
