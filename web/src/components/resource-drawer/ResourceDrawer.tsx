import { memo } from 'react'
import { X, ExternalLink, Copy, Check } from 'lucide-react'
import { clsx } from 'clsx'
import { useState, useCallback } from 'react'
import type { TopologyNode, NodeKind, HealthStatus } from '../../types'
import { getKindBadgeBordered, healthToSeverity, SEVERITY_BADGE_BORDERED } from '../../utils/badge-colors'

interface ResourceDrawerProps {
  node: TopologyNode
  onClose: () => void
}

// Status badge colors using centralized severity
function getStatusBadge(status: HealthStatus) {
  const severity = healthToSeverity(status)
  return SEVERITY_BADGE_BORDERED[severity]
}

// Format data value for display
function formatValue(value: unknown): string {
  if (value === null || value === undefined) return '-'
  if (typeof value === 'boolean') return value ? 'Yes' : 'No'
  if (typeof value === 'object') return JSON.stringify(value, null, 2)
  return String(value)
}

// Get display fields for each kind
function getDisplayFields(kind: NodeKind, data: Record<string, unknown>): Array<[string, unknown]> {
  const common: Array<[string, unknown]> = data.namespace
    ? [['Namespace', data.namespace]]
    : []

  switch (kind) {
    case 'Pod':
      return [
        ...common,
        ['Phase', data.phase],
        ['Restarts', data.restarts],
        ['Containers', data.containers],
        ['Node', data.nodeName],
      ]
    case 'Deployment':
    case 'DaemonSet':
    case 'StatefulSet':
      return [
        ...common,
        ['Ready', `${data.readyReplicas ?? 0}/${data.totalReplicas ?? 0}`],
        ['Strategy', data.strategy],
      ]
    case 'ReplicaSet':
      return [
        ...common,
        ['Ready', `${data.readyReplicas ?? 0}/${data.totalReplicas ?? 0}`],
      ]
    case 'Service':
      return [
        ...common,
        ['Type', data.type],
        ['Cluster IP', data.clusterIP],
        ['Port', data.port],
      ]
    case 'Ingress':
      return [
        ...common,
        ['Hostname', data.hostname],
        ['TLS', data.tls],
      ]
    case 'Gateway':
      return [
        ...common,
        ['Listeners', data.listenerCount],
        ['Addresses', Array.isArray(data.addresses) ? (data.addresses as string[]).join(', ') : '-'],
      ]
    case 'HTTPRoute':
    case 'GRPCRoute':
    case 'TCPRoute':
    case 'TLSRoute':
      return [
        ...common,
        ['Hostnames', Array.isArray(data.hostnames) ? (data.hostnames as string[]).join(', ') : '-'],
        ['Rules', data.rulesCount],
      ]
    case 'ConfigMap':
      return [
        ...common,
        ['Keys', data.keys],
      ]
    case 'HorizontalPodAutoscaler':
      return [
        ...common,
        ['Min Replicas', data.minReplicas],
        ['Max Replicas', data.maxReplicas],
        ['Current', data.current],
      ]
    default:
      return common
  }
}

export const ResourceDrawer = memo(function ResourceDrawer({
  node,
  onClose,
}: ResourceDrawerProps) {
  const [copied, setCopied] = useState(false)

  const copyName = useCallback(() => {
    navigator.clipboard.writeText(node.name)
    setCopied(true)
    setTimeout(() => setCopied(false), 2000)
  }, [node.name])

  const fields = getDisplayFields(node.kind, node.data)

  return (
    <>
      {/* Backdrop */}
      <div
        className="fixed inset-0 bg-black/50 z-40"
        onClick={onClose}
      />

      {/* Drawer */}
      <div className="fixed right-0 top-0 bottom-0 w-96 bg-theme-surface border-l border-theme-border z-50 flex flex-col shadow-2xl">
        {/* Header */}
        <div className="flex items-start justify-between p-4 border-b border-theme-border">
          <div className="flex-1 min-w-0">
            <div className="flex items-center gap-2 mb-2">
              <span
                className={clsx(
                  'px-2 py-0.5 text-xs font-medium rounded',
                  getKindBadgeBordered(node.kind)
                )}
              >
                {node.kind}
              </span>
              <span
                className={clsx(
                  'px-2 py-0.5 text-xs font-medium rounded border',
                  getStatusBadge(node.status)
                )}
              >
                {node.status}
              </span>
            </div>
            <div className="flex items-center gap-2">
              <h2 className="text-lg font-semibold text-theme-text-primary truncate">
                {node.name}
              </h2>
              <button
                onClick={copyName}
                className="p-1 text-theme-text-secondary hover:text-theme-text-primary hover:bg-theme-elevated rounded"
                title="Copy name"
              >
                {copied ? (
                  <Check className="w-4 h-4 text-green-400" />
                ) : (
                  <Copy className="w-4 h-4" />
                )}
              </button>
            </div>
          </div>
          <button
            onClick={onClose}
            className="p-2 text-theme-text-secondary hover:text-theme-text-primary hover:bg-theme-elevated rounded"
          >
            <X className="w-5 h-5" />
          </button>
        </div>

        {/* Content */}
        <div className="flex-1 overflow-y-auto p-4">
          {/* Details */}
          <section className="mb-6">
            <h3 className="text-sm font-medium text-theme-text-secondary uppercase tracking-wide mb-3">
              Details
            </h3>
            <div className="space-y-3">
              {fields.map(([label, value]) => (
                <div key={label as string}>
                  <dt className="text-xs text-theme-text-tertiary mb-0.5">{label as string}</dt>
                  <dd className="text-sm text-theme-text-primary">{formatValue(value)}</dd>
                </div>
              ))}
            </div>
          </section>

          {/* Raw data */}
          <section>
            <h3 className="text-sm font-medium text-theme-text-secondary uppercase tracking-wide mb-3">
              Raw Data
            </h3>
            <pre className="bg-theme-base rounded-lg p-3 text-xs text-theme-text-secondary overflow-x-auto">
              {JSON.stringify(node.data, null, 2)}
            </pre>
          </section>
        </div>

        {/* Footer */}
        <div className="p-4 border-t border-theme-border">
          <button
            onClick={() => {
              // TODO: Open kubectl command or link to dashboard
              console.log('View in dashboard:', node)
            }}
            className="w-full flex items-center justify-center gap-2 px-4 py-2 bg-blue-500 hover:bg-blue-600 text-theme-text-primary rounded-lg transition-colors"
          >
            <ExternalLink className="w-4 h-4" />
            View Full Resource
          </button>
        </div>
      </div>
    </>
  )
})
