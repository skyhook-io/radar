import { Shield, Info, Users } from 'lucide-react'
import { clsx } from 'clsx'
import { Section, PropertyList, Property, AlertBanner, ResourceLink } from '../../ui/drawer-components'
import type { RBACRoleResponse, RBACSubject, ResourceRef } from '../../../types'
import { rbacKindBadgeClass, rbacVerbBadgeClass } from '../../../utils/rbac-badges'
import { RBACErrorSection } from './RBACErrorSection'

interface RoleRendererProps {
  data: any
  /** Reverse-lookup data from /api/rbac/role/{kind}/{namespace}/{name}.
   *  Undefined when the host hasn't wired the fetch (Bindings section is
   *  omitted). Null when the fetch failed (renders an inline error). */
  rbacRoleData?: RBACRoleResponse | null
  rbacRoleLoading?: boolean
  rbacRoleError?: Error | null
  onNavigate?: (ref: ResourceRef) => void
}


export function RoleRenderer({ data, rbacRoleData, rbacRoleLoading, rbacRoleError, onNavigate }: RoleRendererProps) {
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

      {/* Bindings reverse-lookup — only when host wired the fetch. */}
      {rbacRoleData !== undefined && (
        <RoleBindingsSection
          rbacRoleData={rbacRoleData}
          loading={!!rbacRoleLoading}
          error={rbacRoleError ?? null}
          onNavigate={onNavigate}
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
                        className={clsx('badge', rbacVerbBadgeClass(verb))}
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

// ============================================================================
// ROLE BINDINGS REVERSE-LOOKUP SECTION
// ============================================================================

interface RoleBindingsSectionProps {
  rbacRoleData: RBACRoleResponse | null
  loading: boolean
  error: Error | null
  onNavigate?: (ref: ResourceRef) => void
}

function RoleBindingsSection({ rbacRoleData, loading, error, onNavigate }: RoleBindingsSectionProps) {
  if (loading) {
    return (
      <Section title="Bindings" icon={Users}>
        <div className="text-sm text-theme-text-tertiary">Loading bindings…</div>
      </Section>
    )
  }
  if (error) {
    return <RBACErrorSection title="Bindings" error={error} icon={Users} errorPrefix="Could not load bindings" />
  }
  if (!rbacRoleData) return null

  const bindings = rbacRoleData.bindings ?? []
  return (
    <Section title={`Bindings (${bindings.length})`} icon={Users} defaultExpanded>
      {bindings.length === 0 ? (
        <div className="text-sm text-theme-text-tertiary">
          No bindings reference this role. It grants no effective permissions.
        </div>
      ) : (
        <div className="space-y-2">
          {bindings.map((b) => {
            const bindingKindPlural =
              b.binding.kind === 'RoleBinding' ? 'rolebindings' : 'clusterrolebindings'
            return (
              <div key={b.binding.kind + '/' + b.binding.namespace + '/' + b.binding.name} className="card-inner">
                {/* items-center + uniform text-xs so the badge, link, and
                 *  "granted to N subjects" footnote share a baseline. */}
                <div className="flex items-center gap-2 flex-wrap text-xs mb-2">
                  <span className={clsx('badge', rbacKindBadgeClass(b.binding.kind))}>
                    {b.binding.kind}
                  </span>
                  <ResourceLink
                    kind={bindingKindPlural}
                    namespace={b.binding.namespace}
                    name={b.binding.name}
                    onNavigate={onNavigate}
                  />
                  <span className="text-theme-text-secondary">
                    granted to {b.subjects.length} subject{b.subjects.length === 1 ? '' : 's'}
                  </span>
                </div>
                <RoleBindingSubjectsList subjects={b.subjects} onNavigate={onNavigate} />
              </div>
            )
          })}
        </div>
      )}
    </Section>
  )
}

function RoleBindingSubjectsList({
  subjects,
  onNavigate,
}: {
  subjects: RBACSubject[]
  onNavigate?: (ref: ResourceRef) => void
}) {
  const inline = subjects.slice(0, 3)
  const hidden = subjects.length - inline.length
  return (
    <div className="flex flex-wrap gap-1 items-center text-xs">
      {inline.map((s, i) => (
        <RoleBindingSubjectChip key={i} subject={s} onNavigate={onNavigate} />
      ))}
      {hidden > 0 && (
        <span className="text-theme-text-secondary">+{hidden} more</span>
      )}
    </div>
  )
}

function RoleBindingSubjectChip({
  subject,
  onNavigate,
}: {
  subject: RBACSubject
  onNavigate?: (ref: ResourceRef) => void
}) {
  // ServiceAccounts have a detail page; Users and Groups don't (those are
  // external identities — there's no "User" resource in Kubernetes).
  if (subject.kind === 'ServiceAccount') {
    return (
      <span className="inline-flex items-center gap-1 px-1.5 py-0.5 rounded bg-theme-elevated border border-theme-border">
        <span className="text-theme-text-secondary">sa:</span>
        <ResourceLink
          kind="serviceaccounts"
          namespace={subject.namespace}
          name={subject.name}
          onNavigate={onNavigate}
        />
        {subject.namespace && <span className="text-theme-text-secondary">({subject.namespace})</span>}
      </span>
    )
  }
  return (
    <span
      className="inline-flex items-center gap-1 px-1.5 py-0.5 rounded bg-theme-elevated border border-theme-border text-theme-text-primary"
      title={`${subject.kind} — no detail page (external identity)`}
    >
      <span className="text-theme-text-secondary">{subject.kind.toLowerCase()}:</span>
      {subject.name}
    </span>
  )
}
