import { Info, CheckCircle, XCircle, AlertCircle } from 'lucide-react'
import { clsx } from 'clsx'
import { Section, PropertyList, Property, ExpandableSection } from '../drawer-components'

interface GenericRendererProps {
  data: any
}

export function GenericRenderer({ data }: GenericRendererProps) {
  const spec = data.spec || {}
  const status = data.status || {}

  // Extract important fields from spec and status
  const specFields = getImportantFields(spec, 'spec')
  const statusFields = getImportantFields(status, 'status')
  const conditions = status.conditions

  return (
    <>
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

      {/* Spec Section */}
      {Object.keys(specFields).length > 0 && (
        <Section title="Specification" defaultExpanded={Object.keys(specFields).length <= 5}>
          <PropertyList>
            {Object.entries(specFields).map(([key, value]) => (
              <Property key={key} label={formatFieldName(key)} value={formatValue(value)} />
            ))}
          </PropertyList>
        </Section>
      )}

      {/* Raw Spec (for complex nested structures) */}
      {hasNestedObjects(spec) && (
        <ExpandableSection title="Full Specification" defaultExpanded={false}>
          <pre className="text-xs text-theme-text-secondary bg-theme-base/50 p-3 rounded overflow-x-auto max-h-64 overflow-y-auto">
            {JSON.stringify(spec, null, 2)}
          </pre>
        </ExpandableSection>
      )}

      {/* Raw Status (for complex nested structures) */}
      {hasNestedObjects(status) && Object.keys(status).length > 0 && (
        <ExpandableSection title="Full Status" defaultExpanded={false}>
          <pre className="text-xs text-theme-text-secondary bg-theme-base/50 p-3 rounded overflow-x-auto max-h-64 overflow-y-auto">
            {JSON.stringify(status, null, 2)}
          </pre>
        </ExpandableSection>
      )}
    </>
  )
}

// Extract important top-level fields (scalars and simple arrays)
function getImportantFields(obj: any, context: string): Record<string, any> {
  const result: Record<string, any> = {}

  if (!obj || typeof obj !== 'object') return result

  // Priority fields to show first
  const priorityFields = context === 'status'
    ? ['phase', 'state', 'ready', 'available', 'replicas', 'currentReplicas', 'readyReplicas', 'message', 'reason']
    : ['replicas', 'selector', 'template', 'schedule', 'suspend', 'concurrencyPolicy']

  // First add priority fields
  for (const field of priorityFields) {
    if (obj[field] !== undefined && !isComplexObject(obj[field])) {
      result[field] = obj[field]
    }
  }

  // Then add other simple fields
  for (const [key, value] of Object.entries(obj)) {
    if (result[key] !== undefined) continue // Already added
    if (key === 'conditions') continue // Handled separately
    if (isComplexObject(value)) continue // Skip complex objects

    result[key] = value
  }

  return result
}

// Check if a value is a complex object (not a simple scalar or array of scalars)
function isComplexObject(value: any): boolean {
  if (value === null || value === undefined) return false
  if (typeof value !== 'object') return false
  if (Array.isArray(value)) {
    // Arrays of scalars are OK
    return value.some(item => typeof item === 'object' && item !== null)
  }
  // Objects are complex
  return true
}

// Check if object has nested objects (for showing raw JSON)
function hasNestedObjects(obj: any): boolean {
  if (!obj || typeof obj !== 'object') return false
  return Object.values(obj).some(isComplexObject)
}

// Format field names for display (camelCase -> Title Case)
function formatFieldName(name: string): string {
  return name
    .replace(/([A-Z])/g, ' $1')
    .replace(/^./, str => str.toUpperCase())
    .trim()
}

// Format values for display
function formatValue(value: any): string {
  if (value === null || value === undefined) return '-'
  if (typeof value === 'boolean') return value ? 'Yes' : 'No'
  if (Array.isArray(value)) return value.join(', ')
  if (typeof value === 'object') return JSON.stringify(value)
  return String(value)
}

// Generic conditions display (common in many CRDs)
function GenericConditionsSection({ conditions }: { conditions: any[] }) {
  return (
    <Section title={`Conditions (${conditions.length})`} defaultExpanded={conditions.length <= 5}>
      <div className="space-y-2">
        {conditions.map((cond: any, i: number) => {
          const isTrue = cond.status === 'True'
          const isFalse = cond.status === 'False'

          return (
            <div key={i} className="flex items-start gap-2 text-sm">
              <span className={clsx(
                'w-5 h-5 rounded-full flex items-center justify-center shrink-0 mt-0.5',
                isTrue ? 'text-green-400' :
                isFalse ? 'text-red-400' :
                'text-yellow-400'
              )}>
                {isTrue ? <CheckCircle className="w-4 h-4" /> :
                 isFalse ? <XCircle className="w-4 h-4" /> :
                 <AlertCircle className="w-4 h-4" />}
              </span>
              <div className="flex-1 min-w-0">
                <div className="flex items-center gap-2">
                  <span className="text-theme-text-primary font-medium">{cond.type}</span>
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
