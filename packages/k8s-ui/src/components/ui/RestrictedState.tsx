import { useState } from 'react'
import { clsx } from 'clsx'
import { Shield, ChevronDown, Copy, Check } from 'lucide-react'

// RestrictedState is the shared "you can't see this because of Kubernetes RBAC"
// surface — distinct from EmptyState (which covers healthy / filtered / no-data).
// It never claims WHY the SAR failed (Radar can't prove tier vs custom-role vs
// disabled-value from a denied check); it states the fact and hands the operator
// something to forward to whoever administers their cluster.
//
// Reused across the resource list, topology, search, and the per-cluster access
// summary so the messaging can't drift.

interface Props {
  /** Display kind, e.g. "Node". */
  kindLabel: string
  /** API group for the kind ("" for core). Used to build the example RBAC. */
  group?: string
  /** Plural resource name, e.g. "nodes". When omitted the snippet uses a
   *  placeholder the admin fills in. */
  resource?: string
  /** Why the kind is hidden. "rbac_denied" (default): Radar can read it but the
   *  user's RBAC can't — show the grant request. "unavailable": Radar's
   *  ServiceAccount can't read it at all (not installed / SA RBAC / feature off)
   *  — a user grant won't help, so show a different message and no snippet. */
  reason?: 'rbac_denied' | 'unavailable' | string
  /** Tightens spacing for inline/embedded use (topology overlay, etc.). */
  compact?: boolean
  className?: string
}

function buildRbacRequest(group: string, resource: string): string {
  // resource is the placeholder when we don't have a confident API plural
  // (e.g. a CRD deep-link before discovery resolves the kind) — use a generic
  // role name rather than radar-read-<Kind>.
  const roleName = resource === '<resource>' ? 'radar-read-access' : `radar-read-${resource}`
  return [
    `apiVersion: rbac.authorization.k8s.io/v1`,
    `kind: ClusterRole`,
    `metadata:`,
    `  name: ${roleName}`,
    `rules:`,
    `  - apiGroups: ["${group}"]`,
    `    resources: ["${resource}"]`,
    `    verbs: ["get", "list", "watch"]`,
    `---`,
    `apiVersion: rbac.authorization.k8s.io/v1`,
    `kind: ClusterRoleBinding`,
    `metadata:`,
    `  name: ${roleName}`,
    `roleRef:`,
    `  apiGroup: rbac.authorization.k8s.io`,
    `  kind: ClusterRole`,
    `  name: ${roleName}`,
    `subjects:`,
    `  - kind: Group        # or User / ServiceAccount`,
    `    name: <your-identity>`,
    `    apiGroup: rbac.authorization.k8s.io`,
  ].join('\n')
}

export function RestrictedState({ kindLabel, group = '', resource, reason, compact, className }: Props) {
  const [expanded, setExpanded] = useState(false)
  const [copied, setCopied] = useState(false)
  const isUnavailable = reason === 'unavailable'

  // RBAC `resources` must be the lowercase API plural. selectedKind.name is
  // that in normal use, but a CRD deep-link can transiently carry the Kind
  // (e.g. "HTTPRoute") before discovery resolves it — emitting that would
  // produce a snippet that doesn't grant access. Only inline the resource when
  // it looks like a valid resource name; otherwise leave a clear placeholder.
  const validResource = resource && /^[a-z0-9.-]+$/.test(resource) ? resource : '<resource>'
  const snippet = buildRbacRequest(group, validResource)

  const copy = () => {
    navigator.clipboard.writeText(snippet).then(
      () => {
        setCopied(true)
        setTimeout(() => setCopied(false), 2000)
      },
      () => {},
    )
  }

  return (
    <div
      className={clsx(
        'flex flex-col items-center justify-center text-center text-theme-text-tertiary',
        compact ? 'p-4' : 'p-6',
        className,
      )}
    >
      <Shield className="w-8 h-8 text-amber-400 mb-2" />
      {isUnavailable ? (
        // Radar's ServiceAccount can't read this kind — a user grant won't help.
        <>
          <p className="text-theme-text-secondary font-medium">{kindLabel} isn't available here</p>
          <p className="text-sm mt-1 max-w-md">
            Radar can't read {kindLabel} resources in this cluster — the type may not be installed,
            or read access isn't granted to Radar's ServiceAccount (some kinds, like RBAC objects
            and Secrets, are off unless enabled in the Radar chart). Granting your own identity
            access won't surface it.
          </p>
        </>
      ) : (
        <>
          <p className="text-theme-text-secondary font-medium">You don't have access to {kindLabel}</p>
          <p className="text-sm mt-1 max-w-md">
            Your Kubernetes RBAC doesn't allow listing {kindLabel} resources in this cluster. This
            isn't an empty cluster — Radar is hiding what your identity can't read.
          </p>

          <div className="mt-3 w-full max-w-md">
            <button
              onClick={() => setExpanded((v) => !v)}
              className="flex items-center gap-1.5 mx-auto text-sm text-theme-text-secondary hover:text-theme-text-primary transition-colors"
            >
              <ChevronDown className={clsx('w-4 h-4 transition-transform', expanded && 'rotate-180')} />
              How to get access
            </button>

            {expanded && (
              <div className="mt-2 text-left">
                <p className="text-xs text-theme-text-tertiary mb-2">
                  Apply this, or send it to whoever administers your cluster, to grant your identity
                  read access.
                </p>
                <div className="rounded-md border border-theme-border bg-theme-base overflow-hidden">
                  <div className="flex items-center justify-between px-3 py-1.5 border-b border-theme-border bg-theme-elevated">
                    <span className="text-xs text-theme-text-tertiary">
                      ClusterRole + ClusterRoleBinding
                    </span>
                    <button
                      onClick={copy}
                      className="flex items-center gap-1 text-xs text-theme-text-secondary hover:text-theme-text-primary transition-colors"
                    >
                      {copied ? <Check className="w-3.5 h-3.5" /> : <Copy className="w-3.5 h-3.5" />}
                      {copied ? 'Copied' : 'Copy'}
                    </button>
                  </div>
                  <pre className="text-xs p-3 max-h-72 overflow-auto text-theme-text-secondary">
                    {snippet}
                  </pre>
                </div>
              </div>
            )}
          </div>
        </>
      )}
    </div>
  )
}
