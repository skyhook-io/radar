import { useState } from 'react'
import { Info, CheckCircle, XCircle, AlertCircle, FileCode, AlertTriangle, Layers, Minus } from 'lucide-react'
import { clsx } from 'clsx'
import { AlertBanner, Section, PropertyList, Property, isInvertedPolarityCondition } from '../../ui/drawer-components'
import { Collapse, CollapseChevron, useDisclosure } from '../../ui/Collapse'
import { getGenericResourceStatus } from '../generic-status'
import { getAgentRuntimeStatus } from '../resource-utils-agent-runtimes'
import { isApiGroup } from '../resource-utils-cnpg'
import { getInferenceServiceStatus, isInferenceServiceStopRequested } from '../resource-utils-kserve'
import { getCrossplanePauseState, getCrossplaneIndependentStatus, getCrossplaneConditionTone } from '../resource-utils-crossplane'
import { CrossplanePauseNotice } from './CrossplanePauseContext'
import type { HealthLevel } from '../resource-utils'
import { splitCamelCase } from '../../../utils/format'

interface GenericRendererProps {
  data: any
}

export function GenericRenderer({ data }: GenericRendererProps) {
  const spec = data.spec || {}
  const status = data.status || {}
  const conditions = status.conditions

  // Determine health status from conditions or status fields
  const acknowledgedPause = getCrossplanePauseState(data) === 'acknowledged'
  const healthStatus = getHealthStatus(data)

  // Extract important fields from spec and status
  const statusFields = getImportantFields(status, 'status')
  const specFields = getImportantFields(spec, 'spec')

  // Get nested objects for separate display
  const nestedSpecObjects = getNestedObjects(spec)
  const nestedStatusObjects = getNestedObjects(status)

  // Check if resource has meaningful content
  const hasStatusContent = Object.keys(statusFields).length > 0 || (conditions && conditions.length > 0)
  const hasSpecContent = Object.keys(specFields).length > 0 || nestedSpecObjects.length > 0

  return (
    <>
      {acknowledgedPause && <CrossplanePauseNotice data={data} />}
      {/* Health Status Banner (if determinable) */}
      {healthStatus && (
        <AlertBanner
          variant={BANNER_VARIANT[healthStatus.level]}
          icon={BANNER_ICON[healthStatus.level]}
          title={healthStatus.label}
          message={healthStatus.message}
        />
      )}

      {/* Empty state hint */}
      {!hasStatusContent && !hasSpecContent && (
        <div className="mb-4 p-4 bg-slate-500/10 border border-slate-500/20 rounded-lg">
          <div className="flex items-start gap-3">
            <FileCode className="w-5 h-5 text-slate-400 mt-0.5 shrink-0" />
            <div>
              <div className="text-sm font-medium text-theme-text-secondary">Custom Resource</div>
              <div className="text-xs text-theme-text-tertiary mt-1">
                This resource type doesn&apos;t have a dedicated view. Use the YAML view (code icon in header) to see the full resource definition.
              </div>
            </div>
          </div>
        </div>
      )}

      {/* Status Section */}
      {Object.keys(statusFields).length > 0 && (
        <Section title="Status" icon={Info} defaultExpanded>
          <PropertyList>
            {Object.entries(statusFields).map(([key, value]) => (
              <Property key={key} label={formatFieldName(key)} value={formatValue(value)} />
            ))}
          </PropertyList>
        </Section>
      )}

      {/* Conditions (common in CRDs) */}
      {conditions && Array.isArray(conditions) && conditions.length > 0 && (
        <GenericConditionsSection conditions={conditions} resource={data} />
      )}

      {/* Nested Status Objects */}
      {nestedStatusObjects.length > 0 && (
        <Section title="Status Details" icon={Layers} defaultExpanded={false}>
          <div className="space-y-2">
            {nestedStatusObjects.map(({ key, value }) => (
              <NestedObjectViewer key={key} name={formatFieldName(key)} value={value} />
            ))}
          </div>
        </Section>
      )}

      {/* Spec Section */}
      {Object.keys(specFields).length > 0 && (
        <Section title="Specification" defaultExpanded={Object.keys(specFields).length <= 8}>
          <PropertyList>
            {Object.entries(specFields).map(([key, value]) => (
              <Property key={key} label={formatFieldName(key)} value={formatValue(value)} />
            ))}
          </PropertyList>
        </Section>
      )}

      {/* Nested Spec Objects */}
      {nestedSpecObjects.length > 0 && (
        <Section title="Specification Details" icon={Layers} defaultExpanded={false}>
          <div className="space-y-2">
            {nestedSpecObjects.map(({ key, value }) => (
              <NestedObjectViewer key={key} name={formatFieldName(key)} value={value} />
            ))}
          </div>
        </Section>
      )}
    </>
  )
}

