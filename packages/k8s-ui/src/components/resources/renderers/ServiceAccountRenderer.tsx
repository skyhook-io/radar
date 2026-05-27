import { useState } from 'react'
import { UserCog, Key, Cloud, Shield, Users, AlertTriangle, Box } from 'lucide-react'
import { clsx } from 'clsx'
import { Section, PropertyList, Property, ResourceLink, AlertBanner } from '../../ui/drawer-components'
import type { ResourceRef } from '../../../types'
import type {
  RBACSubjectResponse,
  RBACBindingRules,
  RBACPolicyRule,
  RBACInheritedGroup,
} from '../../../types'
import {
  rbacVerbBadgeClass,
  rbacResourceBadgeClass,
  rbacApiGroupBadgeClass,
  rbacKindBadgeClass,
} from '../../../utils/rbac-badges'
import { RBAC_BLAST_ESCALATION_VERBS } from '../../../utils/rbac-blast-radius'
import { RBACErrorSection } from './RBACErrorSection'
import { BADGE_SEVERITY_COLORS } from '../../ui/Badge'

interface ServiceAccountRendererProps {
  data: any
  /** RBAC reverse-lookup data fetched from /api/rbac/subject/ServiceAccount/{ns}/{name}.
   *  Undefined while loading; null when the fetch failed (renderer shows a tactful
   *  fallback). When omitted entirely (consumer didn't wire the fetch) the
   *  Bindings/Permissions sections are simply not rendered. */
  rbacData?: RBACSubjectResponse | null
  rbacLoading?: boolean
  rbacError?: Error | null
  /** Navigation callback for ResourceLinks pointing at bindings / roles. */
  onNavigate?: (ref: ResourceRef) => void
}

// "Dangerous" specifically — not the same as "permissive". Resource-wildcards
// with read-only verbs (e.g. `view` granting list/watch on `*` within a CRD
// group) are not alarming and were creating banner-fatigue on every SA. We
// only flag what a compromised SA could *act on*: verb wildcards, escalation
// verbs (escalate/bind/impersonate), and the cluster-admin role.
//
// Distinct from detectBlastRadius (utils/rbac-blast-radius.ts) — the SA page
// scopes to *direct* bindings only and returns boolean (does any direct
// binding qualify?), whereas the Pod/Workload pages walk direct + inherited
// and produce per-binding reasons. Sharing the implementation would force
// one of those to wear the wrong shape.
function bindingHasDangerousGrant(br: RBACBindingRules): boolean {
  if (br.role.name === 'cluster-admin' && br.role.kind === 'ClusterRole') return true
  return (br.rules ?? []).some((r) => {
    const verbs = r.verbs ?? []
    if (verbs.includes('*')) return true
    if (verbs.some((v) => RBAC_BLAST_ESCALATION_VERBS.has(v))) return true
    return false
  })
}

