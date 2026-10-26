import { dedupeResourceRefs, omitResourceRefs, resourceRefKey } from '../../utils/resource-refs'
import { gapReasonText } from '../../utils/read-actions'
import { customResourceDefinitionRef, getCRDDefinitionsSnapshot, subscribeCRDDefinitions } from '../../utils/navigation'
import { Disclosure } from './Disclosure'
import { createContext, useContext, useState, useSyncExternalStore } from 'react'
import { Copy, Check, Tag, AlertTriangle, CheckCircle, ExternalLink, Layers, X, Minus } from 'lucide-react'
import { clsx } from 'clsx'
import { formatDuration, formatResources } from '../resources/resource-utils'
import { getEffectiveResources } from '../../utils/extended-resources'
import { Tooltip } from './Tooltip'
import { getKindColorClass } from '../ui/Badge'
import { Collapse, CollapseChevron, useDisclosure } from './Collapse'

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
  contentClassName?: string
  variant?: 'plain' | 'card'
}

export function Section({ title, icon: Icon, children, defaultExpanded = true, contentClassName, variant = 'plain' }: SectionProps) {
  const [expanded, setExpanded] = useState(defaultExpanded)
  const { panelId, buttonProps } = useDisclosure(expanded)

  return (
    <div className={variant === 'card' ? 'min-w-0 overflow-hidden rounded-lg border border-theme-border bg-theme-surface' : 'border-b-subtle pb-4 last:border-0'}>
      <button
        {...buttonProps}
        onClick={() => setExpanded(!expanded)}
        className={clsx('flex items-center gap-2 w-full text-left hover:text-theme-text-primary transition-colors', variant === 'card' ? clsx('border-theme-border bg-theme-elevated px-3 py-2.5', expanded && 'border-b') : 'mb-2')}
      >
        <CollapseChevron open={expanded} className="w-4 h-4" />
        {Icon && <Icon className="w-4 h-4 text-theme-text-secondary" />}
        <span className={clsx("font-medium text-theme-text-secondary", variant === 'card' ? 'text-xs uppercase tracking-wider' : 'text-sm')}>{title}</span>
      </button>
      <Collapse open={expanded} id={panelId}>
        <div className={contentClassName ?? (variant === 'card' ? 'p-3' : 'pl-6')}>{children}</div>
      </Collapse>
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
  const { panelId, buttonProps } = useDisclosure(expanded)

  return (
    <div>
      <button
        {...buttonProps}
        onClick={() => setExpanded(!expanded)}
        className="flex items-center gap-2 text-sm text-theme-text-secondary hover:text-theme-text-primary transition-colors mb-1"
      >
        <CollapseChevron open={expanded} className="w-3.5 h-3.5" />
        {title}
      </button>
      <Collapse open={expanded} id={panelId}>
        <div className="ml-5">{children}</div>
      </Collapse>
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
      {/* break-words, not break-all: a long unbroken token (image ref, UID)
          still wraps because it has to, while a sentence keeps its words
          intact. break-all split "PostgreSQL" across two lines. */}
      <span className={clsx('text-theme-text-primary flex-1 min-w-0', isReactElement(value) ? 'break-normal' : 'break-words')}>{displayValue}</span>
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
  // Core K8s — Namespace
  'NamespaceDeletionDiscoveryFailure',
  'NamespaceDeletionGroupVersionParsingFailure',
  'NamespaceDeletionContentFailure',
  'NamespaceContentRemaining',
  'NamespaceFinalizersRemaining',
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
  'StoragePressureRootFileSystem',
  'DPv2MigrationUnsupportedCNI',
  'UnsupportedEBPFPrograms',
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

export type ConditionTone = 'ok' | 'warning' | 'fail' | 'unknown' | 'neutral'

export function defaultConditionTone(cond: { type?: string; status?: string }): Exclude<ConditionTone, 'neutral'> {
  if (cond.status !== 'True' && cond.status !== 'False') return 'unknown'
  return isConditionHealthy(cond) ? 'ok' : 'fail'
}

export function ConditionsSection({
  conditions,
  getConditionTone,
  defaultExpanded,
  variant,
}: {
  conditions?: any[]
  variant?: 'plain' | 'card'
  defaultExpanded?: boolean
  getConditionTone?: (condition: any) => ConditionTone | undefined
}) {
  if (!conditions || conditions.length === 0) return null

  // Sort by lastTransitionTime (most recent first), then alphabetically for ties
  const sorted = [...conditions].sort((a: any, b: any) => {
    const tA = a.lastTransitionTime ? new Date(a.lastTransitionTime).getTime() : 0
    const tB = b.lastTransitionTime ? new Date(b.lastTransitionTime).getTime() : 0
    if (tA !== tB) return tB - tA
    return (a.type || '').localeCompare(b.type || '')
  })

  const conditionTone = (cond: any) => getConditionTone?.(cond) ?? defaultConditionTone(cond)
  const failCount = sorted.filter((c: any) => conditionTone(c) === 'fail').length

  return (
    <Section
      variant={variant}
      title={`Conditions (${conditions.length})${failCount > 0 ? ` · ${failCount} failing` : ''}`}
      defaultExpanded={defaultExpanded ?? conditions.length <= 6}
    >
      <div className="relative">
        {/* Timeline line — sits between timestamp column and dot */}
        <div className="absolute left-[52px] top-2 bottom-2 w-px bg-theme-border" />

        <div className="space-y-0.5">
          {sorted.map((cond: any) => {
            const tone = conditionTone(cond)
            const isOk = tone === 'ok'
            const isWarning = tone === 'warning'
            const isFail = tone === 'fail'
            const isNeutral = tone === 'neutral'
            return (
              <div key={cond.type} className={clsx(
                'flex items-start py-1.5 pr-1 text-sm relative',
                isFail && 'border-l-2 border-red-400/60 dark:border-red-500/40',
                isWarning && 'border-l-2 border-amber-400/60 dark:border-amber-500/40'
              )}>
                {/* Timestamp column — fixed width on the left */}
                <div className="w-[48px] shrink-0 text-[10px] text-theme-text-tertiary text-right pr-2 pt-0.5">
                  {cond.lastTransitionTime ? formatDuration(Date.now() - new Date(cond.lastTransitionTime).getTime(), true) : ''}
                </div>
                {/* Timeline dot */}
                <span className={clsx(
                  'w-3 h-3 rounded-full flex items-center justify-center shrink-0 mt-1 z-10 ring-2 ring-theme-surface',
                  isNeutral ? 'bg-theme-elevated text-theme-text-tertiary'
                    : isOk ? 'bg-emerald-500/20 text-emerald-500 dark:bg-emerald-500/30'
                    : tone === 'unknown' ? 'bg-gray-400/20 text-gray-400 dark:bg-gray-400/30'
                    : isWarning ? 'bg-amber-500/25 text-amber-600 dark:text-amber-400 dark:bg-amber-500/35'
                    : 'bg-red-500/25 text-red-500 dark:bg-red-500/35'
                )}>
                  {isNeutral ? <Minus className="w-2 h-2" strokeWidth={4} />
                    : isOk ? <Check className="w-2 h-2" strokeWidth={4} />
                    : tone === 'unknown' ? <Minus className="w-2 h-2" strokeWidth={4} />
                    : isWarning ? <AlertTriangle className="w-2 h-2" strokeWidth={4} />
                    : <X className="w-2 h-2" strokeWidth={4} />}
                </span>
                {/* Content */}
                <div className="min-w-0 flex-1 pl-2">
                  <span className={clsx(
                    'font-medium text-[13px]',
                    isNeutral ? 'text-theme-text-secondary'
                      : isOk ? 'text-theme-text-primary'
                      : tone === 'unknown' ? 'text-theme-text-secondary'
                        : isWarning ? 'text-amber-600 dark:text-amber-400'
                          : 'text-red-600 dark:text-red-400'
                  )}>{cond.type}</span>
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
  alert:   { bg: 'status-alert', border: '', title: '', message: '', list: '', bullet: '' },
  unknown: { bg: 'status-unknown', border: '', title: '', message: '', list: '', bullet: '' },
  error:   { bg: 'bg-red-50 dark:bg-red-950/30', border: 'border-red-200 dark:border-red-800/30', title: 'text-red-700 dark:text-red-300', message: 'text-red-600/80 dark:text-red-300/70', list: 'text-red-600 dark:text-red-400', bullet: 'text-red-400/60 dark:text-red-400/50' },
  warning: { bg: 'bg-amber-50 dark:bg-amber-950/30', border: 'border-amber-200 dark:border-amber-800/30', title: 'text-amber-700 dark:text-amber-300', message: 'text-amber-600/80 dark:text-amber-300/70', list: 'text-amber-600 dark:text-amber-400', bullet: 'text-amber-400/60 dark:text-amber-400/50' },
  info:    { bg: 'bg-sky-50 dark:bg-sky-950/30', border: 'border-sky-200 dark:border-sky-800/30', title: 'text-sky-700 dark:text-sky-300', message: 'text-sky-600/80 dark:text-sky-300/70', list: 'text-sky-600 dark:text-sky-400', bullet: 'text-sky-400/60 dark:text-sky-400/50' },
  success: { bg: 'bg-emerald-50 dark:bg-emerald-950/30', border: 'border-emerald-200 dark:border-emerald-800/30', title: 'text-emerald-700 dark:text-emerald-300', message: 'text-emerald-600/80 dark:text-emerald-300/70', list: 'text-emerald-600 dark:text-emerald-400', bullet: 'text-emerald-400/60 dark:text-emerald-400/50' },
} as const

const DEFAULT_ICONS: Record<string, React.ComponentType<{ className?: string }>> = {
  alert: AlertTriangle,
  unknown: AlertTriangle,
  error: AlertTriangle,
  warning: AlertTriangle,
  info: AlertTriangle,
  success: CheckCircle,
}

interface AlertBannerProps {
  variant: keyof typeof ALERT_COLORS
  icon?: React.ComponentType<{ className?: string }>
  title: string
  message?: React.ReactNode
  items?: React.ReactNode[]
  children?: React.ReactNode
  /** A control at the end of the title line, e.g. a link to where the problem is handled. */
  action?: React.ReactNode
  /** Replaces the default bottom margin (`mb-4`). */
  className?: string
}

export function AlertBanner({ variant, icon, title, message, items, children, action, className }: AlertBannerProps) {
  const colors = ALERT_COLORS[variant]
  const Icon = icon || DEFAULT_ICONS[variant]
  const hasBody = message || items || children

  return (
    <div className={clsx('p-3 border rounded-lg', className ?? 'mb-4', colors.bg, colors.border)}>
      <div className={clsx('flex gap-2', hasBody ? 'items-start' : 'items-center')}>
        <Icon className={clsx('w-4 h-4 shrink-0', colors.title, hasBody && 'mt-0.5')} />
        {hasBody ? (
          <div className="flex-1 min-w-0">
            {action ? (
              <div className={clsx('flex flex-wrap items-baseline justify-between gap-x-3 gap-y-1', items && 'mb-1')}>
                <div className={clsx('min-w-0 text-sm font-medium', colors.title)}>{title}</div>
                <div className="shrink-0">{action}</div>
              </div>
            ) : (
              <div className={clsx('text-sm font-medium', colors.title, items && 'mb-1')}>{title}</div>
            )}
            {message && <div className={clsx('text-xs mt-1 break-words', colors.message)}>{message}</div>}
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
        ) : action ? (
          <div className="flex min-w-0 flex-1 flex-wrap items-center justify-between gap-x-3 gap-y-1">
            <div className={clsx('min-w-0 text-sm font-medium', colors.title)}>{title}</div>
            <div className="shrink-0">{action}</div>
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
// True when the resource detail is rendering (or still fetching) a dedicated,
// authoritative "Operational Issues" section (the Issues pipeline — richer
// cause/action). Renderers whose problems the pipeline COMPREHENSIVELY covers read
// this and drop their own condition banner so the same failure isn't shown twice.
// It stays true WHILE the live-issues fetch is pending: a banner the pipeline will
// cover would otherwise flash on first paint before the issues arrive and hide it;
// once the fetch settles with no issues (e.g. a CRD the pipeline doesn't watch),
// it flips false and the renderer's banner appears cleanly. It is deliberately
// NOT wired into ProblemAlerts: that component is used only by GitOps renderers
// (Argo/Flux), whose Degraded/OutOfSync/revision-mismatch banners the pipeline
// does not fully emit (e.g. OutOfSync only for automated Argo apps) — suppressing
// them would hide real problems, which is worse than an occasional duplicate.
export const OperationalIssuesShownContext = createContext(false)
export const useOperationalIssuesShown = () => useContext(OperationalIssuesShownContext)

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

export function MetadataSection({ data, onNavigate }: { data: any; onNavigate?: (ref: ResourceRef) => void }) {
  useSyncExternalStore(subscribeCRDDefinitions, getCRDDefinitionsSnapshot, getCRDDefinitionsSnapshot)
  const meta = data.metadata
  if (!meta) return null
  const definition = customResourceDefinitionRef(data.apiVersion, data.kind)

  return (
    <Section title="Metadata" defaultExpanded>
      <PropertyList>
        {definition && <Property label="Definition" value={<ResourceLink {...definition} onNavigate={onNavigate} />} />}
        <Property label="UID" value={meta.uid} />
        <Property label="Resource Version" value={meta.resourceVersion} />
        <Property label="Generation" value={meta.generation} />
        <Property label="Created" value={meta.creationTimestamp ? (
          <Tooltip content={new Date(meta.creationTimestamp).toLocaleString()}>
            <span className="border-b border-dotted border-theme-text-tertiary cursor-help">{formatRelativeAgeTime(meta.creationTimestamp)}</span>
          </Tooltip>
        ) : '-'} />
      </PropertyList>
    </Section>
  )
}

// Templates never get apiserver request-defaulting, so render the effective
// view (requests, falling back to limits) — limits-only GPU specs included.
function ContainerResourcesLine({ resources }: { resources: any }) {
  const effective = getEffectiveResources(resources)
  if (Object.keys(effective).length === 0) return null
  return (
    <Tooltip content="Effective requests (requests, falling back to limits)" delay={150}>
      <div className="text-xs text-theme-text-tertiary mt-1">
        Resources: {formatResources(effective)}
      </div>
    </Tooltip>
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
              <Tooltip content={c.image} delay={300} wrapperClassName="block min-w-0">
                <div className="text-xs text-theme-text-secondary truncate">{c.image}</div>
              </Tooltip>
              {(c.command || c.args) && (
                <Tooltip content={[...(c.command || []), ...(c.args || [])].join(' ')} delay={300} wrapperClassName="block min-w-0">
                  <div className="text-xs text-theme-text-tertiary font-mono mt-1 truncate">
                    $ {[...(c.command || []), ...(c.args || [])].join(' ')}
                  </div>
                </Tooltip>
              )}
              <ContainerResourcesLine resources={c.resources} />
            </div>
          ))}
          <div className="text-xs text-theme-text-tertiary font-medium uppercase tracking-wide mt-3">Containers</div>
        </>
      )}
      {containers.map((c: any) => (
        <div key={c.name} className="card-inner text-sm">
          <div className="font-medium text-theme-text-primary">{c.name}</div>
          <Tooltip content={c.image} delay={300} wrapperClassName="block min-w-0">
            <div className="text-xs text-theme-text-secondary truncate">{c.image}</div>
          </Tooltip>
          {c.ports && (
            <div className="text-xs text-theme-text-tertiary mt-1">
              Ports: {c.ports.map((p: any) => `${p.name ? `${p.name}: ` : ''}${p.containerPort}/${p.protocol || 'TCP'}`).join(', ')}
            </div>
          )}
          <ContainerResourcesLine resources={c.resources} />
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
    replicasets: 'ReplicaSet', services: 'Service', endpointslices: 'EndpointSlice', ingresses: 'Ingress',
    gateways: 'Gateway', httproutes: 'HTTPRoute', grpcroutes: 'GRPCRoute',
    tcproutes: 'TCPRoute', tlsroutes: 'TLSRoute', udproutes: 'UDPRoute', configmaps: 'ConfigMap',
    secrets: 'Secret', jobs: 'Job', cronjobs: 'CronJob', hpas: 'HPA',
    horizontalpodautoscalers: 'HPA', nodes: 'Node', namespaces: 'Namespace',
    persistentvolumeclaims: 'PVC', persistentvolumes: 'PV',
    httpproxies: 'HTTPProxy',
    resourceclaims: 'ResourceClaim', resourceclaimtemplates: 'ResourceClaimTemplate',
    deviceclasses: 'DeviceClass', resourceslices: 'ResourceSlice',
    clusterpolicies: 'ClusterPolicy', nvidiadrivers: 'NVIDIADriver',
    clusterqueues: 'ClusterQueue', localqueues: 'LocalQueue', resourceflavors: 'ResourceFlavor',
    admissionchecks: 'AdmissionCheck', provisioningrequests: 'ProvisioningRequest',
    rayclusters: 'RayCluster', rayjobs: 'RayJob', rayservices: 'RayService', raycronjobs: 'RayCronJob',
    leaderworkersets: 'LeaderWorkerSet', jobsets: 'JobSet',
    inferenceservices: 'InferenceService', servingruntimes: 'ServingRuntime',
    clusterservingruntimes: 'ClusterServingRuntime', inferencegraphs: 'InferenceGraph',
    trainedmodels: 'TrainedModel', llminferenceservices: 'LLMInferenceService',
    inferencepools: 'InferencePool', inferenceobjectives: 'InferenceObjective',
    jobflows: 'JobFlow', jobtemplates: 'JobTemplate', ragengines: 'RAGEngine',
    nimservices: 'NIMService', nimcaches: 'NIMCache', nimpipelines: 'NIMPipeline',
    deviceconfigs: 'DeviceConfig',
    pytorchjobs: 'PyTorchJob', tfjobs: 'TFJob', mpijobs: 'MPIJob', trainjobs: 'TrainJob',
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

// Resolve the display label for a kind chip. The fetched resource's `dataKind`
// is the authoritative PascalCase kind and is correct for every CRD; prefer it.
// Before data loads, fall back to deriving from the URL plural — which is only
// reliable for the core kinds in formatKindName's map.
export function displayKindName(urlPlural: string, dataKind?: string): string {
  return dataKind || formatKindName(urlPlural)
}

// Type for copy handler
export type CopyHandler = (text: string, key: string) => void

// ============================================================================
// RELATED RESOURCES SECTION
// ============================================================================

import type { TimelineEvent, Relationships, ResourceRef, OwnershipCoverage, ManagerAnswer } from '../../types'
import { isChangeEvent, isK8sEvent } from '../../types'
import { Info, Link } from 'lucide-react'
import { formatRelativeAgeTime } from '../../utils/format'

interface RelatedResourcesSectionProps {
  relationships: Relationships | undefined
  onNavigate?: (ref: ResourceRef) => void
  /** The URL plural of the object shown (e.g. `deployments`). */
  kind?: string
  /** The manager the resource's tracking metadata names, when every manager's
   * list was read, that one's doesn't list the resource and another's does. */
  trackedBy?: string
}

// Kinds whose objects usually own others. An empty Children list for one of
// these is surprising, so it says it covered only the kinds Radar checked;
// for any other kind an empty list stays hidden, or nearly every leaf object
// would carry the note.
const KINDS_WITH_CHILDREN = new Set(['deployments', 'replicasets', 'statefulsets', 'daemonsets', 'jobs', 'cronjobs', 'services', 'rollouts'])

export function RelatedResourcesSection({ relationships, onNavigate, kind, trackedBy }: RelatedResourcesSectionProps) {
  if (!relationships) return null

  const configRefs = omitResourceRefs(relationships.configRefs ?? [], relationships.dependencies ?? [])
  const consumers = omitResourceRefs(relationships.consumers ?? [], relationships.dependents ?? [])
  const alsoOwnedBy = omitResourceRefs(relationships.owners ?? [], relationships.owner ? [relationships.owner] : [])
  const managesNote = membersNote(relationships.ownership?.manages, (relationships.manages?.length ?? 0) > 0)
  const managers = dedupeResourceRefs(relationships.managers ?? [])
  const managedByNote = managersNote(relationships.ownership, managers.length, trackedBy)
  const unanswered = ownershipUnanswered(relationships.ownership)
  const several = !relationships.owner && relationships.ownership?.primaryOwnerState === 'several'
  const ownerMissing = relationships.owner || several ? undefined : ownerNote(relationships.ownership)
  let childrenCoverage = childrenNote(relationships.ownership)
  const noChildren = !relationships.children?.length && !relationships.olderRevisions?.length
  if (noChildren && childrenCoverage?.hint && !childrenCoverage.text && kind && KINDS_WITH_CHILDREN.has(kind)) {
    childrenCoverage = { ...childrenCoverage, text: 'No children among the kinds Radar checked.' }
  }

  const hasRelationships =
    relationships.owner ||
    unanswered ||
    ownerMissing ||
    several ||
    childrenCoverage?.text ||
    (relationships.olderRevisions && relationships.olderRevisions.length > 0) ||
    alsoOwnedBy.length > 0 ||
    managers.length > 0 ||
    managedByNote.text ||
    (relationships.manages && relationships.manages.length > 0) ||
    managesNote ||
    relationships.deployment ||
    relationships.node ||
    (relationships.children && relationships.children.length > 0) ||
    (relationships.services && relationships.services.length > 0) ||
    (relationships.ingresses && relationships.ingresses.length > 0) ||
    (relationships.gateways && relationships.gateways.length > 0) ||
    (relationships.routes && relationships.routes.length > 0) ||
    (relationships.pods && relationships.pods.length > 0) ||
    (relationships.configRefs && relationships.configRefs.length > 0) ||
    (relationships.consumers && relationships.consumers.length > 0) ||
    (relationships.scalers && relationships.scalers.length > 0) ||
    (relationships.storageRefs && relationships.storageRefs.length > 0) ||
    (relationships.pdbs && relationships.pdbs.length > 0) ||
    (relationships.networkPolicies && relationships.networkPolicies.length > 0) ||
    (relationships.resourceClaims && relationships.resourceClaims.length > 0) ||
    (relationships.dependencies && relationships.dependencies.length > 0) ||
    (relationships.dependents && relationships.dependents.length > 0) ||
    (relationships.monitors && relationships.monitors.length > 0) ||
    (relationships.monitorTargets && relationships.monitorTargets.length > 0) ||
    (relationships.backends && relationships.backends.length > 0) ||
    (relationships.routedFrom && relationships.routedFrom.length > 0) ||
    (relationships.protects && relationships.protects.length > 0) ||
    (relationships.healthChecks && relationships.healthChecks.length > 0) ||
    (relationships.stagedPolicies && relationships.stagedPolicies.length > 0) ||
    relationships.scaleTarget

  if (!hasRelationships) return null

  return (
    <Section title="Related Resources" icon={Link} defaultExpanded>
      <div className="space-y-3">
        {unanswered && <div className="text-xs text-theme-text-tertiary">{unanswered}</div>}
        {(relationships.owner || ownerMissing) && (
          <RelationshipGroup label="Owner" refs={relationships.owner ? [relationships.owner] : []} onNavigate={onNavigate} note={ownerMissing} />
        )}
        {(alsoOwnedBy.length > 0 || several) && (
          <RelationshipGroup
            label={relationships.owner || ownerMissing ? 'Also Owned By' : 'Owners'}
            refs={dedupeResourceRefs(alsoOwnedBy)}
            onNavigate={onNavigate}
            note={several ? { text: alsoOwnedBy.length > 0 ? 'None of them is its controller.' : 'None of the owners it names can be shown.' } : undefined}
          />
        )}
        {(managers.length > 0 || managedByNote.text) && (
          <RelationshipGroup label={managersListOnly(relationships.ownership?.manager, managers) ? 'Listed By' : 'Managed By'} refs={managers} onNavigate={onNavigate} note={managedByNote} />
        )}
        {relationships.deployment && (
          <RelationshipGroup label="Deployment" refs={[relationships.deployment]} onNavigate={onNavigate} />
        )}
        {relationships.node && (
          <RelationshipGroup label="Node" refs={[relationships.node]} onNavigate={onNavigate} />
        )}
        {((relationships.children && relationships.children.length > 0) || childrenCoverage?.text || (relationships.olderRevisions && relationships.olderRevisions.length > 0)) && (
          <RelationshipGroup
            label="Children"
            refs={dedupeResourceRefs(relationships.children ?? [])}
            onNavigate={onNavigate}
            note={childrenCoverage}
            more={olderRevisions(relationships.olderRevisions)}
            capped={relationships.ownership?.children?.truncated}
          />
        )}
        {((relationships.manages && relationships.manages.length > 0) || managesNote) && (
          <RelationshipGroup label="Manages" refs={dedupeResourceRefs(relationships.manages ?? [])} onNavigate={onNavigate} note={managesNote} capped={relationships.ownership?.manages?.truncated} />
        )}
        {relationships.services && relationships.services.length > 0 && (
          <RelationshipGroup label="Services" refs={dedupeResourceRefs(relationships.services)} onNavigate={onNavigate} />
        )}
        {relationships.ingresses && relationships.ingresses.length > 0 && (
          <RelationshipGroup label="Ingresses" refs={dedupeResourceRefs(relationships.ingresses)} onNavigate={onNavigate} />
        )}
        {relationships.gateways && relationships.gateways.length > 0 && (
          <RelationshipGroup label="Gateways" refs={dedupeResourceRefs(relationships.gateways)} onNavigate={onNavigate} />
        )}
        {relationships.routes && relationships.routes.length > 0 && (
          <RelationshipGroup label="Routes" refs={dedupeResourceRefs(relationships.routes)} onNavigate={onNavigate} />
        )}
        {relationships.pods && relationships.pods.length > 0 && (
          <RelationshipGroup label="Pods" refs={dedupeResourceRefs(relationships.pods)} onNavigate={onNavigate} />
        )}
        {configRefs.length > 0 && (
          <RelationshipGroup label="Configuration" refs={dedupeResourceRefs(configRefs)} onNavigate={onNavigate} />
        )}
        {consumers.length > 0 && (
          <RelationshipGroup label="Used By" refs={dedupeResourceRefs(consumers)} onNavigate={onNavigate} />
        )}
        {relationships.scalers && relationships.scalers.length > 0 && (
          <RelationshipGroup label="Autoscaler" refs={dedupeResourceRefs(relationships.scalers)} onNavigate={onNavigate} />
        )}
        {relationships.storageRefs && relationships.storageRefs.length > 0 && (
          <RelationshipGroup label="Storage" refs={dedupeResourceRefs(relationships.storageRefs)} onNavigate={onNavigate} />
        )}
        {relationships.pdbs && relationships.pdbs.length > 0 && (
          <RelationshipGroup label="Disruption Budget" refs={dedupeResourceRefs(relationships.pdbs)} onNavigate={onNavigate} />
        )}
        {relationships.networkPolicies && relationships.networkPolicies.length > 0 && (
          <RelationshipGroup label="Network Policies" refs={dedupeResourceRefs(relationships.networkPolicies)} onNavigate={onNavigate} />
        )}
        {relationships.resourceClaims && relationships.resourceClaims.length > 0 && (
          <RelationshipGroup label="Resource Claims" refs={dedupeResourceRefs(relationships.resourceClaims)} onNavigate={onNavigate} />
        )}
        {relationships.dependencies && relationships.dependencies.length > 0 && (
          <RelationshipGroup label="Depends On" refs={dedupeResourceRefs(relationships.dependencies)} onNavigate={onNavigate} />
        )}
        {relationships.dependents && relationships.dependents.length > 0 && (
          <RelationshipGroup label="Required By" refs={dedupeResourceRefs(relationships.dependents)} onNavigate={onNavigate} />
        )}
        {relationships.monitors && relationships.monitors.length > 0 && (
          <RelationshipGroup label="Monitored By" refs={dedupeResourceRefs(relationships.monitors)} onNavigate={onNavigate} />
        )}
        {relationships.monitorTargets && relationships.monitorTargets.length > 0 && (
          <RelationshipGroup label="Monitors" refs={dedupeResourceRefs(relationships.monitorTargets)} onNavigate={onNavigate} />
        )}
        {relationships.backends && relationships.backends.length > 0 && (
          <RelationshipGroup label="Routes To" refs={dedupeResourceRefs(relationships.backends)} onNavigate={onNavigate} />
        )}
        {relationships.routedFrom && relationships.routedFrom.length > 0 && (
          <RelationshipGroup label="Routed From" refs={dedupeResourceRefs(relationships.routedFrom)} onNavigate={onNavigate} />
        )}
        {relationships.protects && relationships.protects.length > 0 && (
          <RelationshipGroup label="Selects" refs={dedupeResourceRefs(relationships.protects)} onNavigate={onNavigate} />
        )}
        {relationships.healthChecks && relationships.healthChecks.length > 0 && (
          <RelationshipGroup label="Health Checks" refs={dedupeResourceRefs(relationships.healthChecks)} onNavigate={onNavigate} />
        )}
        {relationships.stagedPolicies && relationships.stagedPolicies.length > 0 && (
          <RelationshipGroup label="Staged Policies" refs={dedupeResourceRefs(relationships.stagedPolicies)} onNavigate={onNavigate} />
        )}
        {relationships.scaleTarget && (
          <RelationshipGroup label="Scale Target" refs={[relationships.scaleTarget]} onNavigate={onNavigate} />
        )}
      </div>
    </Section>
  )
}

// ownershipUnanswered says why Owner and Children couldn't be answered at all.
function ownershipUnanswered(coverage: OwnershipCoverage | undefined): string | undefined {
  const reason = coverage?.unavailableReason
  if (!reason) return undefined
  if (reason === 'user_forbidden') return "Owner and children aren't shown: your access lets you open this object, but not list its kind here."
  return `Owner and children couldn't be read: ${gapReasonText(reason)}.`
}

// ownerNote says why no owner is named, when there is an owner reference.
function ownerNote(coverage: OwnershipCoverage | undefined): RelationshipNote | undefined {
  const ref = coverage?.ownerRef
  const named = ref?.kind && ref.name ? `${ref.kind} ${ref.name}` : 'the owner it names'
  switch (coverage?.primaryOwnerState) {
    case 'gone':
      return { text: `${capitalize(named)} no longer exists.` }
    case 'replaced':
      return { text: `${capitalize(named)} was deleted and recreated; this object names the earlier one.` }
    case 'unknown':
      return { text: coverage?.ownerReason ? `Couldn't check ${named} (${gapReasonText(coverage.ownerReason)}).` : `Couldn't check ${named}.` }
    case 'withheld':
      return { text: 'Not visible to you.' }
    case 'unresolvable':
      return { text: "Its owner reference can't be resolved in this cluster." }
    case 'excluded':
      return { text: "Its owner reference names an Event, which Radar doesn't follow as an owner." }
  }
  return undefined
}

// childrenNote says what a Children list may be missing. Kinds Radar doesn't
// collect are a hint, since most lists miss a few; kinds it couldn't check
// for any other reason, a list cut short, and places the viewer may not read
// are said outright.
function childrenNote(coverage: OwnershipCoverage | undefined): RelationshipNote | undefined {
  const children = coverage?.children
  if (!children) return undefined
  const uncollected = new Set<string>()
  let someNamespaces = false
  let undiscovered = false
  const pressing = new Map<string, string>()
  for (const g of children.notChecked ?? []) {
    if (!g.kind) undiscovered = true
    else if (g.reason === 'radar_kind_not_collected') uncollected.add(g.kind)
    else if (g.reason === 'radar_namespace_not_collected') someNamespaces = true
    else if (!pressing.has(g.kind)) pressing.set(g.kind, gapReasonText(g.reason))
  }
  const note: RelationshipNote = {}
  const hint: string[] = []
  if (uncollected.size > 0) hint.push(`Radar doesn't collect ${joinOr(Array.from(uncollected))} objects, so none of those are listed.`)
  if (someNamespaces) hint.push("Radar doesn't collect every namespace, so children in the others aren't listed.")
  // An API group whose discovery fails, often a metrics API with no server
  // behind it, leaves every answer with this gap; most such groups hold no
  // children, so it is a hint, not a warning.
  if (undiscovered) hint.push("Radar's API discovery missed some API groups, so children of their kinds aren't listed.")
  if (children.narrowed) hint.push('Only children you can view are listed.')
  if (hint.length > 0) note.hint = hint
  const text: string[] = []
  if (pressing.size > 0) text.push(`Couldn't check ${Array.from(pressing, ([kind, why]) => `${kind} (${why})`).join(', ')}.`)
  if (children.truncated) text.push('More children than shown.')
  if (text.length > 0) note.text = text.join(' ')
  return note.hint || note.text ? note : undefined
}

function capitalize(s: string): string {
  return s.charAt(0).toUpperCase() + s.slice(1)
}

function joinOr(items: string[]): string {
  return items.length < 2 ? items.join('') : `${items.slice(0, -1).join(', ')} or ${items[items.length - 1]}`
}

// membersNote says what a GitOps manager's status records beyond the members
// Manages lists; listed says whether it lists any.
function membersNote(manages: OwnershipCoverage['manages'] | undefined, listed: boolean): RelationshipNote | undefined {
  if (!manages) return undefined
  if (manages.membership === 'remote') return { text: "Its destination isn't this cluster as far as Radar can tell, so its members aren't listed." }
  if (manages.membership === 'unreadable') {
    const note: RelationshipNote = {}
    const text: string[] = []
    if (manages.unavailableReason) text.push(`Couldn't read the members its status records (${gapReasonText(manages.unavailableReason)}).`)
    if (manages.truncated) text.push('More members than shown.')
    if (text.length > 0) note.text = text.join(' ')
    if (listed) note.hint = ["Its status has no member list Radar can read now, so these are the resources the topology links to it."]
    else if (!manages.unavailableReason) note.hint = ["Its status has no member list Radar can read now."]
    return note
  }
  const extra: string[] = []
  if (manages.missing) extra.push(`${manages.missing} not in the cluster`)
  if (manages.withheld) extra.push(`${manages.withheld} you can't view`)
  if (manages.unknown) extra.push(`${manages.unknown} Radar can't check`)
  const parts: string[] = []
  if (extra.length > 0) parts.push(`Its status also lists ${extra.join(', ')}.`)
  if (manages.truncated) parts.push('More members than shown.')
  if (parts.length === 0 && !listed) parts.push('Its status records no members to show.')
  return parts.length > 0 ? { text: parts.join(' ') } : undefined
}

export interface RelationshipNote {
  /** Shown under the group. */
  text?: string
  /** Lines shown on hovering an info mark beside the label. */
  hint?: string[]
}

interface RelationshipGroupProps {
  label: string
  refs: ResourceRef[]
  onNavigate?: (ref: ResourceRef) => void
  note?: RelationshipNote
  /** Refs shown only on request, such as a Deployment's old ReplicaSets. */
  more?: MoreRefs
  /** The server cut the list short, so refs is not every one. */
  capped?: boolean
}

interface MoreRefs {
  refs: ResourceRef[]
  /** What the refs are, as "older ReplicaSets" reads after a count. */
  label: string
  /** Why they're kept apart, shown on hovering the toggle. */
  hint: string
}

// olderRevisions are the ReplicaSets the server keeps out of Children because
// they only hold rollback history: listed there, ten of them would bury the
// ones running Pods.
function olderRevisions(refs: ResourceRef[] | undefined): MoreRefs | undefined {
  if (!refs || refs.length === 0) return undefined
  const unique = dedupeResourceRefs(refs)
  return {
    refs: unique,
    label: unique.length === 1 ? 'older ReplicaSet' : 'older ReplicaSets',
    hint: 'Earlier revisions kept for rollback. They run no Pods.',
  }
}

const RELATIONSHIP_TRUNCATE_LIMIT = 10

// Managed By sits next to Owner and reads like it, so it says how they differ
// and which direction its answer comes from.
const MANAGED_BY_HINT = [
  'Argo CD Applications or Flux objects whose status lists this resource, and other controllers that manage it without an owner reference.',
  "Kubernetes' garbage collector follows owner references, not these links.",
]

// managersListOnly: every manager shown is one whose list names the resource
// while its metadata doesn't confirm it, which the chip calls Listed by.
function managersListOnly(answer: ManagerAnswer | undefined, managers: ResourceRef[]): boolean {
  if (!answer || answer.via || !['listed', 'shared', 'disagree'].includes(answer.state) || !managers.length) return false
  const recorders = new Set((answer.direct.recorders ?? []).map(resourceRefKey))
  return managers.every(m => recorders.has(resourceRefKey(m)))
}

// managersNote explains Managed By: where it comes from, more than one
// manager, and the managers the search couldn't settle. With none listed, a
// remote list naming the resource or a manager kind Radar couldn't search is
// said outright, so an empty group doesn't read as "no GitOps manager". An
// unreadable list stays in the hint: it may name any resource, so it would
// otherwise show beside every one, and the controllers stamp tracking
// metadata on what they apply, through which the topology names such a
// manager anyway.
function managersNote(coverage: OwnershipCoverage | undefined, listed: number, trackedBy?: string): RelationshipNote {
  const hint = [...MANAGED_BY_HINT]
  const open: string[] = []
  const coverageOf = coverage?.managers
  if (coverageOf?.unreadable) {
    const n = coverageOf.unreadable
    hint.push(n === 1 ? "1 manager's member list can't be read, so it may also list this resource." : `${n} managers' member lists can't be read, so they may also list this resource.`)
  }
  if (coverageOf?.remote) {
    const n = coverageOf.remote
    open.push(`${n} ${n === 1 ? 'manager lists' : 'managers list'} it for a cluster Radar can't confirm is this one`)
  }
  const pressing = new Map<string, string>()
  for (const g of coverageOf?.notChecked ?? []) {
    // An unserved manager kind under incomplete discovery reads as read_error
    // on every resource, like discovery's own gap.
    if (!g.kind) hint.push("Radar's API discovery missed some API groups, so managers of their kinds aren't checked.")
    else if (g.reason === 'read_error') hint.push(`Radar couldn't search ${g.kind} objects, so those managers aren't checked.`)
    else if (g.reason === 'radar_kind_not_collected') hint.push(`Radar doesn't collect ${g.kind} objects, so those managers aren't checked.`)
    else if (!pressing.has(g.kind)) pressing.set(g.kind, gapReasonText(g.reason))
  }
  if (pressing.size > 0) open.push(`couldn't check ${Array.from(pressing, ([kind, why]) => `${kind} (${why})`).join(', ')}`)
  const text: string[] = []
  if (trackedBy) text.push(`Its tracking label names ${trackedBy}, whose status doesn't list it.`)
  if (open.length > 0) {
    const sentence = `${capitalize(open.join('; '))}.`
    if (listed === 0) text.push(sentence)
    else hint.push(sentence)
  }
  if (listed > 1) text.push('More than one manager lists or links to it.')
  const note: RelationshipNote = { hint: [...new Set(hint)] }
  if (text.length > 0) note.text = text.join(' ')
  return note
}

export function RelationshipGroup({ label, refs, onNavigate, note, more, capped }: RelationshipGroupProps) {
  const [showAll, setShowAll] = useState(false)
  if ((!refs || refs.length === 0) && !note && !more) return null
  const truncated = !showAll && refs.length > RELATIONSHIP_TRUNCATE_LIMIT
  const visibleRefs = truncated ? refs.slice(0, RELATIONSHIP_TRUNCATE_LIMIT) : refs

  return (
    <div>
      <div className="text-xs text-theme-text-tertiary mb-1 flex items-center gap-1">
        <span>{label}{refs.length > 1 ? ` (${refs.length}${capped ? '+' : ''})` : ''}</span>
        {note?.hint && (
          <Tooltip content={<div className="space-y-0.5">{note.hint.map((line) => <div key={line}>{line}</div>)}</div>} delay={150} className="max-h-64 overflow-y-auto">
            <button type="button" className="inline-flex rounded text-theme-text-tertiary hover:text-theme-text-secondary" aria-label={note.hint.join(' ')}>
              <Info className="w-3 h-3" aria-hidden="true" />
            </button>
          </Tooltip>
        )}
      </div>
      <div className="flex flex-wrap gap-1">
        {visibleRefs.map((resourceRef, i) => (
          <ResourceRefBadge key={`${resourceRef.kind}-${resourceRef.namespace}-${resourceRef.name}-${i}`} resourceRef={resourceRef} onClick={onNavigate} />
        ))}
        {truncated && (
          <button
            onClick={() => setShowAll(true)}
            className="px-2 py-0.5 text-xs rounded border border-theme-border text-theme-text-secondary hover:text-theme-text-primary hover:bg-theme-elevated transition-colors"
          >
            {capped ? `Show the ${refs.length} listed` : `Show all ${refs.length}`}
          </button>
        )}
      </div>
      {more && <MoreRefsToggle more={more} onNavigate={onNavigate} />}
      {note?.text && <div className="mt-1 text-xs text-theme-text-tertiary">{note.text}</div>}
    </div>
  )
}

function MoreRefsToggle({ more, onNavigate }: { more: MoreRefs; onNavigate?: (ref: ResourceRef) => void }) {
  const [open, setOpen] = useState(false)
  const { panelId, buttonProps } = useDisclosure(open)
  return (
    <div className="mt-1">
      <button
        {...buttonProps}
        onClick={() => setOpen(!open)}
        title={more.hint}
        className="flex items-center gap-1 text-xs text-theme-text-tertiary hover:text-theme-text-primary transition-colors"
      >
        <CollapseChevron open={open} className="w-3 h-3" />
        {open ? `Hide ${more.label}` : `Show ${more.refs.length} ${more.label}`}
      </button>
      <Collapse open={open} id={panelId} mountLazily>
        <div className="flex flex-wrap gap-1 mt-1">
          {more.refs.map((resourceRef, i) => (
            <ResourceRefBadge key={`${resourceRef.kind}-${resourceRef.namespace}-${resourceRef.name}-${i}`} resourceRef={resourceRef} onClick={onNavigate} />
          ))}
        </div>
      </Collapse>
    </div>
  )
}

export interface ResourceRefBadgeProps {
  resourceRef: ResourceRef
  onClick?: (ref: ResourceRef) => void
  wrapAtSeparator?: boolean
}

/** Reusable chip/badge for showing a related resource with click-to-navigate */
export function ResourceRefBadge({ resourceRef, onClick, wrapAtSeparator }: ResourceRefBadgeProps) {
  const kindClass = getKindColor(resourceRef.kind)
  const kindName = formatKindForRef(resourceRef.kind, resourceRef.group)
  const content = wrapAtSeparator ? (
    <>
      <span className="shrink-0 opacity-60">{kindName}/</span>
      <span className="min-w-0 max-w-full whitespace-normal break-normal text-left leading-tight">{resourceRef.name}</span>
    </>
  ) : (
    <>
      <span className="opacity-60">{kindName}/</span>
      {resourceRef.name}
    </>
  )
  const layoutClass = wrapAtSeparator
    ? 'badge max-w-full min-w-0 flex-wrap items-center whitespace-normal text-left leading-tight'
    : 'badge'
  const tooltip = `${resourceRef.kind}: ${resourceRef.namespace}/${resourceRef.name}`

  if (onClick) {
    return (
      <Tooltip content={tooltip} delay={150}>
        <button
          onClick={() => onClick(resourceRef)}
          className={clsx(
            layoutClass,
            'hover:brightness-[0.92] dark:hover:brightness-125 transition-[filter]',
            kindClass
          )}
        >
          {content}
        </button>
      </Tooltip>
    )
  }

  return (
    <Tooltip content={tooltip} delay={300}>
      <span className={clsx(layoutClass, kindClass)}>
        {content}
      </span>
    </Tooltip>
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

function formatKindForRef(kind: string, group?: string): string {
  const k = kind.toLowerCase()
  // A Knative Service shares its kind with core Services; use Knative's own short name.
  if (k === 'service' && group === 'serving.knative.dev') return 'ksvc'
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
    udproute: 'udp',
    tlsroute: 'tls',
    secret: 'secret',
    pod: 'pod',
    job: 'job',
    cronjob: 'cj',
    hpa: 'hpa',
    horizontalpodautoscaler: 'hpa',
  }
  return shortNames[k] || k
}

// ============================================================================
// EVENTS SECTION
// ============================================================================

interface EventsSectionProps {
  /** K8s events for the focused resource — always shown. */
  events: TimelineEvent[]
  /** Resource update events (informer/historical diffs) — hidden behind a
   *  toggle to avoid drowning out K8s events when a resource flaps. */
  updates?: TimelineEvent[]
  isLoading?: boolean
  /** Errors from the K8s events / updates queries. Rendered inline so a
   *  failed fetch doesn't silently look like "no events." */
  eventsError?: Error | null
  updatesError?: Error | null
  /** Optional hint shown below the event list (e.g. "See Timeline tab for related resources") */
  hint?: React.ReactNode
  fullscreen?: boolean
}

export function EventsSection({ events, updates = [], isLoading, eventsError, updatesError, hint, fullscreen = false }: EventsSectionProps) {
  const [showUpdates, setShowUpdates] = useState(false)
  const [showAllEvents, setShowAllEvents] = useState(false)

  if (isLoading) {
    return (
      <Section title="Recent Events" defaultExpanded>
        <div className="text-sm text-theme-text-tertiary">Loading events…</div>
      </Section>
    )
  }

  const updateCount = updates.length
  const visible = showUpdates
    ? [...events, ...updates].sort((a, b) => new Date(b.timestamp).getTime() - new Date(a.timestamp).getTime())
    : events

  const toggle = updateCount > 0 ? (
    <Tooltip
      className="max-w-xs leading-snug"
      content={
        <span style={{ whiteSpace: 'normal', display: 'inline-block' }}>
          Changes are field-level diffs to this resource&apos;s spec or status (e.g. status flips, replica counts). Distinct from K8s events, which are messages emitted by the kubelet and controllers.
        </span>
      }
    >
      <button
        onClick={(e) => { e.stopPropagation(); setShowUpdates(v => !v) }}
        className="text-xs text-theme-text-tertiary hover:text-theme-text-secondary transition-colors"
      >
        {showUpdates ? `Hide ${updateCount} changes` : `Show ${updateCount} changes`}
      </button>
    </Tooltip>
  ) : null

  const errors = (
    <>
      {eventsError && (
        <div className="text-xs text-red-500 mt-2">Failed to load K8s events: {eventsError.message}</div>
      )}
      {updatesError && (
        <div className="text-xs text-red-500 mt-2">Failed to load resource changes: {updatesError.message}</div>
      )}
    </>
  )

  if (visible.length === 0) {
    return (
      <Section title="Recent Events" defaultExpanded={!!(eventsError || updatesError)}>
        <div className="text-sm text-theme-text-tertiary">No recent events</div>
        {errors}
        {toggle && <div className="mt-2">{toggle}</div>}
        {hint && <div className="mt-2">{hint}</div>}
      </Section>
    )
  }

  const renderEvent = (event: TimelineEvent, i: number) => (
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
  )
  return (
    <Section title={`Recent Events (${visible.length})`} defaultExpanded>
      <div tabIndex={fullscreen ? undefined : 0} className={clsx('space-y-2', !fullscreen && 'max-h-64 overflow-y-auto')}>
        {(fullscreen ? visible.slice(0, 8) : visible).map(renderEvent)}
      </div>
      {fullscreen && visible.length > 8 && <Disclosure open={showAllEvents} onOpenChange={setShowAllEvents} summary={showAllEvents ? 'Show fewer events' : `Show ${visible.length - 8} more events`} className="mt-2" summaryClassName="text-xs text-accent-text">
        <div className="mt-2 space-y-2">{visible.slice(8).map(renderEvent)}</div>
      </Disclosure>}
      {errors}
      {toggle && <div className="mt-2">{toggle}</div>}
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
