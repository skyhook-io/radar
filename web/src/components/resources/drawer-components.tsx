import { useState } from 'react'
import { ChevronDown, ChevronRight, Copy, Check, Tag, AlertTriangle } from 'lucide-react'
import { clsx } from 'clsx'
import { formatAge } from './resource-utils'

// ============================================================================
// UI COMPONENTS
// ============================================================================

export function KeyValueBadge({ k, v }: { k: string; v: string }) {
  return (
    <span className="px-2 py-0.5 bg-theme-elevated rounded text-xs text-theme-text-secondary">
      {k}={v}
    </span>
  )
}

export function KeyValueBadgeList({ items }: { items: Record<string, unknown> | undefined | null }) {
  if (!items || Object.keys(items).length === 0) return null
  return (
    <div className="flex flex-wrap gap-1">
      {Object.entries(items).map(([k, v]) => (
        <KeyValueBadge key={k} k={k} v={String(v)} />
      ))}
    </div>
  )
}

interface SectionProps {
  title: string
  icon?: React.ComponentType<{ className?: string }>
  children: React.ReactNode
  defaultExpanded?: boolean
}

export function Section({ title, icon: Icon, children, defaultExpanded = true }: SectionProps) {
  const [expanded, setExpanded] = useState(defaultExpanded)

  return (
    <div className="border-b-subtle pb-4 last:border-0">
      <button
        onClick={() => setExpanded(!expanded)}
        className="flex items-center gap-2 w-full text-left mb-2 hover:text-theme-text-primary transition-colors"
      >
        {expanded ? <ChevronDown className="w-4 h-4 text-theme-text-tertiary" /> : <ChevronRight className="w-4 h-4 text-theme-text-tertiary" />}
        {Icon && <Icon className="w-4 h-4 text-theme-text-secondary" />}
        <span className="text-sm font-medium text-theme-text-secondary">{title}</span>
      </button>
      {expanded && <div className="pl-6">{children}</div>}
    </div>
  )
}

interface ExpandableSectionProps {
  title: string
  children: React.ReactNode
  defaultExpanded?: boolean
}

export function ExpandableSection({ title, children, defaultExpanded = true }: ExpandableSectionProps) {
  const [expanded, setExpanded] = useState(defaultExpanded)

  return (
    <div>
      <button
        onClick={() => setExpanded(!expanded)}
        className="flex items-center gap-2 text-sm text-theme-text-secondary hover:text-theme-text-primary transition-colors mb-1"
      >
        {expanded ? <ChevronDown className="w-3.5 h-3.5" /> : <ChevronRight className="w-3.5 h-3.5" />}
        {title}
      </button>
      {expanded && <div className="ml-5">{children}</div>}
    </div>
  )
}

export function PropertyList({ children }: { children: React.ReactNode }) {
  return <div className="space-y-2">{children}</div>
}

interface PropertyProps {
  label: React.ReactNode
  value: React.ReactNode
  copyable?: boolean
  onCopy?: (text: string, key: string) => void
  copied?: string | null
}

// Helper to check if value is a React element
function isReactElement(value: unknown): value is React.ReactElement {
  return value !== null && typeof value === 'object' && '$$typeof' in (value as object)
}

export function Property({ label, value, copyable, onCopy, copied }: PropertyProps) {
  if (value === undefined || value === null || value === '') return null
  const labelKey = typeof label === 'string' ? label : 'value'

  // If value is a React element, render it directly; otherwise convert to string
  const displayValue = isReactElement(value) ? value : String(value)
  const strValue = isReactElement(value) ? '' : String(value)

  return (
    <div className="flex items-start gap-2 text-sm">
      <span className="text-theme-text-tertiary w-28 shrink-0">{label}</span>
      <span className="text-theme-text-primary break-all flex-1">{displayValue}</span>
      {copyable && onCopy && !isReactElement(value) && (
        <button
          onClick={() => onCopy(strValue, labelKey)}
          className="p-0.5 text-theme-text-tertiary hover:text-theme-text-primary shrink-0"
        >
          {copied === label ? <Check className="w-3 h-3 text-green-400" /> : <Copy className="w-3 h-3" />}
        </button>
      )}
    </div>
  )
}

// ============================================================================
// COMMON SECTIONS
// ============================================================================

