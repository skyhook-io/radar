import { useState, useMemo } from 'react'
import { ShieldCheck, ChevronDown, ChevronRight, CheckCircle2, XCircle } from 'lucide-react'
import { clsx } from 'clsx'
import { Section, PropertyList, Property } from '../../ui/drawer-components'
import { formatAge } from '../resource-utils'
import { SEVERITY_BADGE_COLORS, SEVERITY_ORDER, TrivyAlertBanner } from './trivy-shared'

interface ConfigAuditReportRendererProps {
  data: any
}

interface GroupedCheck {
  checkID: string
  title: string
  severity: string
  success: boolean
  description: string
  remediation: string
  messages: string[]
  count: number
}

function groupChecks(checks: any[]): GroupedCheck[] {
  const map = new Map<string, GroupedCheck>()

  for (const check of checks) {
    const id = check.checkID || 'unknown'
    const existing = map.get(id)
    if (existing) {
      existing.count++
      if (!check.success) existing.success = false
      if ((SEVERITY_ORDER[check.severity] ?? 99) < (SEVERITY_ORDER[existing.severity] ?? 99)) {
        existing.severity = check.severity
      }
      if (Array.isArray(check.messages)) {
        for (const msg of check.messages) {
          if (msg && !existing.messages.includes(msg)) {
            existing.messages.push(msg)
          }
        }
      }
    } else {
      map.set(id, {
        checkID: id,
        title: check.title || '-',
        severity: check.severity || 'LOW',
        success: check.success,
        description: check.description || '',
        remediation: check.remediation || '',
        messages: Array.isArray(check.messages) ? [...check.messages] : [],
        count: 1,
      })
    }
  }

  const grouped = Array.from(map.values())
  grouped.sort((a, b) => {
    if (a.success !== b.success) return a.success ? 1 : -1
    return (SEVERITY_ORDER[a.severity] ?? 99) - (SEVERITY_ORDER[b.severity] ?? 99)
  })
  return grouped
}