export function ServiceAccountRenderer({
  data,
  rbacData,
  rbacLoading,
  rbacError,
  onNavigate,
}: ServiceAccountRendererProps) {
  const metadata = data.metadata || {}
  const annotations = metadata.annotations || {}
  const secrets = data.secrets || []
  const imagePullSecrets = data.imagePullSecrets || []

  // automountServiceAccountToken defaults to true if not set
  const automountToken = data.automountServiceAccountToken !== false

  // Workload identity annotations
  const gcpServiceAccount = annotations['iam.gke.io/gcp-service-account']
  const awsRoleArn = annotations['eks.amazonaws.com/role-arn']
  const azureClientId = annotations['azure.workload.identity/client-id']
  const hasWorkloadIdentity = gcpServiceAccount || awsRoleArn || azureClientId

  // Risk signal: scoped to *direct* bindings only. Inherited grants come from
  // system groups every SA is in (system:authenticated, system:serviceaccounts),
  // so flagging them here fires the banner on every SA — operators banner-blind
  // and stop trusting the signal. Inherited risk surfaces in the per-binding
  // rows below (the "permissive" chip) instead.
  const dangerousBinding = (rbacData?.direct ?? []).find(bindingHasDangerousGrant)

  return (
    <>
      {dangerousBinding && (
        <AlertBanner
          variant="warning"
          title="Permissive grant detected"
          message={
            `This ServiceAccount is bound to ${dangerousBinding.role.kind} ` +
            `"${dangerousBinding.role.name}", which grants verb wildcards or ` +
            `privilege-escalation verbs. A compromise of this SA — leaked token, ` +
            `compromised Pod — would give an attacker broad cluster access.`
          }
        />
      )}

      {/* Configuration */}
      <Section title="Configuration" icon={UserCog}>
        <PropertyList>
          <Property
            label="Automount Token"
            value={
              <span
                className={automountToken ? 'text-yellow-400' : 'text-green-400'}
                title={automountToken ? 'Token is automatically mounted in pods' : undefined}
              >
                {automountToken ? 'Yes' : 'No'}
              </span>
            }
          />
        </PropertyList>
      </Section>

      {/* RBAC: Direct Bindings + Effective Permissions. Only rendered when
       *  the host has wired the fetch (rbacData is not undefined). */}
      {rbacData !== undefined && (
        <RBACSections
          rbacData={rbacData}
          loading={!!rbacLoading}
          error={rbacError ?? null}
          onNavigate={onNavigate}
        />
      )}

      {/* Pods using this SA — closes the loop. The bindings sections answer
       *  "what permissions"; this answers "what's actually running as this".
       *  Always rendered for SA subjects, even when the list is empty (the
       *  backend's omitempty drops zero-length slices, so we don't gate on
       *  the field's presence — `lonely-sa` should still show "No pods
       *  are running as this SA" rather than vanish from the page). */}
      {rbacData && rbacData.subject.kind === 'ServiceAccount' && (
        <UsedByPodsSection pods={rbacData.usedByPods ?? []} onNavigate={onNavigate} />
      )}

      {/* Workload Identity */}
      {hasWorkloadIdentity && (
        <Section title="Workload Identity" icon={Cloud}>
          <PropertyList>
            <Property label="GCP Service Account" value={gcpServiceAccount} />
            <Property label="AWS Role ARN" value={awsRoleArn} />
            <Property label="Azure Client ID" value={azureClientId} />
          </PropertyList>
        </Section>
      )}

      {/* Secrets */}
      {secrets.length > 0 && (
        <Section title={`Secrets (${secrets.length})`} icon={Key}>
          <PropertyList>
            {secrets.map((secret: any) => (
              <Property key={secret.name} label="Secret" value={secret.name} />
            ))}
          </PropertyList>
        </Section>
      )}

      {/* Image Pull Secrets */}
      {imagePullSecrets.length > 0 && (
        <Section title={`Image Pull Secrets (${imagePullSecrets.length})`}>
          <div className="flex flex-wrap gap-1">
            {imagePullSecrets.map((secret: any) => (
              <span
                key={secret.name}
                className="badge bg-theme-elevated text-theme-text-secondary"
              >
                {secret.name}
              </span>
            ))}
          </div>
        </Section>
      )}
    </>
  )
}

// ---------------------------------------------------------------------------
// RBAC sections — pulled out so the main renderer body stays readable.
// ---------------------------------------------------------------------------

interface RBACSectionsProps {
  rbacData: RBACSubjectResponse | null
  loading: boolean
  error: Error | null
  onNavigate?: (ref: ResourceRef) => void
}

function RBACSections({ rbacData, loading, error, onNavigate }: RBACSectionsProps) {
  if (loading) {
    return (
      <Section title="Bindings" icon={Shield}>
        <div className="text-sm text-theme-text-tertiary">Loading RBAC graph…</div>
      </Section>
    )
  }
  if (error) {
    return <RBACErrorSection title="Bindings" error={error} errorPrefix="Could not load RBAC data" />
  }
  if (!rbacData) return null

  const direct = rbacData.direct ?? []
  const inherited = rbacData.inheritedFromGroups ?? []
  const hasAnyBinding =
    direct.length > 0 || inherited.some((g) => g.bindings.length > 0)

  return (
    <>
      <Section
        title={`Direct Bindings (${direct.length})`}
        icon={Shield}
        defaultExpanded
      >
        {direct.length === 0 ? (
          <div className="text-sm text-theme-text-tertiary">
            No bindings reference this ServiceAccount directly.
            {!hasAnyBinding &&
              ' It has no permissions beyond what implicit group memberships grant (see below).'}
          </div>
        ) : (
          <div className="space-y-2">
            {direct.map((br) => (
              <BindingRow key={br.binding.kind + '/' + br.binding.namespace + '/' + br.binding.name} br={br} onNavigate={onNavigate} />
            ))}
          </div>
        )}
      </Section>

      {/* Inherited from groups — collapsed by default (the load-bearing
       *  choice) so direct bindings stay the focal point. system:authenticated
       *  alone can drag in dozens of bindings on a real cluster; expanding
       *  them here would crowd out the actually-SA-specific direct bindings
       *  above and inflate the perceived blast radius. */}
      {inherited.map((group) => (
        <InheritedGroupSection
          key={group.groupName}
          group={group}
          onNavigate={onNavigate}
        />
      ))}

      <EffectivePermissionsSection rbacData={rbacData} />
    </>
  )
}