// ============================================================================
// HEALTH STATUS DETECTION
// ============================================================================

interface HealthStatusResult {
  level: HealthLevel
  label: string
  message?: string
}

// The shared banner carries light and dark pairs for each tone. `alert` keeps
// its own orange step between degraded and unhealthy; `neutral` reads like
// unknown.
const BANNER_VARIANT = {
  healthy: 'success',
  degraded: 'warning',
  alert: 'alert',
  unhealthy: 'error',
  neutral: 'unknown',
  unknown: 'unknown',
} as const satisfies Record<HealthLevel, string>
const BANNER_ICON: Record<HealthLevel, typeof Info> = {
  healthy: CheckCircle,
  degraded: AlertCircle,
  alert: AlertCircle,
  unhealthy: XCircle,
  neutral: Info,
  unknown: Info,
}

function getHealthStatus(resource: any): HealthStatusResult | null {
  if (resource?.kind === 'InferenceService' && isApiGroup(resource?.apiVersion, 'serving.kserve.io') && isInferenceServiceStopRequested(resource)) {
    const status = getInferenceServiceStatus(resource)
    if (status.level === 'neutral') return { level: 'neutral', label: 'Stopped', message: "This model is stopped and isn't serving requests." }
    return { level: status.level, label: status.text }
  }
  const agentRuntime = getAgentRuntimeStatus(resource)
  if (agentRuntime) return { level: agentRuntime.level, label: agentRuntime.text, message: agentRuntime.message }
  const acknowledgedPause = getCrossplanePauseState(resource) === 'acknowledged'
  const derived = acknowledgedPause ? getCrossplaneIndependentStatus(resource) : getGenericResourceStatus(resource)
  if (!derived || (acknowledgedPause && derived.tone === 'healthy')) return null
  return { level: derived.tone, label: derived.text, message: derived.reason }
}

// ============================================================================
// FIELD EXTRACTION
// ============================================================================

function getImportantFields(obj: any, context: string): Record<string, any> {
  const result: Record<string, any> = {}
  if (!obj || typeof obj !== 'object') return result

  // Priority fields to show first
  const priorityFields = context === 'status'
    ? ['phase', 'state', 'ready', 'available', 'replicas', 'currentReplicas', 'readyReplicas',
       'availableReplicas', 'updatedReplicas', 'observedGeneration', 'message', 'reason']
    : ['replicas', 'selector', 'schedule', 'suspend', 'concurrencyPolicy', 'parallelism',
       'completions', 'backoffLimit', 'image', 'command', 'args', 'ports', 'resources']

  // First add priority fields
  for (const field of priorityFields) {
    if (obj[field] !== undefined && !isComplexObject(obj[field])) {
      result[field] = obj[field]
    }
  }

  // Then add other simple fields
  for (const [key, value] of Object.entries(obj)) {
    if (result[key] !== undefined) continue
    if (key === 'conditions') continue
    if (key === 'managedFields') continue
    if (isComplexObject(value)) continue

    result[key] = value
  }

  return result
}

function getNestedObjects(obj: any): Array<{ key: string; value: any }> {
  const result: Array<{ key: string; value: any }> = []
  if (!obj || typeof obj !== 'object') return result

  for (const [key, value] of Object.entries(obj)) {
    if (key === 'conditions') continue
    if (key === 'managedFields') continue
    if (isComplexObject(value)) {
      result.push({ key, value })
    }
  }

  return result
}

function isComplexObject(value: any): boolean {
  if (value === null || value === undefined) return false
  if (typeof value !== 'object') return false
  if (Array.isArray(value)) {
    return value.some(item => typeof item === 'object' && item !== null)
  }
  return true
}

function formatFieldName(name: string): string {
  return splitCamelCase(name).replace(/^./, str => str.toUpperCase())
}

function formatValue(value: any): string {
  if (value === null || value === undefined) return '-'
  if (typeof value === 'boolean') return value ? 'Yes' : 'No'
  if (Array.isArray(value)) return value.join(', ')
  if (typeof value === 'object') return JSON.stringify(value)
  return String(value)
}

