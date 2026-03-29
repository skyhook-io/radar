import { Globe, ArrowRight, Network } from 'lucide-react'
import { clsx } from 'clsx'
import { Section, PropertyList, Property, ConditionsSection, AlertBanner, ResourceRefBadge } from '../../ui/drawer-components'
import type { ResourceRef } from '../../../types'

interface SimpleRouteRendererProps {
  data: any
  kind: 'TCPRoute' | 'TLSRoute'
  onNavigate?: (ref: ResourceRef) => void
}

export function SimpleRouteRenderer({ data, kind, onNavigate }: SimpleRouteRendererProps) {
  const spec = data.spec || {}
  const status = data.status || {}
  const parentRefs = spec.parentRefs || []
  const hostnames = spec.hostnames || []
  const rules = spec.rules || []
  const parentStatuses = status.parents || []
  const routeNs = data.metadata?.namespace || ''

  const isTLS = kind === 'TLSRoute'

  const notAcceptedParents = parentStatuses.filter((p: any) =>
    (p.conditions || []).some((c: any) => c.type === 'Accepted' && c.status === 'False')
  )
  const unresolvedRefsParents = parentStatuses.filter((p: any) =>
    (p.conditions || []).some((c: any) => c.type === 'ResolvedRefs' && c.status === 'False')
  )

  const firstParentConditions = parentStatuses.length > 0
    ? parentStatuses[0].conditions
    : undefined

  function toGatewayRef(ref: any): ResourceRef {
    return {
      kind: 'Gateway',
      namespace: ref.namespace || routeNs,
      name: ref.name,
      group: 'gateway.networking.k8s.io',
    }
  }

  function toServiceRef(backend: any): ResourceRef {
    return {
      kind: 'Service',
      namespace: backend.namespace || routeNs,
      name: backend.name,
    }
  }

  return (
    <>
      {notAcceptedParents.length > 0 && (
        <AlertBanner
          variant="error"
          title="Route Not Accepted"
          message={notAcceptedParents.map((p: any) => {
            const cond = (p.conditions || []).find((c: any) => c.type === 'Accepted' && c.status === 'False')
            const gwName = p.parentRef?.name || 'unknown'
            return cond?.reason
              ? `Gateway "${gwName}": ${cond.reason}${cond.message ? ' — ' + cond.message : ''}`
              : `Gateway "${gwName}" has not accepted this route.`
          }).join('; ')}
        />
      )}

      {unresolvedRefsParents.length > 0 && (
        <AlertBanner
          variant="warning"
          title="Unresolved References"
          message="Some backend references could not be resolved. Check that the target services exist and are accessible."
        />
      )}

      <Section title="Status" icon={Globe}>
        <PropertyList>
          {isTLS && (
            <Property
              label="SNI Hostnames"
              value={
                hostnames.length > 0 ? (
                  <div className="flex flex-wrap gap-1">
                    {hostnames.map((h: string) => (
                      <span key={h} className="badge bg-theme-elevated text-theme-text-secondary">{h}</span>
                    ))}
                  </div>
                ) : (
                  <span className="text-xs text-theme-text-tertiary">Any</span>
                )
              }
            />
          )}
          <Property
            label="Parent Gateways"
            value={
              parentRefs.length > 0 ? (
                <div className="flex flex-wrap gap-1">
                  {parentRefs.map((ref: any, i: number) => (
                    <ResourceRefBadge key={`${ref.namespace || ''}-${ref.name}-${i}`} resourceRef={toGatewayRef(ref)} onClick={onNavigate} />
                  ))}
                </div>
              ) : 'None'
            }
          />
        </PropertyList>
      </Section>

      <Section title={`Rules (${rules.length})`} icon={Network} defaultExpanded>
        <div className="space-y-1.5">
          {rules.map((rule: any, ruleIdx: number) => {
            const backendRefs = rule.backendRefs || []
            const hasWeights = backendRefs.length > 1 && backendRefs.some((b: any) => b.weight !== undefined)
            const totalWeight = hasWeights ? backendRefs.reduce((sum: number, b: any) => sum + (b.weight ?? 1), 0) : 0

            return (
              <div key={ruleIdx} className="card-inner">
                {rules.length > 1 && (
                  <div className="text-xs font-medium text-theme-text-tertiary mb-1.5">
                    Rule {ruleIdx + 1}
                  </div>
                )}

                {backendRefs.length > 0 ? (
                  <div className="text-xs text-theme-text-secondary flex flex-wrap items-center gap-1.5">
                    <ArrowRight className="w-3 h-3 text-theme-text-tertiary shrink-0" />
                    {backendRefs.map((b: any, bi: number) => {
                      const pct = hasWeights ? Math.round(((b.weight ?? 1) / totalWeight) * 100) : null
                      return (
                        <span key={bi} className="flex items-center gap-1">
                          <ResourceRefBadge resourceRef={toServiceRef(b)} onClick={onNavigate} />
                          {b.port && <span className="text-theme-text-tertiary">:{b.port}</span>}
                          {pct !== null && (
                            <span className="text-theme-text-tertiary text-[10px]">{pct}%</span>
                          )}
                        </span>
                      )
                    })}
                  </div>
                ) : (
                  <div className="text-xs text-theme-text-tertiary italic">No backends</div>
                )}
              </div>
            )
          })}
        </div>
      </Section>

      {/* Parent Status */}
      {parentStatuses.length > 0 && (
        <Section title={`Parent Status (${parentStatuses.length})`} defaultExpanded>
          <div className="space-y-2">
            {parentStatuses.map((parent: any, idx: number) => {
              const ref = parent.parentRef || {}
              const conditions = parent.conditions || []
              const accepted = conditions.find((c: any) => c.type === 'Accepted')
              const resolved = conditions.find((c: any) => c.type === 'ResolvedRefs')

              return (
                <div key={`${ref.namespace || ''}-${ref.name}-${idx}`} className="card-inner">
                  <div className="text-sm font-medium text-theme-text-primary mb-1.5">
                    {ref.namespace ? `${ref.namespace}/` : ''}{ref.name || 'unknown'}
                    {ref.sectionName ? ` (${ref.sectionName})` : ''}
                  </div>
                  <div className="flex flex-wrap gap-2">
                    {accepted && (
                      <span className={clsx(
                        'badge',
                        accepted.status === 'True' ? 'bg-green-500/20 text-green-400' : 'bg-red-500/20 text-red-400'
                      )}>
                        {accepted.status === 'True' ? 'Accepted' : 'Not Accepted'}
                      </span>
                    )}
                    {resolved && (
                      <span className={clsx(
                        'badge',
                        resolved.status === 'True' ? 'bg-green-500/20 text-green-400' : 'bg-yellow-500/20 text-yellow-400'
                      )}>
                        {resolved.status === 'True' ? 'Refs Resolved' : 'Unresolved Refs'}
                      </span>
                    )}
                  </div>
                  {accepted?.message && accepted.status === 'False' && (
                    <div className="text-xs text-theme-text-tertiary mt-1">{accepted.message}</div>
                  )}
                </div>
              )
            })}
          </div>
        </Section>
      )}

      <ConditionsSection conditions={firstParentConditions} />
    </>
  )
}
