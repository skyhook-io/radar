import { Shield, Users, Eye } from 'lucide-react'
import { clsx } from 'clsx'
import { Section, PropertyList, Property, ResourceLink, AlertBanner } from '../../ui/drawer-components'
import type { ResourceRef, RBACPolicyRule } from '../../../types'
import { isForbiddenError } from '../../../types/fetch-error'
import {
  rbacVerbBadgeClass,
  rbacResourceBadgeClass,
  rbacApiGroupBadgeClass,
  rbacKindBadgeClass,
} from '../../../utils/rbac-badges'
import { BADGE_SEVERITY_COLORS } from '../../ui/Badge'

interface RoleBindingRendererProps {
  data: any
  onNavigate?: (ref: ResourceRef) => void
  /** Rules from the referenced Role/ClusterRole. Undefined means the host
   *  hasn't wired the fetch (inline rules preview is omitted). Null means
   *  the fetch finished without a resource (orphan binding); the section
   *  says so. */
  roleRules?: RBACPolicyRule[] | null
  roleRulesLoading?: boolean
  /** Error from the role/clusterrole fetch. When present and shaped like
   *  a FetchErrorShape (status + message), the rules section distinguishes
   *  a 403 ("Access denied") from the orphan-or-unavailable fallback. */
  roleRulesError?: unknown
}

// Wide groups whose membership effectively widens a binding beyond a named
// subject. Kept in sync with pkg/rbac/builtins.go (WideGroups). Mismatch
// here = misleading or missing warning, not a security flaw — but cross-
// check if you add to one side, add to the other.
const WIDE_GROUP_DESCRIPTIONS: Record<string, string> = {
  'system:authenticated':
    'every authenticated entity in the cluster, including external OIDC users',
  'system:unauthenticated':
    'every unauthenticated (anonymous) request',
  'system:masters':
    'the system:masters super-user group, which bypasses authorization',
}

function findWideGroupSubject(subjects: any[]): { name: string; description: string } | null {
  for (const s of subjects) {
    if (s.kind !== 'Group') continue
    const d = WIDE_GROUP_DESCRIPTIONS[s.name]
    if (d) return { name: s.name, description: d }
  }
  return null
}

// Subject-kind colors (ServiceAccount/User/Group). Reuse theme-aware palette
// classes for proper light/dark contrast.
function getSubjectKindBadgeClass(kind: string): string {
  switch (kind) {
    case 'ServiceAccount':
      return BADGE_SEVERITY_COLORS.success
    case 'User':
      return BADGE_SEVERITY_COLORS.info
    case 'Group':
      return rbacKindBadgeClass('ClusterRole') // purple — distinct from User
    default:
      return BADGE_SEVERITY_COLORS.neutral
  }
}

