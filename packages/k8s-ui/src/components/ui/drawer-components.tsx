import { useState } from 'react'
import { ChevronRight, Copy, Check, Tag, AlertTriangle, CheckCircle, ExternalLink, Layers } from 'lucide-react'
import { clsx } from 'clsx'
import { formatAge } from '../resources/resource-utils'
import { Tooltip } from './Tooltip'

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
        <ChevronRight className={clsx('w-4 h-4 text-theme-text-tertiary transition-transform duration-200', expanded && 'rotate-90')} />
        {Icon && <Icon className="w-4 h-4 text-theme-text-secondary" />}
        <span className="text-sm font-medium text-theme-text-secondary">{title}</span>
      </button>
      <div
        className="grid transition-[grid-template-rows] duration-200 ease-out"
        style={{ gridTemplateRows: expanded ? '1fr' : '0fr' }}
      >
        <div className="overflow-hidden">
          <div className="pl-6">{children}</div>
        </div>
      </div>
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
        <ChevronRight className={clsx('w-3.5 h-3.5 transition-transform duration-200', expanded && 'rotate-90')} />
        {title}
      </button>
      <div
        className="grid transition-[grid-template-rows] duration-200 ease-out"
        style={{ gridTemplateRows: expanded ? '1fr' : '0fr' }}
      >
        <div className="overflow-hidden">
          <div className="ml-5">{children}</div>
        </div>
      </div>
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

// ============================================================================
// ALERT BANNER
// ============================================================================

const ALERT_COLORS = {
  error:   { bg: 'bg-red-500/10', border: 'border-red-500/30', title: 'text-red-700 dark:text-red-400', message: 'text-red-600/80 dark:text-red-400/80', list: 'text-red-600 dark:text-red-400', bullet: 'text-red-500/60 dark:text-red-400/60' },
  warning: { bg: 'bg-yellow-500/10', border: 'border-yellow-500/30', title: 'text-yellow-700 dark:text-yellow-400', message: 'text-yellow-600/80 dark:text-yellow-400/80', list: 'text-yellow-600 dark:text-yellow-400', bullet: 'text-yellow-500/60 dark:text-yellow-400/60' },
  info:    { bg: 'bg-blue-500/10', border: 'border-blue-500/30', title: 'text-blue-700 dark:text-blue-400', message: 'text-blue-600/80 dark:text-blue-400/80', list: 'text-blue-600 dark:text-blue-400', bullet: 'text-blue-500/60 dark:text-blue-400/60' },
  success: { bg: 'bg-green-500/10', border: 'border-green-500/30', title: 'text-green-700 dark:text-green-400', message: 'text-green-600/80 dark:text-green-400/80', list: 'text-green-600 dark:text-green-400', bullet: 'text-green-500/60 dark:text-green-400/60' },
} as const

const DEFAULT_ICONS: Record<string, React.ComponentType<{ className?: string }>> = {
  error: AlertTriangle,
  warning: AlertTriangle,
  info: AlertTriangle,
  success: CheckCircle,
}

interface AlertBannerProps {
  variant: 'error' | 'warning' | 'info' | 'success'
  icon?: React.ComponentType<{ className?: string }>
  title: string
  message?: React.ReactNode
  items?: string[]
  children?: React.ReactNode
}

export function AlertBanner({ variant, icon, title, message, items, children }: AlertBannerProps) {
  const colors = ALERT_COLORS[variant]
  const Icon = icon || DEFAULT_ICONS[variant]
  const hasBody = message || items || children

  return (
    <div className={clsx('mb-4 p-3 border rounded-lg', colors.bg, colors.border)}>
      <div className={clsx('flex gap-2', hasBody ? 'items-start' : 'items-center')}>
        <Icon className={clsx('w-4 h-4 shrink-0', colors.title, hasBody && 'mt-0.5')} />
        {hasBody ? (
          <div className="flex-1 min-w-0">
            <div className={clsx('text-sm font-medium', colors.title, items && 'mb-1')}>{title}</div>
            {message && <div className={clsx('text-xs mt-1 break-all', colors.message)}>{message}</div>}
            {items && items.length > 0 && (
              <ul className={clsx('text-xs space-y-1', colors.list)}>
                {items.map((item, i) => (
                  <li key={i} className="flex items-start gap-1.5">
                    <span className={clsx(colors.bullet, 'mt-0.5')}>•</span>
                    <span>{item}</span>
                  </li>
                ))}
              </ul>
            )}
            {children}
          </div>
        ) : (
          <div className={clsx('text-sm font-medium', colors.title)}>{title}</div>
        )}
      </div>
    </div>
  )
}

