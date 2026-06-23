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
  /** Tightens spacing for inline/embedded use (topology overlay, etc.). */
  compact?: boolean
  className?: string
}

function buildRbacRequest(kindLabel: string, group: string, resource: string): string {
  const roleName = `radar-read-${resource}`
  return [
    `I need read access to ${kindLabel} in this Kubernetes cluster via Radar.`,
    `Please grant my identity (user, group, or ServiceAccount) get/list/watch on "${resource}".`,
    ``,
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

export function RestrictedState({ kindLabel, group = '', resource, compact, className }: Props) {
  const [expanded, setExpanded] = useState(false)
  const [copied, setCopied] = useState(false)

  const snippet = buildRbacRequest(kindLabel, group, resource || '<resource>')

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
      <p className="text-theme-text-secondary font-medium">You don't have access to {kindLabel}</p>
      <p className="text-sm mt-1 max-w-md">
        Your Kubernetes RBAC doesn't allow listing {kindLabel} in this cluster. This isn't an empty
        cluster — Radar is hiding what your identity can't read.
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
              Send this to whoever administers your cluster (the person who manages your Radar
              install or your cluster RBAC) — it asks them to grant your identity read access.
            </p>
            <div className="relative">
              <pre className="text-xs bg-theme-base border border-theme-border rounded-md p-3 overflow-x-auto text-theme-text-secondary">
                {snippet}
              </pre>
              <button
                onClick={copy}
                className="absolute top-2 right-2 flex items-center gap-1 text-xs px-2 py-1 rounded bg-theme-elevated hover:bg-theme-border text-theme-text-secondary hover:text-theme-text-primary transition-colors"
              >
                {copied ? <Check className="w-3.5 h-3.5" /> : <Copy className="w-3.5 h-3.5" />}
                {copied ? 'Copied' : 'Copy request'}
              </button>
            </div>
          </div>
        )}
      </div>
    </div>
  )
}