export function ConditionsSection({ conditions }: { conditions?: any[] }) {
  if (!conditions || conditions.length === 0) return null

  return (
    <Section title={`Conditions (${conditions.length})`} defaultExpanded={conditions.length <= 4}>
      <div className="space-y-2">
        {conditions.map((cond: any) => (
          <div key={cond.type} className="flex items-start gap-2 text-sm">
            <span className={clsx(
              'w-4 h-4 rounded-full flex items-center justify-center text-xs shrink-0 mt-0.5',
              cond.status === 'True' ? 'bg-green-500/20 text-green-400' : 'bg-red-500/20 text-red-400'
            )}>
              {cond.status === 'True' ? '✓' : '✗'}
            </span>
            <div>
              <div className="text-theme-text-primary">{cond.type}</div>
              {cond.message && <div className="text-xs text-theme-text-tertiary">{cond.message}</div>}
            </div>
          </div>
        ))}
      </div>
    </Section>
  )
}

/** Problem type for ProblemAlerts component */
export interface Problem {
  color: 'red' | 'yellow'
  message: string
}

/** Displays a list of problem alerts (warnings and errors) for GitOps resources */
export function ProblemAlerts({ problems }: { problems: Problem[] }) {
  if (problems.length === 0) return null

  return (
    <>
      {problems.map((problem, i) => (
        <div
          key={i}
          className={clsx(
            'mb-4 p-3 border rounded-lg',
            problem.color === 'red'
              ? 'bg-red-500/10 border-red-500/30'
              : 'bg-yellow-500/10 border-yellow-500/30'
          )}
        >
          <div className="flex items-start gap-2">
            <AlertTriangle
              className={clsx(
                'w-4 h-4 mt-0.5 shrink-0',
                problem.color === 'red' ? 'text-red-400' : 'text-yellow-400'
              )}
            />
            <div className="flex-1 min-w-0">
              <div
                className={clsx(
                  'text-sm font-medium',
                  problem.color === 'red' ? 'text-red-400' : 'text-yellow-400'
                )}
              >
                {problem.color === 'red' ? 'Issue Detected' : 'Warning'}
              </div>
              <div
                className={clsx(
                  'text-xs mt-1',
                  problem.color === 'red' ? 'text-red-300/80' : 'text-yellow-300/80'
                )}
              >
                {problem.message}
              </div>
            </div>
          </div>
        </div>
      ))}
    </>
  )
}

export function LabelsSection({ data }: { data: any }) {
  const labels = data.metadata?.labels
  if (!labels || Object.keys(labels).length === 0) return null
  const count = Object.keys(labels).length

  return (
    <Section title={`Labels (${count})`} icon={Tag} defaultExpanded={count <= 5}>
      <KeyValueBadgeList items={labels} />
    </Section>
  )
}

export function AnnotationsSection({ data }: { data: any }) {
  const annotations = data.metadata?.annotations
  if (!annotations || Object.keys(annotations).length === 0) return null
  const count = Object.keys(annotations).length

  return (
    <Section title={`Annotations (${count})`} defaultExpanded={count <= 3}>
      <div className="space-y-1 max-h-48 overflow-y-auto">
        {Object.entries(annotations).map(([k, v]) => (
          <div key={k} className="text-xs">
            <span className="text-theme-text-tertiary">{k}:</span>
            <span className="text-theme-text-secondary ml-1 break-all">{v as string}</span>
          </div>
        ))}
      </div>
    </Section>
  )
}

export function MetadataSection({ data }: { data: any }) {
  const meta = data.metadata
  if (!meta) return null

  return (
    <Section title="Metadata" defaultExpanded>
      <PropertyList>
        <Property label="UID" value={meta.uid} />
        <Property label="Resource Version" value={meta.resourceVersion} />
        <Property label="Generation" value={meta.generation} />
        <Property label="Created" value={meta.creationTimestamp ? formatAge(meta.creationTimestamp) : '-'} />
      </PropertyList>
    </Section>
  )
}