export function RoleBindingRenderer({ data, onNavigate, roleRules, roleRulesLoading, roleRulesError }: RoleBindingRendererProps) {
  const roleRef = data.roleRef || {}
  const subjects: any[] = data.subjects || []
  const isClusterRoleBinding = data.kind === 'ClusterRoleBinding'
  const wideGroup = findWideGroupSubject(subjects)

  return (
    <>
      {subjects.length === 0 && (
        <AlertBanner
          variant="warning"
          title="No Subjects"
          message="This binding has no subjects — it has no effect until subjects are added."
        />
      )}

      {/* Wide-group warning — describe the blast radius, don't just label it. */}
      {wideGroup && (
        <AlertBanner variant="warning" title={`Wide grant: ${wideGroup.name}`}>
          <div className="text-xs">
            This binding grants{' '}
            <span className="text-theme-text-secondary font-medium">
              {roleRef.kind || 'a role'}{roleRef.name ? ` "${roleRef.name}"` : ''}
            </span>{' '}
            to {wideGroup.description}.
          </div>
        </AlertBanner>
      )}

      {isClusterRoleBinding && (
        <div className="p-2 bg-blue-500/10 border border-blue-500/30 rounded text-xs text-blue-300/80 flex items-start gap-2">
          <span>ClusterRoleBinding grants permissions across all namespaces.</span>
        </div>
      )}

      <Section title="Role Reference" icon={Shield}>
        <PropertyList>
          <Property
            label="Kind"
            value={
              roleRef.kind ? (
                <span className={clsx('badge', rbacKindBadgeClass(roleRef.kind))}>
                  {roleRef.kind}
                </span>
              ) : undefined
            }
          />
          <Property label="Name" value={
            roleRef.name ? <ResourceLink name={roleRef.name} kind={roleRef.kind === 'ClusterRole' ? 'clusterroles' : 'roles'} namespace={roleRef.kind === 'ClusterRole' ? '' : (data.metadata?.namespace || '')} onNavigate={onNavigate} /> : undefined
          } />
          <Property label="API Group" value={roleRef.apiGroup} />
        </PropertyList>
      </Section>

      {/* Inline rules preview — saves a click when operators are validating
       *  "what does this binding actually grant". Only when host wired fetch. */}
      {roleRules !== undefined && (
        <RulesPreviewSection
          rules={roleRules}
          loading={!!roleRulesLoading}
          error={roleRulesError}
          roleName={roleRef.name}
        />
      )}

      <Section title={`Subjects (${subjects.length})`} icon={Users} defaultExpanded>
        <div className="space-y-2">
          {subjects.map((subject: any, i: number) => (
            <div key={`${subject.kind}-${subject.name}-${i}`} className="card-inner text-sm">
              <div className="flex items-center gap-2">
                <span className={clsx('badge', getSubjectKindBadgeClass(subject.kind))}>
                  {subject.kind}
                </span>
                {subject.kind === 'ServiceAccount' ? (
                  <ResourceLink name={subject.name} kind="serviceaccounts" namespace={subject.namespace || 'default'} onNavigate={onNavigate} />
                ) : (
                  <span className="text-theme-text-primary font-medium">{subject.name}</span>
                )}
                {subject.kind === 'Group' && WIDE_GROUP_DESCRIPTIONS[subject.name] && (
                  <span className={clsx('badge text-xs', BADGE_SEVERITY_COLORS.alert)}>wide</span>
                )}
              </div>
              <div className="text-xs text-theme-text-tertiary mt-1">
                Namespace: {subject.kind === 'ServiceAccount' ? (subject.namespace || 'default') : '-'}
              </div>
            </div>
          ))}
        </div>
      </Section>
    </>
  )
}

function RulesPreviewSection({
  rules,
  loading,
  error,
  roleName,
}: {
  rules: RBACPolicyRule[] | null
  loading: boolean
  error?: unknown
  roleName?: string
}) {
  return (
    <Section title="Rules Granted" icon={Eye} defaultExpanded={false}>
      {loading ? (
        <div className="text-sm text-theme-text-tertiary">Loading rules…</div>
      ) : !rules ? (
        isForbiddenError(error) ? (
          <div className="text-sm text-theme-text-tertiary">
            Access denied reading referenced role
            {roleName ? ` "${roleName}"` : ''}.
          </div>
        ) : (
          <div className="text-sm text-theme-text-tertiary">
            Could not resolve referenced role
            {roleName ? ` "${roleName}"` : ''} — it may not exist (orphan binding)
            or be unavailable.
          </div>
        )
      ) : rules.length === 0 ? (
        <div className="text-sm text-theme-text-tertiary">
          The referenced role has no rules.
        </div>
      ) : (
        <div className="space-y-1">
          {rules.map((r, i) => {
            const resources = r.resources ?? []
            const nonResourceURLs = r.nonResourceURLs ?? []
            // Non-resource rules (e.g. system:discovery grants `get` on
            // /api, /healthz) have no resources/apiGroups - render the
            // URLs in place so the line doesn't read as a bare "X on"
            // with nothing after it.
            const isNonResource = resources.length === 0 && nonResourceURLs.length > 0
            return (
              <div key={i} className="flex items-center gap-1 flex-wrap text-xs">
                {(r.verbs ?? []).map((v) => (
                  <span key={v} className={clsx('badge', rbacVerbBadgeClass(v))}>{v}</span>
                ))}
                <span className="text-theme-text-secondary">on</span>
                {isNonResource ? (
                  nonResourceURLs.map((u) => (
                    <span key={u} className="badge text-xs font-mono bg-theme-elevated text-theme-text-secondary">{u}</span>
                  ))
                ) : (
                  resources.map((res) => (
                    <span key={res} className={clsx('badge', rbacResourceBadgeClass)}>{res}</span>
                  ))
                )}
                {!isNonResource && (r.apiGroups ?? []).filter((g) => g !== '').map((g) => (
                  <span key={g} className={clsx('badge', rbacApiGroupBadgeClass)}>{g}</span>
                ))}
              </div>
            )
          })}
        </div>
      )}
    </Section>
  )
}