export function ConfigAuditReportRenderer({ data }: ConfigAuditReportRendererProps) {
  const [expandedSection, setExpandedSection] = useState(true)
  const [expandedChecks, setExpandedChecks] = useState<Set<string>>(new Set())

  const report = data.report || {}
  const summary = report.summary || {}
  const checks = Array.isArray(report.checks) ? report.checks : []
  const scanner = report.scanner || {}

  const critical = summary.criticalCount || 0
  const high = summary.highCount || 0
  const medium = summary.mediumCount || 0
  const low = summary.lowCount || 0

  const grouped = useMemo(() => groupChecks(checks), [checks])
  const passCount = grouped.filter(g => g.success).length
  const failCount = grouped.length - passCount

  const containerCount = useMemo(() => {
    const names = new Set<string>()
    for (const check of checks) {
      if (Array.isArray(check.messages)) {
        for (const msg of check.messages) {
          const match = msg?.match(/Container '([^']+)'/)
          if (match) names.add(match[1])
        }
      }
    }
    return names.size
  }, [checks])

  const toggleCheck = (checkID: string) => {
    setExpandedChecks(prev => {
      const next = new Set(prev)
      if (next.has(checkID)) next.delete(checkID)
      else next.add(checkID)
      return next
    })
  }

  return (
    <>
      <TrivyAlertBanner critical={critical} high={high} noun="finding" />

      {/* Report Overview */}
      <Section title="Report Overview" icon={ShieldCheck}>
        <PropertyList>
          <Property label="Scanner" value={scanner.name ? `${scanner.name} ${scanner.version || ''}`.trim() : '-'} />
          <Property label="Scanned" value={report.updateTimestamp ? formatAge(report.updateTimestamp) + ' ago' : '-'} />
        </PropertyList>
      </Section>

      {/* Summary */}
      <Section title="Summary">
        <div className="flex items-center gap-4 mb-3">
          <div className="flex items-center gap-1.5">
            <CheckCircle2 className="w-4 h-4 text-green-400" />
            <span className="text-sm font-medium text-green-400">{passCount}</span>
            <span className="text-xs text-theme-text-tertiary">passed</span>
          </div>
          <div className="flex items-center gap-1.5">
            <XCircle className="w-4 h-4 text-red-400" />
            <span className="text-sm font-medium text-red-400">{failCount}</span>
            <span className="text-xs text-theme-text-tertiary">failed</span>
          </div>
        </div>
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
          {failCount === 0 && (
            <span className="badge status-healthy">All checks passed</span>
          )}
        </div>
      </Section>

      {/* Checks - grouped by checkID */}
      {grouped.length > 0 && (
        <Section title="Checks">
          <button
            onClick={() => setExpandedSection(!expandedSection)}
            className="flex items-center gap-1 text-xs text-theme-text-secondary hover:text-theme-text-primary mb-2"
          >
            {expandedSection ? <ChevronDown className="w-3.5 h-3.5" /> : <ChevronRight className="w-3.5 h-3.5" />}
            {grouped.length} unique checks{containerCount > 0 ? ` (${containerCount} containers scanned)` : ''}
          </button>
          {expandedSection && (
            <div className="space-y-0.5">
              {grouped.map((group) => {
                const isExpanded = expandedChecks.has(group.checkID)
                return (
                  <div key={group.checkID}>
                    <button
                      onClick={() => toggleCheck(group.checkID)}
                      className="w-full flex items-center gap-2 py-1.5 px-1 rounded hover:bg-theme-hover/50 text-left"
                    >
                      {group.success ? (
                        <CheckCircle2 className="w-3.5 h-3.5 text-green-400 shrink-0" />
                      ) : (
                        <XCircle className="w-3.5 h-3.5 text-red-400 shrink-0" />
                      )}
                      <span className={clsx('px-1.5 py-0.5 rounded text-[10px] font-medium shrink-0', SEVERITY_BADGE_COLORS[group.severity] || 'bg-gray-500/20 text-gray-400')}>
                        {group.severity}
                      </span>
                      <span className="text-xs text-theme-text-secondary truncate flex-1">{group.title}</span>
                      {group.count > 1 && (
                        <span className="text-[10px] text-theme-text-tertiary shrink-0">{group.count} containers</span>
                      )}
                      {isExpanded ? (
                        <ChevronDown className="w-3 h-3 text-theme-text-tertiary shrink-0" />
                      ) : (
                        <ChevronRight className="w-3 h-3 text-theme-text-tertiary shrink-0" />
                      )}
                    </button>
                    {isExpanded && (
                      <div className="ml-6 mr-1 mb-2 p-2.5 bg-theme-elevated/50 rounded border border-theme-border/50 space-y-2">
                        <div className="text-[10px] font-mono text-theme-text-tertiary">{group.checkID}</div>
                        {group.description && (
                          <div>
                            <div className="text-[10px] font-medium text-theme-text-tertiary uppercase tracking-wider mb-0.5">Description</div>
                            <div className="text-xs text-theme-text-secondary">{group.description}</div>
                          </div>
                        )}
                        {group.messages.length > 0 && (
                          <div>
                            <div className="text-[10px] font-medium text-theme-text-tertiary uppercase tracking-wider mb-0.5">Affected</div>
                            <div className="space-y-0.5">
                              {group.messages.map((msg, i) => (
                                <div key={i} className="text-xs text-theme-text-secondary">{msg}</div>
                              ))}
                            </div>
                          </div>
                        )}
                        {group.remediation && (
                          <div>
                            <div className="text-[10px] font-medium text-theme-text-tertiary uppercase tracking-wider mb-0.5">Remediation</div>
                            <div className="text-xs text-theme-text-secondary">{group.remediation}</div>
                          </div>
                        )}
                      </div>
                    )}
                  </div>
                )
              })}
            </div>
          )}
        </Section>
      )}
    </>
  )
}