/** KNative "Not Ready" alert banner — shared across all KNative renderers */
export function KnativeNotReadyBanner({ status, data, resourceType }: { status: { level: string }; data: any; resourceType: string }) {
  if (status.level !== 'unhealthy') return null
  const message = (data.status?.conditions || []).find((c: any) => c.type === 'Ready')?.message
    || `This ${resourceType} is not in a ready state.`
  return <AlertBanner variant="error" title={`${resourceType} Not Ready`} message={message} />
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
        <AlertBanner
          key={i}
          variant={problem.color === 'red' ? 'error' : 'warning'}
          title={problem.color === 'red' ? 'Issue Detected' : 'Warning'}
          message={problem.message}
        />
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
        <Property label="Created" value={meta.creationTimestamp ? (
          <Tooltip content={new Date(meta.creationTimestamp).toLocaleString()}>
            <span className="border-b border-dotted border-theme-text-tertiary cursor-help">{formatAge(meta.creationTimestamp)}</span>
          </Tooltip>
        ) : '-'} />
      </PropertyList>
    </Section>
  )
}

export function PodTemplateSection({ template }: { template: any }) {
  if (!template) return null
  const initContainers = template.spec?.initContainers || []
  const containers = template.spec?.containers || []

  return (
    <div className="space-y-2">
      {initContainers.length > 0 && (
        <>
          <div className="text-xs text-theme-text-tertiary font-medium uppercase tracking-wide">Init Containers</div>
          {initContainers.map((c: any) => (
            <div key={c.name} className="bg-theme-elevated/30 rounded p-2 text-sm border-l-2 border-yellow-500/40">
              <div className="font-medium text-theme-text-primary">{c.name}</div>
              <div className="text-xs text-theme-text-secondary truncate" title={c.image}>{c.image}</div>
              {(c.command || c.args) && (
                <div className="text-xs text-theme-text-tertiary font-mono mt-1 truncate" title={[...(c.command || []), ...(c.args || [])].join(' ')}>
                  $ {[...(c.command || []), ...(c.args || [])].join(' ')}
                </div>
              )}
            </div>
          ))}
          <div className="text-xs text-theme-text-tertiary font-medium uppercase tracking-wide mt-3">Containers</div>
        </>
      )}
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
// EXTERNAL LINKS SECTION
// ============================================================================

const A8R_LINK_KEYS: Record<string, string> = {
  'a8r.io/runbook': 'Runbook',
  'a8r.io/documentation': 'Documentation',
  'a8r.io/repository': 'Repository',
  'a8r.io/logs': 'Logs',
  'a8r.io/chat': 'Chat',
  'a8r.io/incidents': 'Incidents',
  'a8r.io/bugs': 'Bugs',
}

const A8R_TEXT_KEYS: Record<string, string> = {
  'a8r.io/owner': 'Owner',
  'a8r.io/description': 'Description',
}

export function ExternalLinksSection({ data }: { data: any }) {
  const annotations = data.metadata?.annotations
  if (!annotations) return null

  const links: { label: string; url: string }[] = []
  const textProps: { label: string; value: string }[] = []

  // ArgoCD external link
  const argoLink = annotations['link.argocd.argoproj.io/external-link']
  if (argoLink) links.push({ label: 'External Link', url: argoLink })

  // a8r.io links
  for (const [key, label] of Object.entries(A8R_LINK_KEYS)) {
    const val = annotations[key]
    if (val) links.push({ label, url: val })
  }

  // a8r.io text properties
  for (const [key, label] of Object.entries(A8R_TEXT_KEYS)) {
    const val = annotations[key]
    if (val) textProps.push({ label, value: val })
  }

  if (links.length === 0 && textProps.length === 0) return null

  return (
    <Section title={`External Info (${links.length + textProps.length})`} icon={ExternalLink} defaultExpanded>
      <div className="space-y-2">
        {textProps.map(({ label, value }) => (
          <div key={label} className="text-sm">
            <span className="text-theme-text-tertiary">{label}: </span>
            <span className="text-theme-text-primary">{value}</span>
          </div>
        ))}
        {links.map(({ label, url }) => (
          <div key={label} className="text-sm">
            <a
              href={url}
              target="_blank"
              rel="noopener noreferrer"
              className="text-blue-400 hover:text-blue-300 hover:underline inline-flex items-center gap-1"
            >
              {label}
              <ExternalLink className="w-3 h-3" />
            </a>
          </div>
        ))}
      </div>
    </Section>
  )
}

// ============================================================================
// APP INFO SECTION (app.kubernetes.io/* labels)
// ============================================================================

const APP_LABELS: Record<string, string> = {
  'app.kubernetes.io/name': 'App Name',
  'app.kubernetes.io/version': 'Version',
  'app.kubernetes.io/component': 'Component',
  'app.kubernetes.io/part-of': 'Part Of',
  'app.kubernetes.io/managed-by': 'Managed By',
  'app.kubernetes.io/instance': 'Instance',
}

export function AppInfoSection({ data }: { data: any }) {
  const labels = data.metadata?.labels
  if (!labels) return null

  const entries = Object.entries(APP_LABELS)
    .map(([key, label]) => ({ label, value: labels[key] }))
    .filter(({ value }) => value)

  if (entries.length === 0) return null

  return (
    <Section title="App Info" icon={Layers} defaultExpanded>
      <PropertyList>
        {entries.map(({ label, value }) => (
          <Property key={label} label={label} value={value} />
        ))}
      </PropertyList>
    </Section>
  )
}

// ============================================================================
// HELPERS
// ============================================================================

export function getKindColor(kind: string): string {
  const k = kind.toLowerCase()
  // Use darker text for light mode contrast, brighter for dark mode
  if (k.includes('pod')) return 'bg-lime-500/20 text-lime-700 dark:text-lime-400 border-lime-500/30'
  if (k.includes('deployment')) return 'bg-emerald-500/20 text-emerald-700 dark:text-emerald-400 border-emerald-500/30'
  if (k.includes('service')) return 'bg-blue-500/20 text-blue-700 dark:text-blue-400 border-blue-500/30'
  if (k.includes('gateway')) return 'bg-violet-500/20 text-violet-700 dark:text-violet-400 border-violet-500/30'
  if (k.includes('route')) return 'bg-purple-500/20 text-purple-700 dark:text-purple-400 border-purple-500/30'
  if (k.includes('ingress')) return 'bg-violet-500/20 text-violet-700 dark:text-violet-400 border-violet-500/30'
  if (k.includes('configmap')) return 'bg-amber-500/20 text-amber-700 dark:text-amber-400 border-amber-500/30'
  if (k.endsWith('report')) return 'bg-rose-500/20 text-rose-700 dark:text-rose-400 border-rose-500/30'
  if (k.includes('secret')) return 'bg-red-500/20 text-red-700 dark:text-red-400 border-red-500/30'
  if (k.includes('daemonset')) return 'bg-teal-500/20 text-teal-700 dark:text-teal-400 border-teal-500/30'
  if (k.includes('statefulset')) return 'bg-cyan-500/20 text-cyan-700 dark:text-cyan-400 border-cyan-500/30'
  if (k.includes('replicaset')) return 'bg-green-500/20 text-green-700 dark:text-green-400 border-green-500/30'
  if (k.includes('hpa') || k.includes('horizontalpodautoscaler')) return 'bg-pink-500/20 text-pink-700 dark:text-pink-400 border-pink-500/30'
  if (k.includes('cronjob')) return 'bg-orange-500/20 text-orange-700 dark:text-orange-400 border-orange-500/30'
  if (k.includes('job')) return 'bg-orange-500/20 text-orange-700 dark:text-orange-400 border-orange-500/30'
  if (k.includes('node')) return 'bg-gray-500/20 text-gray-700 dark:text-gray-400 border-gray-500/30'
  if (k.includes('namespace')) return 'bg-blue-500/20 text-blue-700 dark:text-blue-400 border-blue-500/30'
  if (k.includes('persistentvolume')) return 'bg-purple-500/20 text-purple-700 dark:text-purple-400 border-purple-500/30'
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

function dedupeRefs(refs: ResourceRef[]): ResourceRef[] {
  const seen = new Set<string>()
  return refs.filter(ref => {
    const key = `${ref.kind}/${ref.namespace}/${ref.name}`
    if (seen.has(key)) return false
    seen.add(key)
    return true
  })
}

export function RelatedResourcesSection({ relationships, onNavigate }: RelatedResourcesSectionProps) {
  if (!relationships) return null

  const hasRelationships =
    relationships.owner ||
    relationships.deployment ||
    (relationships.children && relationships.children.length > 0) ||
    (relationships.services && relationships.services.length > 0) ||
    (relationships.ingresses && relationships.ingresses.length > 0) ||
    (relationships.gateways && relationships.gateways.length > 0) ||
    (relationships.routes && relationships.routes.length > 0) ||
    (relationships.pods && relationships.pods.length > 0) ||
    (relationships.configRefs && relationships.configRefs.length > 0) ||
    (relationships.consumers && relationships.consumers.length > 0) ||
    (relationships.scalers && relationships.scalers.length > 0) ||
    (relationships.policies && relationships.policies.length > 0) ||
    relationships.scaleTarget

  if (!hasRelationships) return null

  return (
    <Section title="Related Resources" icon={Link} defaultExpanded>
      <div className="space-y-3">
        {relationships.owner && (
          <RelationshipGroup label="Owner" refs={[relationships.owner]} onNavigate={onNavigate} />
        )}
        {relationships.deployment && (
          <RelationshipGroup label="Deployment" refs={[relationships.deployment]} onNavigate={onNavigate} />
        )}
        {relationships.children && relationships.children.length > 0 && (
          <RelationshipGroup label="Children" refs={dedupeRefs(relationships.children)} onNavigate={onNavigate} />
        )}
        {relationships.services && relationships.services.length > 0 && (
          <RelationshipGroup label="Services" refs={dedupeRefs(relationships.services)} onNavigate={onNavigate} />
        )}
        {relationships.ingresses && relationships.ingresses.length > 0 && (
          <RelationshipGroup label="Ingresses" refs={dedupeRefs(relationships.ingresses)} onNavigate={onNavigate} />
        )}
        {relationships.gateways && relationships.gateways.length > 0 && (
          <RelationshipGroup label="Gateways" refs={dedupeRefs(relationships.gateways)} onNavigate={onNavigate} />
        )}
        {relationships.routes && relationships.routes.length > 0 && (
          <RelationshipGroup label="Routes" refs={dedupeRefs(relationships.routes)} onNavigate={onNavigate} />
        )}
        {relationships.pods && relationships.pods.length > 0 && (
          <RelationshipGroup label="Pods" refs={dedupeRefs(relationships.pods)} onNavigate={onNavigate} />
        )}
        {relationships.configRefs && relationships.configRefs.length > 0 && (
          <RelationshipGroup label="Configuration" refs={dedupeRefs(relationships.configRefs)} onNavigate={onNavigate} />
        )}
        {relationships.consumers && relationships.consumers.length > 0 && (
          <RelationshipGroup label="Used By" refs={dedupeRefs(relationships.consumers)} onNavigate={onNavigate} />
        )}
        {relationships.scalers && relationships.scalers.length > 0 && (
          <RelationshipGroup label="Autoscaler" refs={dedupeRefs(relationships.scalers)} onNavigate={onNavigate} />
        )}
        {relationships.policies && relationships.policies.length > 0 && (
          <RelationshipGroup label="Disruption Budget" refs={dedupeRefs(relationships.policies)} onNavigate={onNavigate} />
        )}
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

const RELATIONSHIP_TRUNCATE_LIMIT = 10

function RelationshipGroup({ label, refs, onNavigate }: RelationshipGroupProps) {
  const [showAll, setShowAll] = useState(false)
  if (!refs || refs.length === 0) return null

  const truncated = !showAll && refs.length > RELATIONSHIP_TRUNCATE_LIMIT
  const visibleRefs = truncated ? refs.slice(0, RELATIONSHIP_TRUNCATE_LIMIT) : refs

  return (
    <div>
      <div className="text-xs text-theme-text-tertiary mb-1">{label}{refs.length > 1 ? ` (${refs.length})` : ''}</div>
      <div className="flex flex-wrap gap-1">
        {visibleRefs.map((resourceRef, i) => (
          <ResourceRefBadge key={`${resourceRef.kind}-${resourceRef.namespace}-${resourceRef.name}-${i}`} resourceRef={resourceRef} onClick={onNavigate} />
        ))}
        {truncated && (
          <button
            onClick={() => setShowAll(true)}
            className="px-2 py-0.5 text-xs rounded border border-theme-border text-theme-text-secondary hover:text-theme-text-primary hover:bg-theme-elevated transition-colors"
          >
            Show all {refs.length}
          </button>
        )}
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
          'px-2 py-0.5 text-xs rounded border transition-colors hover:brightness-125',
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

/** Inline text link for navigating to a resource. Renders as plain text when onNavigate is absent. */
export function ResourceLink({ name, kind, namespace = '', group, label, onNavigate }: {
  name: string
  kind: string
  namespace?: string
  group?: string
  label?: React.ReactNode
  onNavigate?: ((ref: { kind: string; namespace: string; name: string; group?: string }) => void) | null
}) {
  if (!onNavigate) return <>{label || name}</>
  return (
    <button
      onClick={() => onNavigate({ kind, namespace, name, group })}
      className="text-blue-400 hover:text-blue-300 hover:underline"
    >
      {label || name}
    </button>
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
  /** Optional hint shown below the event list (e.g. "See Timeline tab for related resources") */
  hint?: React.ReactNode
}

export function EventsSection({ events, isLoading, hint }: EventsSectionProps) {
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
        {hint && <div className="mt-2">{hint}</div>}
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
      {hint && <div className="mt-2">{hint}</div>}
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
