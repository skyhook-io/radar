import { useState } from 'react'
import { Info, CheckCircle, XCircle, AlertCircle, ChevronRight, ChevronDown, FileCode, AlertTriangle, Layers } from 'lucide-react'
import { clsx } from 'clsx'
import { Section, PropertyList, Property } from '../../ui/drawer-components'

interface GenericRendererProps {
  data: any
}

export function GenericRenderer({ data }: GenericRendererProps) {
  const spec = data.spec || {}
  const status = data.status || {}
  const conditions = status.conditions

  // Determine health status from conditions or status fields
  const healthStatus = getHealthStatus(status, conditions)

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
      {/* Health Status Banner (if determinable) */}
      {healthStatus && (
        <div className={clsx(
          'mb-4 p-3 rounded-lg border flex items-center gap-3',
          healthStatus.type === 'healthy' && 'bg-green-500/10 border-green-500/30',
          healthStatus.type === 'degraded' && 'bg-yellow-500/10 border-yellow-500/30',
          healthStatus.type === 'unhealthy' && 'bg-red-500/10 border-red-500/30',
          healthStatus.type === 'unknown' && 'bg-slate-500/10 border-slate-500/30'
        )}>
          {healthStatus.type === 'healthy' && <CheckCircle className="w-5 h-5 text-green-400 shrink-0" />}
          {healthStatus.type === 'degraded' && <AlertCircle className="w-5 h-5 text-yellow-400 shrink-0" />}
          {healthStatus.type === 'unhealthy' && <XCircle className="w-5 h-5 text-red-400 shrink-0" />}
          {healthStatus.type === 'unknown' && <Info className="w-5 h-5 text-slate-400 shrink-0" />}
          <div>
            <div className={clsx(
              'text-sm font-medium',
              healthStatus.type === 'healthy' && 'text-green-400',
              healthStatus.type === 'degraded' && 'text-yellow-400',
              healthStatus.type === 'unhealthy' && 'text-red-400',
              healthStatus.type === 'unknown' && 'text-slate-400'
            )}>
              {healthStatus.label}
            </div>
            {healthStatus.message && (
              <div className="text-xs text-theme-text-secondary mt-0.5">{healthStatus.message}</div>
            )}
          </div>
        </div>
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
        <GenericConditionsSection conditions={conditions} />
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

type HealthType = 'healthy' | 'degraded' | 'unhealthy' | 'unknown'

interface HealthStatusResult {
  type: HealthType
  label: string
  message?: string
}

function getHealthStatus(status: any, conditions?: any[]): HealthStatusResult | null {
  if (!status) return null

  // Check phase first (common pattern)
  if (status.phase) {
    const phase = String(status.phase)
    const healthyPhases = ['Running', 'Active', 'Succeeded', 'Ready', 'Healthy', 'Available', 'Bound', 'Complete']
    const degradedPhases = ['Pending', 'Progressing', 'Unknown', 'Terminating', 'Waiting']
    const unhealthyPhases = ['Failed', 'Error', 'CrashLoopBackOff', 'ImagePullBackOff', 'ErrImagePull']

    if (healthyPhases.includes(phase)) {
      return { type: 'healthy', label: phase }
    }
    if (degradedPhases.includes(phase)) {
      return { type: 'degraded', label: phase, message: status.message || status.reason }
    }
    if (unhealthyPhases.includes(phase)) {
      return { type: 'unhealthy', label: phase, message: status.message || status.reason }
    }
  }

  // Check conditions
  if (conditions && Array.isArray(conditions) && conditions.length > 0) {
    // Look for Ready or Available condition
    const readyCondition = conditions.find((c: any) =>
      c.type === 'Ready' || c.type === 'Available' || c.type === 'Healthy'
    )
    if (readyCondition) {
      if (readyCondition.status === 'True') {
        return { type: 'healthy', label: 'Ready' }
      }
      if (readyCondition.status === 'False') {
        return {
          type: 'unhealthy',
          label: 'Not Ready',
          message: readyCondition.message || readyCondition.reason
        }
      }
    }

    // Check for any False conditions that indicate problems
    const problemConditions = conditions.filter((c: any) =>
      c.status === 'False' && ['Ready', 'Available', 'Healthy', 'Initialized'].includes(c.type)
    )
    if (problemConditions.length > 0) {
      const problem = problemConditions[0]
      return {
        type: 'unhealthy',
        label: `${problem.type}: False`,
        message: problem.message || problem.reason
      }
    }

    // Check for warning conditions
    const warningConditions = conditions.filter((c: any) =>
      c.status === 'True' && ['Degraded', 'Warning', 'ScalingLimited'].includes(c.type)
    )
    if (warningConditions.length > 0) {
      const warning = warningConditions[0]
      return {
        type: 'degraded',
        label: warning.type,
        message: warning.message || warning.reason
      }
    }
  }

  // Check replica-based status
  if (status.replicas !== undefined) {
    const desired = status.replicas
    const ready = status.readyReplicas || status.availableReplicas || 0
    if (desired > 0 && ready >= desired) {
      return { type: 'healthy', label: `${ready}/${desired} Ready` }
    }
    if (desired > 0 && ready > 0) {
      return { type: 'degraded', label: `${ready}/${desired} Ready` }
    }
    if (desired > 0 && ready === 0) {
      return { type: 'unhealthy', label: `0/${desired} Ready` }
    }
  }

  return null
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
  return name
    .replace(/([A-Z])/g, ' $1')
    .replace(/^./, str => str.toUpperCase())
    .trim()
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

  if (Array.isArray(value)) {
    return (
      <div className="text-sm">
        <button
          onClick={() => setExpanded(!expanded)}
          className="flex items-center gap-1 text-theme-text-secondary hover:text-theme-text-primary transition-colors"
        >
          {expanded ? <ChevronDown className="w-3.5 h-3.5" /> : <ChevronRight className="w-3.5 h-3.5" />}
          <span className="font-medium">{name}</span>
          <span className="text-xs text-theme-text-tertiary">({value.length} items)</span>
        </button>
        {expanded && (
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
        )}
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
          onClick={() => setExpanded(!expanded)}
          className="flex items-center gap-1 text-theme-text-secondary hover:text-theme-text-primary transition-colors"
        >
          {expanded ? <ChevronDown className="w-3.5 h-3.5" /> : <ChevronRight className="w-3.5 h-3.5" />}
          <span className="font-medium">{name}</span>
          <span className="text-xs text-theme-text-tertiary">({entries.length} fields)</span>
        </button>
        {expanded && (
          <div className="ml-5 mt-1 space-y-1">
            {simpleEntries.map(([k, v]) => (
              <div key={k} className="flex items-start gap-2 text-xs">
                <span className="text-theme-text-tertiary w-28 shrink-0">{formatFieldName(k)}</span>
                <span className="text-theme-text-secondary break-all">{formatValue(v)}</span>
              </div>
            ))}
            {complexEntries.map(([k, v]) => (
              <NestedObjectViewer key={k} name={formatFieldName(k)} value={v} depth={depth + 1} />
            ))}
          </div>
        )}
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

function GenericConditionsSection({ conditions }: { conditions: any[] }) {
  // Sort conditions: problematic first, then alphabetically
  const sortedConditions = [...conditions].sort((a, b) => {
    const aProblematic = a.status === 'False' || (a.status === 'True' && ['Degraded', 'Warning'].includes(a.type))
    const bProblematic = b.status === 'False' || (b.status === 'True' && ['Degraded', 'Warning'].includes(b.type))
    if (aProblematic && !bProblematic) return -1
    if (!aProblematic && bProblematic) return 1
    return (a.type || '').localeCompare(b.type || '')
  })

  // Check for any problems
  const hasProblems = sortedConditions.some(c =>
    c.status === 'False' || (c.status === 'True' && ['Degraded', 'Warning'].includes(c.type))
  )

  return (
    <Section title={`Conditions (${conditions.length})`} defaultExpanded={hasProblems || conditions.length <= 5}>
      <div className="space-y-2">
        {sortedConditions.map((cond: any, index: number) => {
          const isTrue = cond.status === 'True'
          const isFalse = cond.status === 'False'
          const isWarningType = ['Degraded', 'Warning', 'ScalingLimited'].includes(cond.type)
          const isPositiveType = ['Ready', 'Available', 'Healthy', 'Initialized', 'Complete'].includes(cond.type)

          // Determine icon color based on condition semantics
          let iconColor = 'text-yellow-400'
          if (isTrue && isPositiveType) iconColor = 'text-green-400'
          else if (isTrue && isWarningType) iconColor = 'text-yellow-400'
          else if (isTrue) iconColor = 'text-green-400'
          else if (isFalse && isPositiveType) iconColor = 'text-red-400'
          else if (isFalse) iconColor = 'text-slate-400'

          return (
            <div key={`${cond.type}-${index}`} className="flex items-start gap-2 text-sm">
              <span className={clsx('w-5 h-5 rounded-full flex items-center justify-center shrink-0 mt-0.5', iconColor)}>
                {isTrue && isPositiveType ? <CheckCircle className="w-4 h-4" /> :
                 isTrue && isWarningType ? <AlertTriangle className="w-4 h-4" /> :
                 isTrue ? <CheckCircle className="w-4 h-4" /> :
                 isFalse && isPositiveType ? <XCircle className="w-4 h-4" /> :
                 isFalse ? <AlertCircle className="w-4 h-4" /> :
                 <AlertCircle className="w-4 h-4" />}
              </span>
              <div className="flex-1 min-w-0">
                <div className="flex items-center gap-2 flex-wrap">
                  <span className="text-theme-text-primary font-medium">{cond.type}</span>
                  <span className={clsx(
                    'badge-sm',
                    isTrue ? 'bg-theme-elevated text-theme-text-secondary' : 'bg-red-500/20 text-red-400'
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