// ============================================================================
// NESTED OBJECT VIEWER
// ============================================================================

interface NestedObjectViewerProps {
  name: string
  value: any
  depth?: number
}

function NestedObjectViewer({ name, value, depth = 0 }: NestedObjectViewerProps) {
  const [expanded, setExpanded] = useState(depth < 1)
  const { panelId, buttonProps } = useDisclosure(expanded)

  if (Array.isArray(value)) {
    return (
      <div className="text-sm">
        <button
          {...buttonProps}
          onClick={() => setExpanded(!expanded)}
          className="flex items-center gap-1 text-theme-text-secondary hover:text-theme-text-primary transition-colors"
        >
          <CollapseChevron open={expanded} className="w-3.5 h-3.5" />
          <span className="font-medium">{name}</span>
          <span className="text-xs text-theme-text-tertiary">({value.length} items)</span>
        </button>
        {/* Recursive and unbounded: closed branches unmount so a deep object
            costs only the rows the user has actually opened. */}
        <Collapse open={expanded} unmountOnExit id={panelId}>
          <div className="ml-5 mt-1 space-y-1">
            {value.map((item, i) => {
              if (typeof item === 'object' && item !== null) {
                return <NestedObjectViewer key={i} name={`[${i}]`} value={item} depth={depth + 1} />
              }
              return (
                <div key={i} className="text-xs text-theme-text-secondary">
                  [{i}]: {String(item)}
                </div>
              )
            })}
          </div>
        </Collapse>
      </div>
    )
  }

  if (typeof value === 'object' && value !== null) {
    const entries = Object.entries(value)
    const simpleEntries = entries.filter(([, v]) => !isComplexObject(v))
    const complexEntries = entries.filter(([, v]) => isComplexObject(v))

    return (
      <div className="text-sm">
        <button
          {...buttonProps}
          onClick={() => setExpanded(!expanded)}
          className="flex items-center gap-1 text-theme-text-secondary hover:text-theme-text-primary transition-colors"
        >
          <CollapseChevron open={expanded} className="w-3.5 h-3.5" />
          <span className="font-medium">{name}</span>
          <span className="text-xs text-theme-text-tertiary">({entries.length} fields)</span>
        </button>
        <Collapse open={expanded} unmountOnExit id={panelId}>
          <div className="ml-5 mt-1 space-y-1">
            {simpleEntries.map(([k, v]) => (
              <div key={k} className="flex items-start gap-2 text-xs">
                <span className="text-theme-text-tertiary w-40 shrink-0">{formatFieldName(k)}</span>
                <span className="text-theme-text-secondary break-all">{formatValue(v)}</span>
              </div>
            ))}
            {complexEntries.map(([k, v]) => (
              <NestedObjectViewer key={k} name={formatFieldName(k)} value={v} depth={depth + 1} />
            ))}
          </div>
        </Collapse>
      </div>
    )
  }

  return (
    <div className="text-xs text-theme-text-secondary">
      {name}: {String(value)}
    </div>
  )
}

// ============================================================================
// CONDITIONS SECTION
// ============================================================================

// KServe's Stopped reports an intentional state rather than health: True for a
// model its owner stopped, False otherwise. While it is True, readiness that is
// False only because of the stop follows it.
function isKServeStopState(resource: any, cond: any): boolean {
  if (!isApiGroup(resource?.apiVersion, 'serving.kserve.io')) return false
  if (cond.type === 'Stopped') return true
  return cond.status === 'False' && cond.reason === 'Stopped' && isInferenceServiceStopRequested(resource)
}
const WARNING_CONDITION_TYPES = new Set(['Degraded', 'Warning', 'ScalingLimited'])
const POSITIVE_CONDITION_TYPES = new Set(['Ready', 'Available', 'Healthy', 'Initialized', 'Complete'])

export type GenericConditionTone = 'neutral' | 'ok' | 'warning' | 'fail' | 'unclear' | 'unknown'

