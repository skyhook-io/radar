import { useState, useEffect, useRef } from 'react'
import { Loader2, Lock, ExternalLink } from 'lucide-react'
import { clsx } from 'clsx'
import { useQuery } from '@tanstack/react-query'
import {
  rbacVerbBadgeClass,
  rbacResourceBadgeClass,
  rbacApiGroupBadgeClass,
  rbacResourceNameBadgeClass,
  rbacNonResourceUrlBadgeClass,
  type RBACWhoamiResponse,
} from '@skyhook-io/k8s-ui'
import { useNamespaces, useAuthMe, fetchJSON } from '../../api/client'
import { useRBACWhoami } from '../../api/rbac'

// MyPermissionsContent renders the "your access on this cluster" viewer inline
// (no modal chrome) so the Settings dialog can host it directly in its content
// pane. `active` gates the apiserver queries so opening Settings on another
// section doesn't fire a SelfSubjectRulesReview the user never asked for.
export function MyPermissionsContent({ active }: { active: boolean }) {
  const [namespace, setNamespace] = useState('default')
  const { data: authMe } = useAuthMe()
  const { data: namespaces } = useNamespaces()
  const { data: whoami, isLoading, error } = useRBACWhoami(namespace, active)

  // Land on an accessible namespace: once the list loads, if the current pick
  // ('default' initially) isn't among the user's namespaces and they haven't
  // chosen one, switch to the first accessible one — otherwise a user scoped to
  // e.g. team-a would open on an empty 'default'.
  const userPicked = useRef(false)
  useEffect(() => {
    if (userPicked.current || !namespaces?.length) return
    if (!namespaces.some((ns) => ns.name === namespace)) {
      setNamespace(namespaces[0].name)
    }
  }, [namespaces, namespace])

  return (
    <div className="space-y-4">
      {/* Identity + namespace selector */}
      <div className="flex flex-wrap items-end gap-3">
        <div className="flex-1 min-w-[180px]">
          <label className="block text-xs text-theme-text-tertiary mb-1">Identity</label>
          <div className="px-2 py-1.5 text-sm bg-theme-elevated rounded border border-theme-border text-theme-text-primary truncate">
            {authMe?.username || '(current kubeconfig user)'}
          </div>
        </div>
        <div className="flex-1 min-w-[180px]">
          <label className="block text-xs text-theme-text-tertiary mb-1">Namespace</label>
          <select
            value={namespace}
            onChange={(e) => { userPicked.current = true; setNamespace(e.target.value) }}
            className="w-full px-2 py-1.5 text-sm bg-theme-elevated border border-theme-border rounded text-theme-text-primary focus:outline-none focus:border-blue-500"
          >
            {namespaces?.map((ns) => (
              <option key={ns.name} value={ns.name}>{ns.name}</option>
            ))}
            {!namespaces?.some((ns) => ns.name === namespace) && (
              <option value={namespace}>{namespace}</option>
            )}
          </select>
        </div>
      </div>

      <p className="text-xs text-theme-text-tertiary">
        Computed by the Kubernetes API via{' '}
        <code className="inline-code">SelfSubjectRulesReview</code>.
        Shows what you can do in <span className="text-theme-text-secondary">{namespace}</span>,
        plus any cluster-scoped rules that apply everywhere.
      </p>

      {whoami?.incomplete && (
        <div className="px-2.5 py-1.5 text-xs bg-amber-500/10 border border-amber-500/20 rounded">
          The apiserver returned an incomplete rule set
          {whoami.evaluationError ? ` (${whoami.evaluationError})` : ''}.
          The list below may understate your actual permissions.
        </div>
      )}

      {isLoading ? (
        <div className="flex items-center gap-2 text-sm text-theme-text-tertiary">
          <Loader2 className="w-3.5 h-3.5 animate-spin" />
          Querying apiserver…
        </div>
      ) : error ? (
        <div className="text-sm text-red-400">
          Failed to load: {(error as Error).message}
        </div>
      ) : whoami ? (
        <PermissionsTable whoami={whoami} />
      ) : null}

      <RestrictedResources enabled={active} />
    </div>
  )
}

