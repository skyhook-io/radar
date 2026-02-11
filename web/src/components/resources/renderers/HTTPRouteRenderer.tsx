import { Globe, AlertTriangle, ArrowRight, Network, Filter } from 'lucide-react'
import { clsx } from 'clsx'
import { Section, PropertyList, Property, ConditionsSection } from '../drawer-components'

interface HTTPRouteRendererProps {
  data: any
}

export function HTTPRouteRenderer({ data }: HTTPRouteRendererProps) {
  const spec = data.spec || {}
  const status = data.status || {}
  const parentRefs = spec.parentRefs || []
  const hostnames = spec.hostnames || []
  const rules = spec.rules || []
  const parentStatuses = status.parents || []

  // Problem detection
  const notAcceptedParents = parentStatuses.filter((p: any) =>
    (p.conditions || []).some((c: any) => c.type === 'Accepted' && c.status === 'False')
  )
  const unresolvedRefsParents = parentStatuses.filter((p: any) =>
    (p.conditions || []).some((c: any) => c.type === 'ResolvedRefs' && c.status === 'False')
  )

  // Get conditions from the first parent for the shared ConditionsSection
  const firstParentConditions = parentStatuses.length > 0
    ? parentStatuses[0].conditions
    : undefined

  return (
    <>
      {/* Accepted=False alert */}
      {notAcceptedParents.length > 0 && (
        <div className="mb-4 p-3 bg-red-500/10 border border-red-500/30 rounded-lg">
          <div className="flex items-start gap-2">
            <AlertTriangle className="w-4 h-4 text-red-400 mt-0.5 shrink-0" />
            <div className="flex-1 min-w-0">
              <div className="text-sm font-medium text-red-400">Route Not Accepted</div>
              <div className="text-xs text-red-300/80 mt-1">
                {notAcceptedParents.map((p: any) => {
                  const cond = (p.conditions || []).find((c: any) => c.type === 'Accepted' && c.status === 'False')
                  const gwName = p.parentRef?.name || 'unknown'
                  return cond?.reason
                    ? `Gateway "${gwName}": ${cond.reason}${cond.message ? ' — ' + cond.message : ''}`
                    : `Gateway "${gwName}" has not accepted this route.`
                }).join('; ')}
              </div>
            </div>
          </div>
        </div>
      )}

      {/* ResolvedRefs=False alert */}
      {unresolvedRefsParents.length > 0 && (
        <div className="mb-4 p-3 bg-yellow-500/10 border border-yellow-500/30 rounded-lg">
          <div className="flex items-start gap-2">
            <AlertTriangle className="w-4 h-4 text-yellow-400 mt-0.5 shrink-0" />
            <div className="flex-1 min-w-0">
              <div className="text-sm font-medium text-yellow-400">Unresolved References</div>
              <div className="text-xs text-yellow-300/80 mt-1">
                Some backend references could not be resolved. Check that the target services exist and are accessible.
              </div>
            </div>
          </div>
        </div>
      )}

      {/* Status section */}
      <Section title="Status" icon={Globe}>
        <PropertyList>
          <Property
            label="Hostnames"
            value={
              hostnames.length > 0 ? (
                <div className="flex flex-wrap gap-1">
                  {hostnames.map((h: string) => (
                    <span
                      key={h}
                      className="px-2 py-0.5 bg-theme-elevated rounded text-xs text-theme-text-secondary"
                    >
                      {h}
                    </span>
                  ))}
                </div>
              ) : (
                <span className="text-xs text-theme-text-tertiary">Any</span>
              )
            }
          />
          <Property
            label="Parent Gateways"
            value={
              parentRefs.length > 0 ? (
                <div className="flex flex-wrap gap-1">
                  {parentRefs.map((ref: any, i: number) => (
                    <span
                      key={`${ref.namespace || ''}-${ref.name}-${i}`}
                      className="px-2 py-0.5 bg-violet-500/20 text-violet-400 border border-violet-500/30 rounded text-xs"
                    >
                      {ref.namespace ? `${ref.namespace}/` : ''}{ref.name}
                      {ref.sectionName ? ` (${ref.sectionName})` : ''}
                    </span>
                  ))}
                </div>
              ) : 'None'
            }
          />
        </PropertyList>
      </Section>

      {/* Rules section */}
      <Section title={`Rules (${rules.length})`} icon={Network} defaultExpanded>
        <div className="space-y-3">
          {rules.map((rule: any, ruleIdx: number) => {
            const matches = rule.matches || []
            const backendRefs = rule.backendRefs || []
            const filters = rule.filters || []

            return (
              <div key={ruleIdx} className="bg-theme-elevated/30 rounded p-3">
                <div className="text-xs font-medium text-theme-text-tertiary mb-2">
                  Rule {ruleIdx + 1}
                </div>

                {/* Matches */}
                <div className="mb-2">
                  <div className="text-xs text-theme-text-tertiary mb-1">Matches</div>
                  {matches.length === 0 ? (
                    <div className="text-xs text-theme-text-secondary italic">Match all</div>
                  ) : (
                    <div className="space-y-1">
                      {matches.map((match: any, matchIdx: number) => (
                        <div key={matchIdx} className="text-xs text-theme-text-secondary flex flex-wrap items-center gap-1.5">
                          {match.method && (
                            <span className="px-1.5 py-0.5 bg-blue-500/20 text-blue-400 rounded font-medium">
                              {match.method}
                            </span>
                          )}
                          {match.path && (
                            <span>
                              <span className="text-theme-text-tertiary">{match.path.type || 'PathPrefix'}:</span>{' '}
                              <span className="text-theme-text-primary">{match.path.value}</span>
                            </span>
                          )}
                          {match.headers && match.headers.length > 0 && (
                            <span className="text-theme-text-tertiary">
                              headers: [{match.headers.map((h: any) => `${h.name}=${h.value}`).join(', ')}]
                            </span>
                          )}
                          {match.queryParams && match.queryParams.length > 0 && (
                            <span className="text-theme-text-tertiary">
                              query: [{match.queryParams.map((q: any) => `${q.name}=${q.value}`).join(', ')}]
                            </span>
                          )}
                        </div>
                      ))}
                    </div>
                  )}
                </div>

                {/* Backends */}
                {backendRefs.length > 0 && (
                  <div className="mb-2">
                    <div className="text-xs text-theme-text-tertiary mb-1">Backends</div>
                    <div className="space-y-1">
                      {backendRefs.map((backend: any, backendIdx: number) => (
                        <div key={backendIdx} className="text-xs text-theme-text-secondary flex items-center gap-1.5">
                          <ArrowRight className="w-3 h-3 text-theme-text-tertiary shrink-0" />
                          <span className="text-blue-400">{backend.name}</span>
                          {backend.port && (
                            <span className="text-theme-text-tertiary">:{backend.port}</span>
                          )}
                          {backend.weight !== undefined && (
                            <span className={clsx(
                              'px-1.5 py-0.5 rounded text-xs',
                              'bg-theme-elevated text-theme-text-tertiary'
                            )}>
                              weight: {backend.weight}
                            </span>
                          )}
                        </div>
                      ))}
                    </div>
                  </div>
                )}

                {/* Filters */}
                {filters.length > 0 && (
                  <div>
                    <div className="text-xs text-theme-text-tertiary mb-1 flex items-center gap-1">
                      <Filter className="w-3 h-3" />
                      Filters
                    </div>
                    <div className="space-y-1">
                      {filters.map((filter: any, filterIdx: number) => (
                        <div key={filterIdx} className="text-xs text-theme-text-secondary">
                          <span className="px-1.5 py-0.5 bg-amber-500/20 text-amber-400 rounded">
                            {filter.type}
                          </span>
                          {filter.type === 'RequestHeaderModifier' && filter.requestHeaderModifier && (
                            <span className="ml-1.5 text-theme-text-tertiary">
                              {summarizeHeaderModifier(filter.requestHeaderModifier)}
                            </span>
                          )}
                          {filter.type === 'ResponseHeaderModifier' && filter.responseHeaderModifier && (
                            <span className="ml-1.5 text-theme-text-tertiary">
                              {summarizeHeaderModifier(filter.responseHeaderModifier)}
                            </span>
                          )}
                          {filter.type === 'RequestRedirect' && filter.requestRedirect && (
                            <span className="ml-1.5 text-theme-text-tertiary">
                              {filter.requestRedirect.scheme && `${filter.requestRedirect.scheme}://`}
                              {filter.requestRedirect.hostname || ''}
                              {filter.requestRedirect.port ? `:${filter.requestRedirect.port}` : ''}
                              {filter.requestRedirect.statusCode ? ` (${filter.requestRedirect.statusCode})` : ''}
                            </span>
                          )}
                          {filter.type === 'URLRewrite' && filter.urlRewrite && (
                            <span className="ml-1.5 text-theme-text-tertiary">
                              {filter.urlRewrite.hostname && `host: ${filter.urlRewrite.hostname}`}
                              {filter.urlRewrite.path?.replacePrefixMatch && ` path: ${filter.urlRewrite.path.replacePrefixMatch}`}
                            </span>
                          )}
                          {filter.type === 'RequestMirror' && filter.requestMirror && (
                            <span className="ml-1.5 text-theme-text-tertiary">
                              {filter.requestMirror.backendRef?.name || 'unknown'}
                              {filter.requestMirror.backendRef?.port ? `:${filter.requestMirror.backendRef.port}` : ''}
                            </span>
                          )}
                        </div>
                      ))}
                    </div>
                  </div>
                )}
              </div>
            )
          })}
        </div>
      </Section>

      {/* Parent Status section */}
      {parentStatuses.length > 0 && (
        <Section title={`Parent Status (${parentStatuses.length})`} defaultExpanded>
          <div className="space-y-3">
            {parentStatuses.map((parent: any, idx: number) => {
              const ref = parent.parentRef || {}
              const conditions = parent.conditions || []
              const accepted = conditions.find((c: any) => c.type === 'Accepted')
              const resolved = conditions.find((c: any) => c.type === 'ResolvedRefs')

              return (
                <div key={`${ref.namespace || ''}-${ref.name}-${idx}`} className="bg-theme-elevated/30 rounded p-3">
                  <div className="text-sm font-medium text-theme-text-primary mb-2">
                    {ref.namespace ? `${ref.namespace}/` : ''}{ref.name || 'unknown'}
                    {ref.sectionName ? ` (${ref.sectionName})` : ''}
                  </div>
                  <div className="flex flex-wrap gap-2">
                    {accepted && (
                      <span className={clsx(
                        'px-2 py-0.5 rounded text-xs font-medium',
                        accepted.status === 'True'
                          ? 'bg-green-500/20 text-green-400'
                          : 'bg-red-500/20 text-red-400'
                      )}>
                        {accepted.status === 'True' ? 'Accepted' : 'Not Accepted'}
                      </span>
                    )}
                    {resolved && (
                      <span className={clsx(
                        'px-2 py-0.5 rounded text-xs font-medium',
                        resolved.status === 'True'
                          ? 'bg-green-500/20 text-green-400'
                          : 'bg-yellow-500/20 text-yellow-400'
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

      {/* Conditions from first parent */}
      <ConditionsSection conditions={firstParentConditions} />
    </>
  )
}

function summarizeHeaderModifier(modifier: any): string {
  const parts: string[] = []
  if (modifier.set?.length) {
    parts.push(`set: ${modifier.set.map((h: any) => h.name).join(', ')}`)
  }
  if (modifier.add?.length) {
    parts.push(`add: ${modifier.add.map((h: any) => h.name).join(', ')}`)
  }
  if (modifier.remove?.length) {
    parts.push(`remove: ${modifier.remove.join(', ')}`)
  }
  return parts.join('; ')
}
