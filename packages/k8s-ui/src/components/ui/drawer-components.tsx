import { useState } from 'react'
import { ChevronRight, Copy, Check, Tag, AlertTriangle, CheckCircle, ExternalLink, Layers, X, Minus } from 'lucide-react'
import { clsx } from 'clsx'
import { formatAge, formatDuration } from '../resources/resource-utils'
import { Tooltip } from './Tooltip'
import { getKindColorClass } from '../ui/Badge'

// ============================================================================
// UI COMPONENTS
// ============================================================================

export function KeyValueBadge({ k, v }: { k: string; v: string }) {
  return (
    <span className="badge bg-theme-elevated text-theme-text-secondary">
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

/**
 * Renders a Kubernetes label selector (matchLabels + matchExpressions).
 * Accepts either a full selector object `{ matchLabels?, matchExpressions? }`
 * or a flat `Record<string, string>` (e.g. Service.spec.selector).
 * Pass `inline` for flow-inline rendering (e.g. within a sentence).
 */
export function LabelSelectorDisplay({
  selector,
  emptyText = 'All',
  inline = false,
}: {
  selector: any
  emptyText?: string
  inline?: boolean
}) {
  if (!selector) {
    return <span className="text-xs text-theme-text-tertiary">{emptyText}</span>
  }

  // Detect flat selector (e.g. Service.spec.selector has no matchLabels wrapper)
  const isFlat = typeof selector === 'object' && !Array.isArray(selector) && !selector.matchLabels && !selector.matchExpressions
  const matchLabels: Record<string, unknown> = isFlat ? selector : (selector.matchLabels || {})
  const matchExpressions: Array<{ key: string; operator: string; values?: string[] }> = selector.matchExpressions || []

  const hasLabels = Object.keys(matchLabels).length > 0
  const hasExpressions = matchExpressions.length > 0

  if (!hasLabels && !hasExpressions) {
    return <span className="text-xs text-theme-text-tertiary">{emptyText}</span>
  }

  const Wrapper = inline ? 'span' : 'div'

  return (
    <Wrapper className={inline ? 'inline-flex flex-wrap gap-1 align-middle' : 'flex flex-wrap gap-1'}>
      {Object.entries(matchLabels).map(([k, v]) => (
        <KeyValueBadge key={`l-${k}`} k={k} v={String(v)} />
      ))}
      {matchExpressions.map((expr, i) => (
        <span key={`e-${i}`} className="badge bg-theme-elevated text-theme-text-secondary">
          {expr.key} {expr.operator}{expr.values && expr.values.length > 0 ? ` ${expr.values.join(', ')}` : ''}
        </span>
      ))}
    </Wrapper>
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
      <span className="text-theme-text-tertiary w-40 shrink-0">{label}</span>
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

// Condition types where status=True means a problem and status=False means healthy.
//
// K8s has no canonical API metadata for polarity — SIG-API Machinery explicitly
// declined to add it (https://gateway-api.sigs.k8s.io/geps/gep-1364/), so every
// consumer has to maintain a list. This one is assembled from:
//   - Core K8s (kubelet node conditions, Pod, workload controllers)
//   - Node Problem Detector default configs (kubernetes/node-problem-detector)
//   - GKE's NPD plugin set (observed in-cluster)
//   - ArgoCD Application conditions (argoproj/argo-cd)
//   - FluxCD meta conditions (fluxcd/pkg/apis/meta)
//   - Conventional Operator-pattern conditions (Degraded, OutOfSync)
// Patterns only cover two NPD naming families whose convention is
// strict ("Deprecated*" for deprecation checks, "Frequent*" for restart
// counters) — everything else is explicit to avoid guessing wrong.
const NEGATIVE_POLARITY_TYPES = new Set([
  // Core K8s — Node (kubelet)
  'MemoryPressure',
  'DiskPressure',
  'PIDPressure',
  'NetworkUnavailable',
  // Core K8s — Pod
  'DisruptionTarget',
  // Core K8s — workloads
  'ReplicaFailure',
  // NPD — upstream default configs
  'KernelDeadlock',
  'CperHardwareErrorFatal',
  'XfsShutdown',
  'CorruptDockerOverlay2',
  'ReadonlyFilesystem',
  'ContainerRuntimeUnhealthy',
  'KubeletUnhealthy',
  // NPD — GKE extensions (observed)
  'ReadOnlyRootFileSystem',
  'ResourceExhausted',
  'Swap',
  // NPD — AKS extensions (learn.microsoft.com/azure/aks/node-problem-detector)
  'FilesystemCorruptionProblem',
  'KubeletProblem',
  'ContainerRuntimeProblem',
  'VMEventScheduled',
  'GPUMissing',
  'NVLinkStatusInactive',
  'XIDErrors',
  'IBLinkFlapping',
  'GPUClockThrottling',
  'UnhealthyNvidiaDevicePlugin',
  // ArgoCD Application
  'DeletionError',
  'InvalidSpecError',
  'ComparisonError',
  'SyncError',
  'UnknownError',
  'SharedResourceWarning',
  'RepeatedResourceWarning',
  'OrphanedResourceWarning',
  'ExcludedResourceWarning',
  'OutOfSync',
  // ArgoCD ApplicationSet
  'ErrorOccurred',
  // FluxCD (meta)
  'Stalled',
  // Gateway API (sigs.k8s.io/gateway-api v1)
  'Conflicted',
  'OverlappingTLSConfig',
  'PartiallyInvalid',
  'InsecureFrontendValidationMode',
  // cert-manager (CertificateRequest)
  'Denied',
  'InvalidRequest',
  // Karpenter NodeClaim
  'Drifted',
  // VPA
  'ConfigUnsupported',
  'LowConfidence',
  // KEDA ScaledObject
  'Fallback',
  // Operator-pattern CRDs (widespread)
  'Degraded',
  'Failed',
])

const NEGATIVE_POLARITY_PATTERNS: RegExp[] = [
  // NPD deprecation-check family (e.g. DeprecatedAuthsFieldInContainerdConfiguration)
  /^Deprecated/,
  // NPD restart-counter family (e.g. FrequentKubeletRestart, FrequentUnregisterNetDevice)
  /^Frequent/,
]

function isInvertedPolarityCondition(type: string | undefined): boolean {
  if (!type) return false
  if (NEGATIVE_POLARITY_TYPES.has(type)) return true
  return NEGATIVE_POLARITY_PATTERNS.some((re) => re.test(type))
}

function isConditionHealthy(cond: { type?: string; status?: string }): boolean {
  const inverted = isInvertedPolarityCondition(cond.type)
  return inverted ? cond.status === 'False' : cond.status === 'True'
}

export function ConditionsSection({ conditions }: { conditions?: any[] }) {
  if (!conditions || conditions.length === 0) return null

  // Sort by lastTransitionTime (most recent first), then alphabetically for ties
  const sorted = [...conditions].sort((a: any, b: any) => {
    const tA = a.lastTransitionTime ? new Date(a.lastTransitionTime).getTime() : 0
    const tB = b.lastTransitionTime ? new Date(b.lastTransitionTime).getTime() : 0
    if (tA !== tB) return tB - tA
    return (a.type || '').localeCompare(b.type || '')
  })

  const failCount = sorted.filter((c: any) => (c.status === 'True' || c.status === 'False') && !isConditionHealthy(c)).length

  return (
    <Section
      title={`Conditions (${conditions.length})${failCount > 0 ? ` · ${failCount} failing` : ''}`}
      defaultExpanded={conditions.length <= 6}
    >
      <div className="relative">
        {/* Timeline line — sits between timestamp column and dot */}
        <div className="absolute left-[52px] top-2 bottom-2 w-px bg-theme-border" />

        <div className="space-y-0.5">
          {sorted.map((cond: any) => {
            const isUnknown = cond.status !== 'True' && cond.status !== 'False'
            const isOk = !isUnknown && isConditionHealthy(cond)
            const isFail = !isOk && !isUnknown
            return (
              <div key={cond.type} className={clsx(
                'flex items-start py-1.5 pr-1 text-sm relative',
                isFail && 'border-l-2 border-red-400/60 dark:border-red-500/40'
              )}>
                {/* Timestamp column — fixed width on the left */}
                <div className="w-[48px] shrink-0 text-[10px] text-theme-text-tertiary text-right pr-2 pt-0.5">
                  {cond.lastTransitionTime ? formatDuration(Date.now() - new Date(cond.lastTransitionTime).getTime(), true) : ''}
                </div>
                {/* Timeline dot */}
                <span className={clsx(
                  'w-3 h-3 rounded-full flex items-center justify-center shrink-0 mt-1 z-10 ring-2 ring-theme-surface',
                  isOk ? 'bg-emerald-500/20 text-emerald-500 dark:bg-emerald-500/30'
                    : isUnknown ? 'bg-gray-400/20 text-gray-400 dark:bg-gray-400/30'
                    : 'bg-red-500/25 text-red-500 dark:bg-red-500/35'
                )}>
                  {isOk ? <Check className="w-2 h-2" strokeWidth={4} />
                    : isUnknown ? <Minus className="w-2 h-2" strokeWidth={4} />
                    : <X className="w-2 h-2" strokeWidth={4} />}
                </span>
                {/* Content */}
                <div className="min-w-0 flex-1 pl-2">
                  <span className={clsx('font-medium text-[13px]', isOk ? 'text-theme-text-primary' : isUnknown ? 'text-theme-text-secondary' : 'text-red-600 dark:text-red-400')}>{cond.type}</span>
                  {cond.reason && cond.reason !== cond.type && (
                    <div className="text-[10px] text-theme-text-secondary">{cond.reason}</div>
                  )}
                  {cond.message && <div className="text-[10.5px] text-theme-text-tertiary break-words leading-relaxed">{cond.message}</div>}
                </div>
              </div>
            )
          })}
        </div>
      </div>
    </Section>
  )
}

// ============================================================================
// ALERT BANNER
// ============================================================================

const ALERT_COLORS = {
  error:   { bg: 'bg-red-50 dark:bg-red-950/30', border: 'border-red-200 dark:border-red-800/30', title: 'text-red-700 dark:text-red-300', message: 'text-red-600/80 dark:text-red-300/70', list: 'text-red-600 dark:text-red-400', bullet: 'text-red-400/60 dark:text-red-400/50' },
  warning: { bg: 'bg-amber-50 dark:bg-amber-950/30', border: 'border-amber-200 dark:border-amber-800/30', title: 'text-amber-700 dark:text-amber-300', message: 'text-amber-600/80 dark:text-amber-300/70', list: 'text-amber-600 dark:text-amber-400', bullet: 'text-amber-400/60 dark:text-amber-400/50' },
  info:    { bg: 'bg-sky-50 dark:bg-sky-950/30', border: 'border-sky-200 dark:border-sky-800/30', title: 'text-sky-700 dark:text-sky-300', message: 'text-sky-600/80 dark:text-sky-300/70', list: 'text-sky-600 dark:text-sky-400', bullet: 'text-sky-400/60 dark:text-sky-400/50' },
  success: { bg: 'bg-emerald-50 dark:bg-emerald-950/30', border: 'border-emerald-200 dark:border-emerald-800/30', title: 'text-emerald-700 dark:text-emerald-300', message: 'text-emerald-600/80 dark:text-emerald-300/70', list: 'text-emerald-600 dark:text-emerald-400', bullet: 'text-emerald-400/60 dark:text-emerald-400/50' },
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

/** Displays a list of problem alerts (warnings and errors) */
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
            <div key={c.name} className="card-inner text-sm border-l-2 border-yellow-500/40">
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
        <div key={c.name} className="card-inner text-sm">
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

/** Get kind badge color — delegates to Badge.tsx's static color lookup */
export function getKindColor(kind: string): string {
  return getKindColorClass(kind)
}

// In drawer headers, kind sits next to a status pill ("Pod" / "Running"). Both
// filled creates two pills competing for attention even though kind is just
// metadata (the user already clicked the resource) and status is the signal.
// Stripping the fills makes kind read as a label and status as a value.
export function getKindColorOutline(kind: string): string {
  return getKindColorClass(kind)
    .replace(/\b(?:dark:)?bg-\S+/g, '')
    .replace(/\s+/g, ' ')
    .trim()
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
    httpproxies: 'HTTPProxy',
  }
  if (names[k]) return names[k]

  // For unknown kinds (CRDs), use the original kind name
  // or format it nicely if it's a plural name
  if (k.endsWith('ies')) {
    // Handle -ies → -y (e.g., httpproxies → Httpproxy)
    const singular = kind.slice(0, -3) + 'y'
    return singular.charAt(0).toUpperCase() + singular.slice(1)
  }
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
        {relationships.policies && relationships.policies.length > 0 && (() => {
          const policyKinds = new Set(['NetworkPolicy', 'CiliumNetworkPolicy', 'CiliumClusterwideNetworkPolicy', 'ClusterNetworkPolicy'])
          const pdbs = relationships.policies.filter(r => r.kind === 'PodDisruptionBudget')
          const netpols = relationships.policies.filter(r => policyKinds.has(r.kind))
          const other = relationships.policies.filter(r => r.kind !== 'PodDisruptionBudget' && !policyKinds.has(r.kind))
          return (
            <>
              {pdbs.length > 0 && (
                <RelationshipGroup label="Disruption Budget" refs={dedupeRefs(pdbs)} onNavigate={onNavigate} />
              )}
              {netpols.length > 0 && (
                <RelationshipGroup label="Network Policies" refs={dedupeRefs(netpols)} onNavigate={onNavigate} />
              )}
              {other.length > 0 && (
                <RelationshipGroup label="Policies" refs={dedupeRefs(other)} onNavigate={onNavigate} />
              )}
            </>
          )
        })()}
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
          'badge hover:brightness-[0.92] dark:hover:brightness-125 transition-[filter]',
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
      className={clsx('badge', kindClass)}
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