function PermissionsTable({ whoami }: { whoami: RBACWhoamiResponse }) {
  const resourceRules = whoami.resourceRules ?? []
  const nonResourceRules = whoami.nonResourceRules ?? []

  return (
    <div className="space-y-4">
      <div>
        <div className="text-xs font-medium text-theme-text-secondary uppercase tracking-wider mb-2">
          Resource permissions ({resourceRules.length})
        </div>
        {resourceRules.length === 0 ? (
          <div className="text-sm text-theme-text-tertiary">
            No resource permissions in this namespace.
          </div>
        ) : (
          <div className="space-y-2">
            {resourceRules.map((r, i) => (
              <ResourceRuleRow key={i} rule={r} />
            ))}
          </div>
        )}
      </div>

      {nonResourceRules.length > 0 && (
        <div>
          <div className="text-xs font-medium text-theme-text-secondary uppercase tracking-wider mb-2">
            Non-resource permissions ({nonResourceRules.length})
          </div>
          <div className="space-y-1 text-xs">
            {nonResourceRules.map((r, i) => (
              <div key={i} className="flex items-center gap-1 flex-wrap">
                {(r.verbs ?? []).map((v: string) => (
                  <span key={v} className={clsx('badge', rbacVerbBadgeClass(v))}>{v}</span>
                ))}
                <span className="text-theme-text-secondary">on</span>
                {(r.nonResourceURLs ?? []).map((u: string) => (
                  <span key={u} className={clsx('badge', rbacNonResourceUrlBadgeClass)}>{u}</span>
                ))}
              </div>
            ))}
          </div>
        </div>
      )}
    </div>
  )
}

function ResourceRuleRow({ rule }: { rule: { verbs?: string[]; apiGroups?: string[]; resources?: string[]; resourceNames?: string[] } }) {
  const verbs = rule.verbs ?? []
  const resources = rule.resources ?? []
  const groups = rule.apiGroups ?? []
  const resourceNames = rule.resourceNames ?? []
  return (
    <div className="card-inner text-xs flex items-center gap-1 flex-wrap">
      {verbs.map((v) => (
        <span key={v} className={clsx('badge', rbacVerbBadgeClass(v))}>{v}</span>
      ))}
      <span className="text-theme-text-secondary">on</span>
      {resources.length === 0 ? (
        <span className="text-theme-text-secondary italic">(no resources)</span>
      ) : (
        resources.map((r) => (
          <span key={r} className={clsx('badge', rbacResourceBadgeClass)}>
            {r === '*' ? '*' : r}
          </span>
        ))
      )}
      {groups.length > 0 && groups.some((g) => g !== '') && (
        <>
          <span className="text-theme-text-secondary">in</span>
          {groups.map((g) => (
            <span key={g} className={clsx('badge', rbacApiGroupBadgeClass)}>
              {g === '' ? 'core' : g}
            </span>
          ))}
        </>
      )}
      {resourceNames.length > 0 && (
        <>
          <span className="text-theme-text-secondary">named</span>
          {resourceNames.map((n) => (
            <span key={n} className={clsx('badge', rbacResourceNameBadgeClass)}>{n}</span>
          ))}
        </>
      )}
    </div>
  )
}

// displayKind strips the API group from a resource-counts key ("group/Kind" →
// "Kind"; core kinds have no prefix).
function displayKind(countKey: string): string {
  const i = countKey.indexOf('/')
  return i === -1 ? countKey : countKey.slice(i + 1)
}

// RestrictedResources surfaces the kinds Radar isn't showing the user (the
// resource-counts `forbidden` set) — the "what's hidden from me" half of access,
// alongside the SelfSubjectRulesReview rules above. That set mixes RBAC denials
// with not-installed/not-watched kinds, so the copy says "usually RBAC" rather
// than asserting a cause, and links to the docs that carry the unblock RBAC.
function RestrictedResources({ enabled }: { enabled: boolean }) {
  const { data } = useQuery<{ forbidden?: string[] }>({
    queryKey: ['resource-counts', 'your-access'],
    queryFn: () => fetchJSON('/resource-counts'),
    enabled,
    staleTime: 10000,
  })
  const forbidden = data?.forbidden ?? []
  if (forbidden.length === 0) return null

  return (
    <div>
      <div className="text-xs font-medium text-theme-text-secondary uppercase tracking-wider mb-2 flex items-center gap-1.5">
        <Lock className="w-3.5 h-3.5 text-amber-400" />
        Restricted or unavailable ({forbidden.length})
      </div>
      <p className="text-xs text-theme-text-tertiary mb-2">
        Resource types Radar isn't showing you — usually because your RBAC doesn't allow listing
        them, sometimes because the type isn't installed or watched on this cluster. Either way,
        not an empty cluster.
      </p>
      <div className="flex flex-wrap gap-1.5">
        {forbidden.map((k) => (
          <span
            key={k}
            className="inline-flex items-center px-2 py-0.5 text-xs rounded border border-theme-border bg-theme-elevated text-theme-text-secondary"
          >
            {displayKind(k)}
          </span>
        ))}
      </div>
      <a
        href="https://radarhq.io/docs/cloud/rbac"
        target="_blank"
        rel="noreferrer"
        className="inline-flex items-center gap-1 text-xs text-accent-text hover:underline mt-2"
      >
        How to get access
        <ExternalLink className="w-3 h-3" />
      </a>
    </div>
  )
}