export function PodTemplateSection({ template }: { template: any }) {
  if (!template) return null
  const containers = template.spec?.containers || []

  return (
    <div className="space-y-2">
      {containers.map((c: any) => (
        <div key={c.name} className="bg-theme-elevated/30 rounded p-2 text-sm">
          <div className="font-medium text-theme-text-primary">{c.name}</div>
          <div className="text-xs text-theme-text-secondary truncate" title={c.image}>{c.image}</div>
          {c.ports && (
            <div className="text-xs text-theme-text-tertiary mt-1">
              Ports: {c.ports.map((p: any) => `${p.containerPort}/${p.protocol || 'TCP'}`).join(', ')}
            </div>
          )}
        </div>
      ))}
    </div>
  )
}

// ============================================================================
// HELPERS
// ============================================================================

export function getKindColor(kind: string): string {
  const k = kind.toLowerCase()
  // Use darker text for light mode contrast, brighter for dark mode
  if (k.includes('pod')) return 'bg-lime-500/20 text-lime-700 dark:text-lime-300 border-lime-500/30'
  if (k.includes('deployment')) return 'bg-emerald-500/20 text-emerald-700 dark:text-emerald-300 border-emerald-500/30'
  if (k.includes('service')) return 'bg-blue-500/20 text-blue-700 dark:text-blue-300 border-blue-500/30'
  if (k.includes('gateway')) return 'bg-violet-500/20 text-violet-700 dark:text-violet-300 border-violet-500/30'
  if (k.includes('route')) return 'bg-purple-500/20 text-purple-700 dark:text-purple-300 border-purple-500/30'
  if (k.includes('ingress')) return 'bg-violet-500/20 text-violet-700 dark:text-violet-300 border-violet-500/30'
  if (k.includes('configmap')) return 'bg-amber-500/20 text-amber-700 dark:text-amber-300 border-amber-500/30'
  if (k.includes('secret')) return 'bg-red-500/20 text-red-700 dark:text-red-300 border-red-500/30'
  if (k.includes('daemonset')) return 'bg-teal-500/20 text-teal-700 dark:text-teal-300 border-teal-500/30'
  if (k.includes('statefulset')) return 'bg-cyan-500/20 text-cyan-700 dark:text-cyan-300 border-cyan-500/30'
  if (k.includes('replicaset')) return 'bg-green-500/20 text-green-700 dark:text-green-300 border-green-500/30'
  if (k.includes('hpa') || k.includes('horizontalpodautoscaler')) return 'bg-pink-500/20 text-pink-700 dark:text-pink-300 border-pink-500/30'
  if (k.includes('cronjob')) return 'bg-orange-500/20 text-orange-700 dark:text-orange-300 border-orange-500/30'
  if (k.includes('job')) return 'bg-orange-500/20 text-orange-700 dark:text-orange-300 border-orange-500/30'
  if (k.includes('node')) return 'bg-gray-500/20 text-gray-700 dark:text-gray-300 border-gray-500/30'
  if (k.includes('namespace')) return 'bg-blue-500/20 text-blue-700 dark:text-blue-300 border-blue-500/30'
  if (k.includes('persistentvolume')) return 'bg-purple-500/20 text-purple-700 dark:text-purple-300 border-purple-500/30'
  // Default color for CRDs
  return 'bg-fuchsia-500/20 text-fuchsia-700 dark:text-fuchsia-300 border-fuchsia-500/30'
}

export function formatKindName(kind: string): string {
  const k = kind.toLowerCase()
  const names: Record<string, string> = {
    pods: 'Pod', deployments: 'Deployment', daemonsets: 'DaemonSet', statefulsets: 'StatefulSet',
    replicasets: 'ReplicaSet', services: 'Service', ingresses: 'Ingress',
    gateways: 'Gateway', httproutes: 'HTTPRoute', grpcroutes: 'GRPCRoute',
    tcproutes: 'TCPRoute', tlsroutes: 'TLSRoute', configmaps: 'ConfigMap',
    secrets: 'Secret', jobs: 'Job', cronjobs: 'CronJob', hpas: 'HPA',
    horizontalpodautoscalers: 'HPA', nodes: 'Node', namespaces: 'Namespace',
    persistentvolumeclaims: 'PVC', persistentvolumes: 'PV',
  }
  if (names[k]) return names[k]

  // For unknown kinds (CRDs), use the original kind name
  // or format it nicely if it's a plural name
  if (kind.endsWith('s') && !kind.endsWith('ss')) {
    // Try to singularize simple plurals
    const singular = kind.slice(0, -1)
    // Capitalize first letter
    return singular.charAt(0).toUpperCase() + singular.slice(1)
  }
  return kind
}

