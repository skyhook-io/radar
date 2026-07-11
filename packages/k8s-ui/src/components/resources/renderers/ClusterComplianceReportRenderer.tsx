import { useState, useMemo } from 'react'
import { ShieldCheck, ChevronDown, ChevronRight, CheckCircle2, XCircle, Search } from 'lucide-react'
import { clsx } from 'clsx'
import { Section, PropertyList, Property, AlertBanner } from '../../ui/drawer-components'
import { Input } from '../../ui/Input'
import { formatAge } from '../resource-utils'
import { SEVERITY_BADGE_COLORS, SEVERITY_ORDER } from './trivy-shared'
import { pluralize } from '../../../utils/pluralize'
import { BADGE_INACTIVE } from '../../../utils/badge-colors'

interface ClusterComplianceReportRendererProps {
  data: any
}

export function ClusterComplianceReportRenderer({ data }: ClusterComplianceReportRendererProps) {
  const [expandedSection, setExpandedSection] = useState(true)
  const [expandedControls, setExpandedControls] = useState<Set<string>>(new Set())
  const [searchTerm, setSearchTerm] = useState('')
  const [showFailedOnly, setShowFailedOnly] = useState(false)

  const compliance = data.spec?.compliance || {}
  const status = data.status || {}
  const summaryReport = status.summaryReport || {}
  const controlChecks = Array.isArray(summaryReport.controlCheck) ? summaryReport.controlCheck : []
  const controls = Array.isArray(compliance.controls) ? compliance.controls : []

  const passCount = status.summary?.passCount || 0
  const failCount = status.summary?.failCount || 0
  const total = passCount + failCount

  // Build a map of control definitions for descriptions
  const controlMap = useMemo(() => {
    const map = new Map<string, any>()
    for (const ctrl of controls) {
      if (ctrl.id) map.set(ctrl.id, ctrl)
    }
    return map
  }, [controls])

  // Sort: failed first by severity, then passed
  const sortedChecks = useMemo(() => [...controlChecks].sort((a: any, b: any) => {
    const aFail = (a.totalFail || 0) > 0
    const bFail = (b.totalFail || 0) > 0
    if (aFail !== bFail) return aFail ? -1 : 1
    return (SEVERITY_ORDER[a.severity] ?? 99) - (SEVERITY_ORDER[b.severity] ?? 99)
  }), [controlChecks])

  const filteredChecks = useMemo(() => {
    let result = sortedChecks
    if (showFailedOnly) {
      result = result.filter((c: any) => (c.totalFail || 0) > 0)
    }
    if (searchTerm) {
      const term = searchTerm.toLowerCase()
      result = result.filter((c: any) => {
        const def = controlMap.get(c.id)
        return (c.name || '').toLowerCase().includes(term) ||
          (c.id || '').toLowerCase().includes(term) ||
          (def?.description || '').toLowerCase().includes(term)
      })
    }
    return result
  }, [sortedChecks, searchTerm, showFailedOnly, controlMap])

  const hasActiveFilter = searchTerm !== '' || showFailedOnly

  const toggleControl = (id: string) => {
    setExpandedControls(prev => {
      const next = new Set(prev)
      if (next.has(id)) next.delete(id)
      else next.add(id)
      return next
    })
  }

  return (
    <>
      {/* Alert banner for failures */}
      {failCount > 0 && (
        <AlertBanner
          variant="error"
          icon={XCircle}
          title={`${pluralize(failCount, 'control')} failed`}
          message="Review failed controls to improve cluster compliance."
        />
      )}

      {/* Framework Overview */}
      <Section title="Compliance Framework" icon={ShieldCheck}>
        <PropertyList>
          <Property label="Framework" value={compliance.title || compliance.id || '-'} />
          {compliance.description && <Property label="Description" value={compliance.description} />}
          {compliance.version && <Property label="Version" value={compliance.version} />}
          {compliance.platform && <Property label="Platform" value={compliance.platform} />}
          <Property label="Last Updated" value={status.updateTimestamp ? formatAge(status.updateTimestamp) + ' ago' : '-'} />
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
          {total > 0 && (
            <div className="ml-auto text-sm font-medium text-theme-text-secondary">
              {Math.round((passCount / total) * 100)}% compliant
            </div>
          )}
        </div>
        {total > 0 && (
          <div className="h-3 rounded overflow-hidden flex">
            {passCount > 0 && <div className="h-full bg-green-500" style={{ width: `${(passCount / total) * 100}%` }} />}
            {failCount > 0 && <div className="h-full bg-red-500" style={{ width: `${(failCount / total) * 100}%` }} />}
          </div>
        )}
      </Section>

      {/* Controls */}
      {sortedChecks.length > 0 && (
        <Section title="Controls">
          <button
            onClick={() => setExpandedSection(!expandedSection)}
            className="flex items-center gap-1 text-xs text-theme-text-secondary hover:text-theme-text-primary mb-2"
          >
            {expandedSection ? <ChevronDown className="w-3.5 h-3.5" /> : <ChevronRight className="w-3.5 h-3.5" />}
            {sortedChecks.length} controls
          </button>
          {expandedSection && (
            <>
            {/* Search and filter */}
            <div className="flex items-center gap-2 mb-2">
              <div className="relative flex-1">
                <Search className="absolute left-2 top-1/2 -translate-y-1/2 w-3 h-3 text-theme-text-tertiary" />
                <Input
                  placeholder="Filter by control name or ID..."
                  value={searchTerm}
                  onChange={(e) => setSearchTerm(e.target.value)}
                  className="w-full pl-6 pr-2 py-1 text-xs bg-theme-bg border border-theme-border rounded text-theme-text-primary placeholder:text-theme-text-tertiary focus:outline-none focus:ring-1 focus:ring-blue-500/50"
                />
              </div>
              <button
                onClick={() => setShowFailedOnly(!showFailedOnly)}
                className={clsx(
                  'flex items-center gap-1 px-2 py-1 text-xs rounded border transition-colors whitespace-nowrap',
                  showFailedOnly
                    ? 'bg-red-500/15 border-red-500/30 text-red-400'
                    : 'bg-theme-bg border-theme-border text-theme-text-tertiary hover:text-theme-text-secondary'
                )}
              >
                <XCircle className="w-3 h-3" />
                Failed
              </button>
            </div>
            {hasActiveFilter && (
              <div className="flex items-center gap-1 mb-2 text-xs text-theme-text-tertiary">
                Showing {filteredChecks.length} of {sortedChecks.length}
                <button
                  onClick={() => { setSearchTerm(''); setShowFailedOnly(false) }}
                  className="ml-1 text-blue-400 hover:text-blue-300 hover:underline"
                >
                  Clear
                </button>
              </div>
            )}
            <div className="space-y-0.5">
              {filteredChecks.map((check: any) => {
                const hasFail = (check.totalFail || 0) > 0
                const controlDef = controlMap.get(check.id)
                const isExpanded = expandedControls.has(check.id)
                return (
                  <div key={check.id}>
                    <button
                      onClick={() => toggleControl(check.id)}
                      className="w-full flex items-center gap-2 py-1.5 px-1 rounded hover:bg-theme-hover/50 text-left"
                    >
                      {hasFail ? (
                        <XCircle className="w-3.5 h-3.5 text-red-400 shrink-0" />
                      ) : (
                        <CheckCircle2 className="w-3.5 h-3.5 text-green-400 shrink-0" />
                      )}
                      <span className={clsx('px-1.5 py-0.5 rounded text-[10px] font-medium shrink-0', SEVERITY_BADGE_COLORS[check.severity] || BADGE_INACTIVE)}>
                        {check.severity || '-'}
                      </span>
                      <span className="text-xs text-theme-text-secondary truncate flex-1">{check.name || check.id}</span>
                      <span className="text-[10px] text-theme-text-tertiary shrink-0">
                        {check.totalFail || 0}F / {check.totalPass || 0}P
                      </span>
                      {isExpanded ? (
                        <ChevronDown className="w-3 h-3 text-theme-text-tertiary shrink-0" />
                      ) : (
                        <ChevronRight className="w-3 h-3 text-theme-text-tertiary shrink-0" />
                      )}
                    </button>
                    {isExpanded && controlDef && (
                      <div className="ml-6 mr-1 mb-2 p-2.5 bg-theme-elevated/50 rounded border border-theme-border/50 space-y-2">
                        <div className="text-[10px] font-mono text-theme-text-tertiary">{check.id}</div>
                        {controlDef.description && (
                          <div>
                            <div className="text-[10px] font-medium text-theme-text-tertiary uppercase tracking-wider mb-0.5">Description</div>
                            <div className="text-xs text-theme-text-secondary">{controlDef.description}</div>
                          </div>
                        )}
                        {controlDef.checks?.length > 0 && (
                          <div>
                            <div className="text-[10px] font-medium text-theme-text-tertiary uppercase tracking-wider mb-0.5">Check IDs</div>
                            <div className="flex flex-wrap gap-1">
                              {controlDef.checks.map((c: any, i: number) => (
                                <span key={i} className="text-[10px] font-mono text-theme-text-tertiary bg-theme-surface px-1.5 py-0.5 rounded">{c.id}</span>
                              ))}
                            </div>
                          </div>
                        )}
                      </div>
                    )}
                  </div>
                )
              })}
              {filteredChecks.length === 0 && (
                <div className="py-4 text-center text-xs text-theme-text-tertiary">
                  No controls match the current filter.
                </div>
              )}
            </div>
            </>
          )}
        </Section>
      )}
    </>
  )
}