function InheritedGroupSection({
  group,
  onNavigate,
}: {
  group: RBACInheritedGroup
  onNavigate?: (ref: ResourceRef) => void
}) {
  if (group.bindings.length === 0) return null
  return (
    <Section
      title={`Inherited via ${group.groupName} (${group.bindings.length})`}
      icon={Users}
      defaultExpanded={false}
    >
      <div className="space-y-2">
        {group.bindings.map((br) => (
          <BindingRow
            key={br.binding.kind + '/' + br.binding.namespace + '/' + br.binding.name}
            br={br}
            onNavigate={onNavigate}
            inherited
          />
        ))}
      </div>
    </Section>
  )
}

function BindingRow({
  br,
  onNavigate,
  inherited,
}: {
  br: RBACBindingRules
  onNavigate?: (ref: ResourceRef) => void
  inherited?: boolean
}) {
  // Map binding kind to the lowercase plural the resource-routing layer
  // expects. The detail page route normalises these.
  const bindingKindPlural =
    br.binding.kind === 'RoleBinding' ? 'rolebindings' : 'clusterrolebindings'
  const roleKindPlural =
    br.role.kind === 'Role' ? 'roles' : 'clusterroles'

  const risky = bindingHasDangerousGrant(br)

  return (
    <div className={clsx('card-inner', risky && 'border-orange-500/40')}>
      {/* items-center + uniform text-xs so badges, arrow, and ResourceLink
       *  text share a baseline. items-start + a parent without a text size
       *  let ResourceLink inherit default sm/base while siblings were xs,
       *  producing visible baseline drift. */}
      <div className="flex items-center gap-2 flex-wrap text-xs">
        <span className={clsx('badge', rbacKindBadgeClass(br.binding.kind))}>
          {br.binding.kind}
        </span>
        <ResourceLink
          kind={bindingKindPlural}
          namespace={br.binding.namespace}
          name={br.binding.name}
          onNavigate={onNavigate}
        />
        <span className="text-theme-text-secondary">→</span>
        <span className={clsx('badge', rbacKindBadgeClass(br.role.kind))}>
          {br.role.kind}
        </span>
        <ResourceLink
          kind={roleKindPlural}
          namespace={br.role.namespace}
          name={br.role.name}
          onNavigate={onNavigate}
        />
        {br.scopeNamespace && (
          <span
            className={clsx('badge', rbacApiGroupBadgeClass)}
            title="RoleBinding grants a ClusterRole's rules only within this namespace"
          >
            scoped to {br.scopeNamespace}
          </span>
        )}
        {risky && (
          <span
            className={clsx('badge inline-flex items-center gap-1', BADGE_SEVERITY_COLORS.alert)}
            title="This binding grants wildcard or escalation permissions"
          >
            <AlertTriangle className="w-3 h-3" />
            permissive
          </span>
        )}
        {inherited && (
          <span className="text-theme-text-secondary italic">
            inherited
          </span>
        )}
      </div>
      {/* Rule preview — capped to keep the row compact. */}
      {br.rules && br.rules.length > 0 && (
        <RulesPreview rules={br.rules} max={3} />
      )}
      {br.rules && br.rules.length === 0 && (
        <div className="mt-2 text-xs text-orange-400">
          Referenced role has no rules (or could not be resolved — may be an orphan binding).
        </div>
      )}
    </div>
  )
}

function RulesPreview({ rules, max }: { rules: RBACPolicyRule[]; max: number }) {
  const shown = rules.slice(0, max)
  const hidden = rules.length - shown.length
  return (
    <div className="mt-2 space-y-1">
      {shown.map((r, i) => (
        <RuleLine key={i} rule={r} />
      ))}
      {hidden > 0 && (
        <div className="text-xs text-theme-text-tertiary">+{hidden} more rule{hidden === 1 ? '' : 's'}</div>
      )}
    </div>
  )
}

function RuleLine({ rule }: { rule: RBACPolicyRule }) {
  const verbs = rule.verbs ?? []
  const resources = rule.resources ?? []
  const nonResourceURLs = rule.nonResourceURLs ?? []
  const groups = rule.apiGroups ?? []
  // Non-resource rules (e.g. system:discovery grants `get` on /api, /healthz)
  // have no resources/apiGroups — only URLs. Render those in place; suppresses
  // the misleading bare "get on" with nothing after.
  const isNonResource = resources.length === 0 && nonResourceURLs.length > 0
  return (
    <div className="flex items-center gap-1 flex-wrap text-xs">
      {verbs.map((v) => (
        <span key={v} className={clsx('badge text-xs', rbacVerbBadgeClass(v))}>{v}</span>
      ))}
      <span className="text-theme-text-secondary">on</span>
      {isNonResource ? (
        nonResourceURLs.map((u) => (
          <span key={u} className="badge text-xs font-mono bg-theme-elevated text-theme-text-secondary">{u}</span>
        ))
      ) : (
        resources.map((r) => (
          <span key={r} className={clsx('badge text-xs', rbacResourceBadgeClass)}>
            {r}
          </span>
        ))
      )}
      {!isNonResource && groups.length > 0 && groups.some((g) => g !== '') && (
        <>
          <span className="text-theme-text-secondary">in</span>
          {groups.map((g) => (
            <span key={g} className={clsx('badge text-xs', rbacApiGroupBadgeClass)}>
              {g === '' ? 'core' : g}
            </span>
          ))}
        </>
      )}
    </div>
  )
}

