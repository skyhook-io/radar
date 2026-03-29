import { useState } from 'react'
import { KeyRound, ChevronDown, ChevronRight } from 'lucide-react'
import { clsx } from 'clsx'
import { Section, PropertyList, Property } from '../../ui/drawer-components'
import { formatAge } from '../resource-utils'
import { SEVERITY_BADGE_COLORS, TrivyAlertBanner, formatTrivyImage } from './trivy-shared'

interface ExposedSecretReportRendererProps {
  data: any
}

const INITIAL_SHOW_COUNT = 50

export function ExposedSecretReportRenderer({ data }: ExposedSecretReportRendererProps) {
  const [showAll, setShowAll] = useState(false)
  const [expanded, setExpanded] = useState(true)

  const report = data.report || {}
  const summary = report.summary || {}
  const secrets = Array.isArray(report.secrets) ? report.secrets : []
  const scanner = report.scanner || {}
  const labels = data.metadata?.labels || {}

  const critical = summary.criticalCount || 0
  const high = summary.highCount || 0
  const medium = summary.mediumCount || 0
  const low = summary.lowCount || 0

  const containerName = labels['trivy-operator.container.name'] || '-'
  const image = formatTrivyImage(report)

  const displayedSecrets = showAll ? secrets : secrets.slice(0, INITIAL_SHOW_COUNT)

  return (
    <>
      <TrivyAlertBanner critical={critical} high={high} noun="exposed secret" />

      {/* Report Overview */}
      <Section title="Report Overview" icon={KeyRound}>
        <PropertyList>
          <Property label="Container" value={containerName} />
          <Property label="Image" value={image} />
          <Property label="Scanner" value={scanner.name ? `${scanner.name} ${scanner.version || ''}`.trim() : '-'} />
          <Property label="Scanned" value={report.updateTimestamp ? formatAge(report.updateTimestamp) + ' ago' : '-'} />
        </PropertyList>
      </Section>

      {/* Severity Summary */}
      <Section title="Summary">
        <div className="flex flex-wrap gap-2">
          {[
            { label: 'Critical', count: critical, key: 'CRITICAL' },
            { label: 'High', count: high, key: 'HIGH' },
            { label: 'Medium', count: medium, key: 'MEDIUM' },
            { label: 'Low', count: low, key: 'LOW' },
          ].filter(({ count }) => count > 0).map(({ label, count, key }) => (
            <span key={key} className={clsx('badge', SEVERITY_BADGE_COLORS[key])}>
              {count} {label}
            </span>
          ))}
          {secrets.length === 0 && (
            <span className="badge status-healthy">No secrets found</span>
          )}
        </div>
      </Section>

      {/* Secrets Table */}
      {secrets.length > 0 && (
        <Section title="Exposed Secrets">
          <button
            onClick={() => setExpanded(!expanded)}
            className="flex items-center gap-1 text-xs text-theme-text-secondary hover:text-theme-text-primary mb-2"
          >
            {expanded ? <ChevronDown className="w-3.5 h-3.5" /> : <ChevronRight className="w-3.5 h-3.5" />}
            {secrets.length} secret{secrets.length !== 1 ? 's' : ''}
          </button>
          {expanded && (
            <div className="overflow-x-auto -mx-1">
              <table className="w-full text-xs">
                <thead>
                  <tr className="border-b border-theme-border text-theme-text-tertiary">
                    <th className="text-left py-1.5 px-1 font-medium">Rule</th>
                    <th className="text-left py-1.5 px-1 font-medium">Severity</th>
                    <th className="text-left py-1.5 px-1 font-medium">Category</th>
                    <th className="text-left py-1.5 px-1 font-medium">Target</th>
                    <th className="text-left py-1.5 px-1 font-medium">Match</th>
                  </tr>
                </thead>
                <tbody>
                  {displayedSecrets.map((secret: any, i: number) => (
                    <tr key={`${secret.ruleID || i}-${i}`} className="border-b border-theme-border/50 hover:bg-theme-hover/50">
                      <td className="py-1.5 px-1 text-theme-text-secondary font-mono">{secret.ruleID || '-'}</td>
                      <td className="py-1.5 px-1">
                        <span className={clsx('px-1.5 py-0.5 rounded text-[10px] font-medium', SEVERITY_BADGE_COLORS[secret.severity] || 'bg-gray-500/20 text-gray-400')}>
                          {secret.severity || '-'}
                        </span>
                      </td>
                      <td className="py-1.5 px-1 text-theme-text-secondary">{secret.category || secret.title || '-'}</td>
                      <td className="py-1.5 px-1 text-theme-text-secondary max-w-[180px] truncate font-mono" title={secret.target}>{secret.target || '-'}</td>
                      <td className="py-1.5 px-1 text-theme-text-tertiary max-w-[150px] truncate font-mono" title={secret.match}>{secret.match || '-'}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
              {!showAll && secrets.length > INITIAL_SHOW_COUNT && (
                <button
                  onClick={() => setShowAll(true)}
                  className="mt-2 text-xs text-blue-400 hover:text-blue-300 hover:underline"
                >
                  Show all {secrets.length} secrets
                </button>
              )}
            </div>
          )}
        </Section>
      )}
    </>
  )
}