// One tone per condition, so the icon, the status chip, the sort order and
// whether the section opens all agree. Unknown-polarity False is 'unclear'
// (slate icon, red chip): many such conditions are positive, like
// PredictorReady, and their False is a real failure. An Unknown status is not
// a failure, so its chip isn't red.
export function genericConditionTone(resource: any, cond: any): GenericConditionTone {
  if (getCrossplaneConditionTone(resource, cond) === 'neutral') return 'neutral'
  if (cond.status !== 'True' && cond.status !== 'False') return 'unknown'
  if (isKServeStopState(resource, cond)) return 'neutral'
  if (resource?.kind === 'ApplicationSet' && isApiGroup(resource?.apiVersion, 'argoproj.io')
    && cond.type === 'RolloutProgressing') return 'neutral'
  const isTrue = cond.status === 'True'
  if (WARNING_CONDITION_TYPES.has(cond.type)) return isTrue ? 'warning' : 'ok'
  if (isInvertedPolarityCondition(cond.type)) return isTrue ? 'fail' : 'ok'
  if (isTrue) return 'ok'
  return POSITIVE_CONDITION_TYPES.has(cond.type) ? 'fail' : 'unclear'
}

const isProblemTone = (tone: GenericConditionTone) => tone === 'fail' || tone === 'warning' || tone === 'unclear'

function GenericConditionsSection({ conditions, resource }: { conditions: any[]; resource: any }) {
  // Sort conditions: problematic first, then alphabetically
  const sortedConditions = [...conditions].sort((a, b) => {
    const aProblematic = isProblemTone(genericConditionTone(resource, a))
    const bProblematic = isProblemTone(genericConditionTone(resource, b))
    if (aProblematic && !bProblematic) return -1
    if (!aProblematic && bProblematic) return 1
    return (a.type || '').localeCompare(b.type || '')
  })

  const hasProblems = sortedConditions.some(c => isProblemTone(genericConditionTone(resource, c)))

  return (
    <Section title={`Conditions (${conditions.length})`} defaultExpanded={hasProblems || conditions.length <= 5}>
      <div className="space-y-2">
        {sortedConditions.map((cond: any, index: number) => {
          const tone = genericConditionTone(resource, cond)
          const iconColor = {
            neutral: 'text-theme-text-tertiary',
            ok: 'text-green-400',
            warning: 'text-yellow-400',
            fail: 'text-red-400',
            unclear: 'text-slate-400',
            unknown: 'text-yellow-400',
          }[tone]
          const chipRed = tone === 'fail' || tone === 'unclear'

          return (
            <div key={`${cond.type}-${index}`} className="flex items-start gap-2 text-sm">
              <span className={clsx('w-5 h-5 rounded-full flex items-center justify-center shrink-0 mt-0.5', iconColor)}>
                {tone === 'neutral' ? <Minus className="w-4 h-4" /> :
                 tone === 'ok' ? <CheckCircle className="w-4 h-4" /> :
                 tone === 'warning' ? <AlertTriangle className="w-4 h-4" /> :
                 tone === 'fail' ? <XCircle className="w-4 h-4" /> :
                 <AlertCircle className="w-4 h-4" />}
              </span>
              <div className="flex-1 min-w-0">
                <div className="flex items-center gap-2 flex-wrap">
                  <span className="text-theme-text-primary font-medium">{cond.type}</span>
                  <span className={clsx(
                    'badge-sm',
                    chipRed ? 'status-red' : 'bg-theme-elevated text-theme-text-secondary'
                  )}>
                    {cond.status}
                  </span>
                  {cond.reason && (
                    <span className="text-xs text-theme-text-tertiary">({cond.reason})</span>
                  )}
                </div>
                {cond.message && (
                  <div className="text-xs text-theme-text-secondary mt-0.5 break-words">
                    {cond.message}
                  </div>
                )}
                {cond.lastTransitionTime && (
                  <div className="text-xs text-theme-text-tertiary mt-0.5">
                    Last transition: {formatTimestamp(cond.lastTransitionTime)}
                  </div>
                )}
              </div>
            </div>
          )
        })}
      </div>
    </Section>
  )
}

function formatTimestamp(timestamp: string): string {
  try {
    const date = new Date(timestamp)
    const now = new Date()
    const diffMs = now.getTime() - date.getTime()
    const diffMins = Math.floor(diffMs / 60000)
    const diffHours = Math.floor(diffMins / 60)
    const diffDays = Math.floor(diffHours / 24)

    if (diffMins < 1) return 'just now'
    if (diffMins < 60) return `${diffMins}m ago`
    if (diffHours < 24) return `${diffHours}h ago`
    if (diffDays < 7) return `${diffDays}d ago`
    return date.toLocaleDateString()
  } catch {
    return timestamp
  }
}