// Effective permissions: tab between per-binding (provenance preserved) and
// the flat deduplicated rule set ("does this SA have X anywhere?"). Collapsed
// by default — the Direct Bindings section above is the primary view.
function EffectivePermissionsSection({ rbacData }: { rbacData: RBACSubjectResponse }) {
  const [tab, setTab] = useState<'by-binding' | 'flat'>('by-binding')
  const allBindings: RBACBindingRules[] = [
    ...rbacData.direct,
    ...rbacData.inheritedFromGroups.flatMap((g) => g.bindings),
  ]
  const ruleCount = rbacData.flat.length

  return (
    <Section
      title={`Effective Permissions (${ruleCount}${rbacData.truncated ? '+' : ''} rule${ruleCount === 1 ? '' : 's'})`}
      icon={Shield}
    >
      <div className="flex gap-1 mb-3">
        <button
          className={clsx(
            'btn-brand-toggle text-xs px-2 py-1',
            tab === 'by-binding' && 'bg-theme-elevated',
          )}
          onClick={() => setTab('by-binding')}
        >
          By binding
        </button>
        <button
          className={clsx(
            'btn-brand-toggle text-xs px-2 py-1',
            tab === 'flat' && 'bg-theme-elevated',
          )}
          onClick={() => setTab('flat')}
        >
          All rules (flat)
        </button>
      </div>

      {rbacData.truncated && (
        <div className="mb-2 text-xs text-orange-400">
          Rule list capped at {ruleCount} entries — this SA has more
          inherited bindings than fit in the summary. View the underlying
          Role/ClusterRole YAML for the full picture.
        </div>
      )}

      {tab === 'by-binding' && (
        <div className="space-y-3">
          {allBindings.length === 0 ? (
            <div className="text-sm text-theme-text-tertiary">No effective permissions.</div>
          ) : (
            allBindings.map((br, i) => (
              <div key={i} className="card-inner-lg">
                <div className="text-xs text-theme-text-tertiary mb-1">
                  Via {br.binding.kind} <span className="text-theme-text-secondary">{br.binding.name}</span>
                  {' → '}
                  {br.role.kind} <span className="text-theme-text-secondary">{br.role.name}</span>
                </div>
                <div className="space-y-1">
                  {(br.rules ?? []).map((r, j) => (
                    <RuleLine key={j} rule={r} />
                  ))}
                  {(!br.rules || br.rules.length === 0) && (
                    <div className="text-xs text-theme-text-tertiary">No rules (orphan binding or empty role)</div>
                  )}
                </div>
              </div>
            ))
          )}
        </div>
      )}

      {tab === 'flat' && (
        <div className="space-y-1">
          {rbacData.flat.length === 0 ? (
            <div className="text-sm text-theme-text-tertiary">No effective permissions.</div>
          ) : (
            rbacData.flat.map((r, i) => <RuleLine key={i} rule={r} />)
          )}
        </div>
      )}
    </Section>
  )
}

// ============================================================================
// USED BY PODS SECTION
// ============================================================================
// Closes the loop on the SA detail page: the bindings sections answer "what
// permissions" — this answers "what workloads inherit them". Operators
// reading top-down should be able to ask both questions without leaving.

function UsedByPodsSection({
  pods,
  onNavigate,
}: {
  pods: { namespace: string; name: string }[]
  onNavigate?: (ref: ResourceRef) => void
}) {
  return (
    <Section title={`Used by Pods (${pods.length})`} icon={Box}>
      {pods.length === 0 ? (
        <div className="text-sm text-theme-text-secondary">
          No Pods are currently running as this ServiceAccount.
        </div>
      ) : (
        <div className="flex flex-wrap gap-1.5 text-xs">
          {pods.map((p) => (
            <ResourceLink
              key={p.namespace + '/' + p.name}
              kind="pods"
              namespace={p.namespace}
              name={p.name}
              onNavigate={onNavigate}
            />
          ))}
        </div>
      )}
    </Section>
  )
}
