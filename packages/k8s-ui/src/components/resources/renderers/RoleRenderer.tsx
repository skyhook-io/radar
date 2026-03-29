import { Shield, Info } from 'lucide-react'
import { clsx } from 'clsx'
import { Section, PropertyList, Property, AlertBanner } from '../../ui/drawer-components'

interface RoleRendererProps {
  data: any
}

const READ_VERBS = new Set(['get', 'list', 'watch'])
const WRITE_VERBS = new Set(['create', 'update', 'patch'])
const DELETE_VERBS = new Set(['delete', 'deletecollection'])

function getVerbColor(verb: string): string {
  if (verb === '*') return 'bg-red-500/20 text-red-400'
  if (READ_VERBS.has(verb)) return 'bg-green-500/20 text-green-400'
  if (WRITE_VERBS.has(verb)) return 'bg-yellow-500/20 text-yellow-400'
  if (DELETE_VERBS.has(verb)) return 'bg-red-500/20 text-red-400'
  return 'bg-blue-500/20 text-blue-400'
}

export function RoleRenderer({ data }: RoleRendererProps) {
  const rules = data.rules || []
  const aggregationRule = data.aggregationRule

  const hasWildcard = rules.some((r: any) =>
    (r.verbs || []).includes('*') || (r.apiGroups || []).includes('*') || (r.resources || []).includes('*')
  )

  return (
    <>
      {hasWildcard && (
        <AlertBanner
          variant="warning"
          title="Wildcard Permissions"
          message="This role grants wildcard (*) permissions. Review the rules to ensure this level of access is intended."
        />
      )}

      {/* Overview */}
      <Section title="Overview" icon={Shield}>
        <PropertyList>
          <Property label="Rules" value={rules.length} />
          {aggregationRule && (
            <Property
              label="Type"
              value={
                <span className="text-blue-400">Aggregated ClusterRole</span>
              }
            />
          )}
        </PropertyList>
        {aggregationRule && (
          <div className="mt-2 p-2 bg-blue-500/10 border border-blue-500/30 rounded text-xs text-blue-300/80 flex items-start gap-2">
            <Info className="w-3.5 h-3.5 mt-0.5 shrink-0 text-blue-400" />
            <span>
              This ClusterRole is automatically aggregated from other ClusterRoles
              matching the specified label selectors.
            </span>
          </div>
        )}
      </Section>

      {/* Rules */}
      <Section title={`Rules (${rules.length})`} icon={Shield} defaultExpanded>
        <div className="space-y-3">
          {rules.map((rule: any, i: number) => (
            <div key={i} className="card-inner-lg">
              {/* API Groups */}
              {rule.apiGroups && rule.apiGroups.length > 0 && (
                <div className="mb-2">
                  <div className="text-xs text-theme-text-tertiary mb-1">API Groups</div>
                  <div className="flex flex-wrap gap-1">
                    {rule.apiGroups.map((group: string, gi: number) => (
                      <span
                        key={gi}
                        className="badge bg-theme-elevated text-theme-text-secondary"
                      >
                        {group === '' ? 'core' : group}
                      </span>
                    ))}
                  </div>
                </div>
              )}

              {/* Resources */}
              {rule.resources && rule.resources.length > 0 && (
                <div className="mb-2">
                  <div className="text-xs text-theme-text-tertiary mb-1">Resources</div>
                  <div className="flex flex-wrap gap-1">
                    {rule.resources.map((resource: string) => (
                      <span
                        key={resource}
                        className="badge bg-purple-500/20 text-purple-400"
                      >
                        {resource}
                      </span>
                    ))}
                  </div>
                </div>
              )}

              {/* Verbs */}
              {rule.verbs && rule.verbs.length > 0 && (
                <div className="mb-2">
                  <div className="text-xs text-theme-text-tertiary mb-1">Verbs</div>
                  <div className="flex flex-wrap gap-1">
                    {rule.verbs.map((verb: string) => (
                      <span
                        key={verb}
                        className={clsx(
                          'badge',
                          getVerbColor(verb)
                        )}
                      >
                        {verb}
                      </span>
                    ))}
                  </div>
                </div>
              )}

              {/* Resource Names */}
              {rule.resourceNames && rule.resourceNames.length > 0 && (
                <div className="mb-2">
                  <div className="text-xs text-theme-text-tertiary mb-1">Resource Names</div>
                  <div className="flex flex-wrap gap-1">
                    {rule.resourceNames.map((name: string) => (
                      <span
                        key={name}
                        className="badge bg-cyan-500/20 text-cyan-400"
                      >
                        {name}
                      </span>
                    ))}
                  </div>
                </div>
              )}

              {/* Non-Resource URLs */}
              {rule.nonResourceURLs && rule.nonResourceURLs.length > 0 && (
                <div>
                  <div className="text-xs text-theme-text-tertiary mb-1">Non-Resource URLs</div>
                  <div className="flex flex-wrap gap-1">
                    {rule.nonResourceURLs.map((url: string) => (
                      <span
                        key={url}
                        className="badge bg-orange-500/20 text-orange-400"
                      >
                        {url}
                      </span>
                    ))}
                  </div>
                </div>
              )}
            </div>
          ))}

          {rules.length === 0 && (
            <div className="text-sm text-theme-text-tertiary">No rules defined</div>
          )}
        </div>
      </Section>

      {/* Aggregation Rule */}
      {aggregationRule && aggregationRule.clusterRoleSelectors && (
        <Section title="Aggregation Rule" defaultExpanded>
          <div className="space-y-3">
            {aggregationRule.clusterRoleSelectors.map((selector: any, i: number) => (
              <div key={i} className="card-inner-lg">
                <div className="text-xs text-theme-text-tertiary mb-1">Match Labels</div>
                {selector.matchLabels && Object.keys(selector.matchLabels).length > 0 ? (
                  <div className="flex flex-wrap gap-1">
                    {Object.entries(selector.matchLabels).map(([k, v]) => (
                      <span
                        key={k}
                        className="badge bg-theme-elevated text-theme-text-secondary"
                      >
                        {k}={String(v)}
                      </span>
                    ))}
                  </div>
                ) : (
                  <div className="text-xs text-theme-text-tertiary">No labels specified</div>
                )}
              </div>
            ))}
          </div>
        </Section>
      )}
    </>
  )
}