// Type for copy handler
export type CopyHandler = (text: string, key: string) => void

// ============================================================================
// RELATED RESOURCES SECTION
// ============================================================================

import type { TimelineEvent, Relationships, ResourceRef } from '../../types'
import { isChangeEvent, isK8sEvent } from '../../types'
import { Link } from 'lucide-react'

interface RelatedResourcesSectionProps {
  relationships: Relationships | undefined
  onNavigate?: (ref: ResourceRef) => void
}

export function RelatedResourcesSection({ relationships, onNavigate }: RelatedResourcesSectionProps) {
  if (!relationships) return null

  const hasRelationships =
    relationships.owner ||
    (relationships.children && relationships.children.length > 0) ||
    (relationships.services && relationships.services.length > 0) ||
    (relationships.ingresses && relationships.ingresses.length > 0) ||
    (relationships.gateways && relationships.gateways.length > 0) ||
    (relationships.routes && relationships.routes.length > 0) ||
    (relationships.pods && relationships.pods.length > 0) ||
    (relationships.configRefs && relationships.configRefs.length > 0) ||
    relationships.hpa ||
    relationships.scaleTarget

  if (!hasRelationships) return null

  return (
    <Section title="Related Resources" icon={Link} defaultExpanded>
      <div className="space-y-3">
        {/* Owner (parent resource) */}
        {relationships.owner && (
          <RelationshipGroup label="Owner" refs={[relationships.owner]} onNavigate={onNavigate} />
        )}

        {/* Children (managed resources) */}
        {relationships.children && relationships.children.length > 0 && (
          <RelationshipGroup label="Children" refs={relationships.children} onNavigate={onNavigate} />
        )}

        {/* Services exposing this resource */}
        {relationships.services && relationships.services.length > 0 && (
          <RelationshipGroup label="Services" refs={relationships.services} onNavigate={onNavigate} />
        )}

        {/* Ingresses routing to this resource */}
        {relationships.ingresses && relationships.ingresses.length > 0 && (
          <RelationshipGroup label="Ingresses" refs={relationships.ingresses} onNavigate={onNavigate} />
        )}

        {/* Gateways routing to this resource */}
        {relationships.gateways && relationships.gateways.length > 0 && (
          <RelationshipGroup label="Gateways" refs={relationships.gateways} onNavigate={onNavigate} />
        )}

        {/* Routes attached to this Gateway */}
        {relationships.routes && relationships.routes.length > 0 && (
          <RelationshipGroup label="Routes" refs={relationships.routes} onNavigate={onNavigate} />
        )}

        {/* Pods selected/exposed by this Service */}
        {relationships.pods && relationships.pods.length > 0 && (
          <RelationshipGroup label="Pods" refs={relationships.pods} onNavigate={onNavigate} />
        )}

        {/* ConfigMaps/Secrets used */}
        {relationships.configRefs && relationships.configRefs.length > 0 && (
          <RelationshipGroup label="Configuration" refs={relationships.configRefs} onNavigate={onNavigate} />
        )}

        {/* HPA scaling this workload */}
        {relationships.hpa && (
          <RelationshipGroup label="Autoscaler" refs={[relationships.hpa]} onNavigate={onNavigate} />
        )}

        {/* What this HPA scales */}
        {relationships.scaleTarget && (
          <RelationshipGroup label="Scale Target" refs={[relationships.scaleTarget]} onNavigate={onNavigate} />
        )}
      </div>
    </Section>
  )
}

interface RelationshipGroupProps {
  label: string
  refs: ResourceRef[]
  onNavigate?: (ref: ResourceRef) => void
}

function RelationshipGroup({ label, refs, onNavigate }: RelationshipGroupProps) {
  if (!refs || refs.length === 0) return null

  return (
    <div>
      <div className="text-xs text-theme-text-tertiary mb-1">{label}</div>
      <div className="flex flex-wrap gap-1">
        {refs.map((resourceRef, i) => (
          <ResourceRefBadge key={`${resourceRef.kind}-${resourceRef.namespace}-${resourceRef.name}-${i}`} resourceRef={resourceRef} onClick={onNavigate} />
        ))}
      </div>
    </div>
  )
}

