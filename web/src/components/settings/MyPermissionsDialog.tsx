import { useState, useEffect, useRef } from 'react'
import { createPortal } from 'react-dom'
import { Shield, X, Loader2 } from 'lucide-react'
import { clsx } from 'clsx'
import {
  rbacVerbBadgeClass,
  rbacResourceBadgeClass,
  rbacApiGroupBadgeClass,
  rbacResourceNameBadgeClass,
  rbacNonResourceUrlBadgeClass,
} from '@skyhook-io/k8s-ui'
import { useAnimatedUnmount } from '../../hooks/useAnimatedUnmount'
import { TRANSITION_BACKDROP, TRANSITION_PANEL } from '../../utils/animation'
import { useNamespaces, useAuthMe } from '../../api/client'
import { useRBACWhoami } from '../../api/rbac'

interface MyPermissionsDialogProps {
  open: boolean
  onClose: () => void
}

export function MyPermissionsDialog({ open, onClose }: MyPermissionsDialogProps) {
  const dialogRef = useRef<HTMLDivElement>(null)
  const { shouldRender, isOpen } = useAnimatedUnmount(open, 200)
  const [namespace, setNamespace] = useState('default')

  const { data: authMe } = useAuthMe()
  const { data: namespaces } = useNamespaces()
  const { data: whoami, isLoading, error } = useRBACWhoami(namespace, open)

  // ESC + focus
  useEffect(() => {
    if (!open) return
    const handler = (e: KeyboardEvent) => {
      if (e.key === 'Escape') {
        e.stopPropagation()
        onClose()
      }
    }
    document.addEventListener('keydown', handler, true)
    return () => document.removeEventListener('keydown', handler, true)
  }, [open, onClose])
  useEffect(() => {
    if (open && dialogRef.current) dialogRef.current.focus()
  }, [open])

  if (!shouldRender) return null

  return createPortal(
    <div className="fixed inset-0 z-50 flex items-center justify-center">
      <div
        className={clsx(
          'absolute inset-0 bg-black/60 backdrop-blur-sm',
          TRANSITION_BACKDROP,
          isOpen ? 'opacity-100' : 'opacity-0',
        )}
        onClick={onClose}
      />
      <div
        ref={dialogRef}
        tabIndex={-1}
        className={clsx(
          'relative bg-theme-surface border border-theme-border shadow-theme-lg w-full outline-none flex flex-col',
          'max-sm:inset-0 max-sm:absolute max-sm:rounded-none max-sm:max-h-full max-sm:border-0',
          'sm:rounded-xl sm:max-w-3xl sm:mx-4 sm:max-h-[85vh]',
          TRANSITION_PANEL,
          isOpen ? 'opacity-100 scale-100' : 'opacity-0 scale-95',
        )}
      >
        {/* Header */}
        <div className="flex items-center justify-between p-4 border-b border-theme-border shrink-0">
          <div className="flex items-center gap-2">
            <Shield className="w-5 h-5 text-theme-text-secondary" />
            <h2 className="text-lg font-semibold text-theme-text-primary">My Permissions</h2>
          </div>
          <button
            onClick={onClose}
            className="p-1 text-theme-text-secondary hover:text-theme-text-primary hover:bg-theme-elevated rounded"
          >
            <X className="w-5 h-5" />
          </button>
        </div>

        <div className="overflow-y-auto p-4 flex-1 space-y-4">
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
                onChange={(e) => setNamespace(e.target.value)}
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
        </div>
      </div>
    </div>,
    document.body,
  )
}

function PermissionsTable({ whoami }: { whoami: { resourceRules: any[]; nonResourceRules: any[] } }) {
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