export interface ResourceRefBadgeProps {
  resourceRef: ResourceRef
  onClick?: (ref: ResourceRef) => void
}

/** Reusable chip/badge for showing a related resource with click-to-navigate */
export function ResourceRefBadge({ resourceRef, onClick }: ResourceRefBadgeProps) {
  const kindClass = getKindColor(resourceRef.kind)
  const kindName = formatKindForRef(resourceRef.kind)

  if (onClick) {
    return (
      <button
        onClick={() => onClick(resourceRef)}
        className={clsx(
          'px-2 py-0.5 text-xs rounded border transition-colors hover:brightness-125 cursor-pointer',
          kindClass
        )}
        title={`${resourceRef.kind}: ${resourceRef.namespace}/${resourceRef.name}`}
      >
        <span className="opacity-60">{kindName}/</span>
        {resourceRef.name}
      </button>
    )
  }

  return (
    <span
      className={clsx('px-2 py-0.5 text-xs rounded border', kindClass)}
      title={`${resourceRef.kind}: ${resourceRef.namespace}/${resourceRef.name}`}
    >
      <span className="opacity-60">{kindName}/</span>
      {resourceRef.name}
    </span>
  )
}

function formatKindForRef(kind: string): string {
  const k = kind.toLowerCase()
  const shortNames: Record<string, string> = {
    deployment: 'deploy',
    daemonset: 'ds',
    statefulset: 'sts',
    replicaset: 'rs',
    configmap: 'cm',
    service: 'svc',
    ingress: 'ing',
    gateway: 'gw',
    httproute: 'hr',
    grpcroute: 'grpc',
    tcproute: 'tcp',
    tlsroute: 'tls',
    secret: 'secret',
    pod: 'pod',
    job: 'job',
    cronjob: 'cj',
    hpa: 'hpa',
  }
  return shortNames[k] || k
}

// ============================================================================
// EVENTS SECTION
// ============================================================================

interface EventsSectionProps {
  events: TimelineEvent[]
  isLoading?: boolean
}

export function EventsSection({ events, isLoading }: EventsSectionProps) {
  if (isLoading) {
    return (
      <Section title="Recent Events" defaultExpanded>
        <div className="text-sm text-theme-text-tertiary">Loading events...</div>
      </Section>
    )
  }

  if (!events || events.length === 0) {
    return (
      <Section title="Recent Events" defaultExpanded={false}>
        <div className="text-sm text-theme-text-tertiary">No recent events</div>
      </Section>
    )
  }

  return (
    <Section title={`Recent Events (${events.length})`} defaultExpanded>
      <div className="space-y-2 max-h-64 overflow-y-auto">
        {events.map((event, i) => (
          <div
            key={`${event.id}-${i}`}
            className={clsx(
              'p-2 rounded text-sm border-l-2',
              event.eventType === 'Warning' || (isChangeEvent(event) && event.eventType === 'delete')
                ? 'bg-red-500/10 border-red-500'
                : isK8sEvent(event)
                ? 'bg-blue-500/10 border-blue-500'
                : 'bg-theme-elevated/30 border-theme-border'
            )}
          >
            <div className="flex items-center justify-between gap-2">
              <span className="font-medium text-theme-text-primary">
                {isK8sEvent(event) ? event.reason : event.eventType}
              </span>
              <span className="text-xs text-theme-text-tertiary">
                {formatEventTime(event.timestamp)}
              </span>
            </div>
            {event.message && (
              <div className="text-xs text-theme-text-secondary mt-1 line-clamp-2">
                {event.message}
              </div>
            )}
            {isChangeEvent(event) && event.diff?.summary && (
              <div className="text-xs text-theme-text-secondary mt-1">
                {event.diff.summary}
              </div>
            )}
          </div>
        ))}
      </div>
    </Section>
  )
}

function formatEventTime(timestamp: string): string {
  const date = new Date(timestamp)
  const now = new Date()
  const diffMs = now.getTime() - date.getTime()
  const diffMins = Math.floor(diffMs / 60000)
  const diffHours = Math.floor(diffMins / 60)

  if (diffMins < 1) return 'just now'
  if (diffMins < 60) return `${diffMins}m ago`
  if (diffHours < 24) return `${diffHours}h ago`
  return date.toLocaleDateString()
}
