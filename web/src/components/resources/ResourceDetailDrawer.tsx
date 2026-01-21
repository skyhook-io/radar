import { useState, useCallback, useEffect, useRef } from 'react'
import {
  X,
  Copy,
  Check,
  RefreshCw,
  Terminal,
  FileText,
  ExternalLink,
  Trash2,
  Play,
  Pause,
  Code,
} from 'lucide-react'
import { clsx } from 'clsx'
import { useResource, useResourceEvents } from '../../api/client'
import type { SelectedResource, Relationships, ResourceRef } from '../../types'
import {
  getPodStatus,
  getWorkloadStatus,
  getJobStatus,
  getCronJobStatus,
  getHPAStatus,
  getServiceStatus,
} from './resource-utils'
import {
  LabelsSection,
  AnnotationsSection,
  MetadataSection,
  EventsSection,
  RelatedResourcesSection,
  getKindColor,
  formatKindName,
} from './drawer-components'
import {
  PodRenderer,
  WorkloadRenderer,
  ReplicaSetRenderer,
  ServiceRenderer,
  IngressRenderer,
  ConfigMapRenderer,
  SecretRenderer,
  JobRenderer,
  CronJobRenderer,
  HPARenderer,
  GenericRenderer,
} from './renderers'

interface ResourceDetailDrawerProps {
  resource: SelectedResource
  onClose: () => void
  onNavigate?: (resource: SelectedResource) => void
}

const MIN_WIDTH = 400
const MAX_WIDTH_PERCENT = 0.7
const DEFAULT_WIDTH = 550

export function ResourceDetailDrawer({ resource, onClose, onNavigate }: ResourceDetailDrawerProps) {
  const [copied, setCopied] = useState<string | null>(null)
  const [showYaml, setShowYaml] = useState(false)
  const [drawerWidth, setDrawerWidth] = useState(DEFAULT_WIDTH)
  const [isResizing, setIsResizing] = useState(false)
  const resizeStartX = useRef(0)
  const resizeStartWidth = useRef(DEFAULT_WIDTH)

  const { data: resourceData, relationships, isLoading, refetch, isRefetching } = useResource<any>(
    resource.kind,
    resource.namespace,
    resource.name
  )

  // Navigate to a related resource
  const handleNavigateToRelated = useCallback((ref: ResourceRef) => {
    if (onNavigate) {
      // Convert kind to plural form for API consistency
      const kindToPlural: Record<string, string> = {
        pod: 'pods', service: 'services', deployment: 'deployments',
        daemonset: 'daemonsets', statefulset: 'statefulsets', replicaset: 'replicasets',
        ingress: 'ingresses', configmap: 'configmaps', secret: 'secrets',
        job: 'jobs', cronjob: 'cronjobs', hpa: 'hpas',
      }
      const pluralKind = kindToPlural[ref.kind.toLowerCase()] || ref.kind.toLowerCase()
      onNavigate({ kind: pluralKind, namespace: ref.namespace, name: ref.name })
    }
  }, [onNavigate])

  // ESC key handler
  useEffect(() => {
    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') onClose()
    }
    document.addEventListener('keydown', handleKeyDown)
    return () => document.removeEventListener('keydown', handleKeyDown)
  }, [onClose])

  // Resize handlers
  const handleResizeStart = useCallback((e: React.MouseEvent) => {
    e.preventDefault()
    setIsResizing(true)
    resizeStartX.current = e.clientX
    resizeStartWidth.current = drawerWidth
  }, [drawerWidth])

  useEffect(() => {
    if (!isResizing) return

    // Set body cursor during resize for better UX
    document.body.style.cursor = 'ew-resize'
    document.body.style.userSelect = 'none'

    const maxWidth = window.innerWidth * MAX_WIDTH_PERCENT
    const handleMouseMove = (e: MouseEvent) => {
      const deltaX = resizeStartX.current - e.clientX
      const newWidth = resizeStartWidth.current + deltaX
      setDrawerWidth(Math.max(MIN_WIDTH, Math.min(newWidth, maxWidth)))
    }
    const handleMouseUp = () => setIsResizing(false)
    document.addEventListener('mousemove', handleMouseMove)
    document.addEventListener('mouseup', handleMouseUp)
    return () => {
      document.body.style.cursor = ''
      document.body.style.userSelect = ''
      document.removeEventListener('mousemove', handleMouseMove)
      document.removeEventListener('mouseup', handleMouseUp)
    }
  }, [isResizing])

  const copyToClipboard = useCallback((text: string, key: string) => {
    navigator.clipboard.writeText(text)
    setCopied(key)
    setTimeout(() => setCopied(null), 2000)
  }, [])

  const headerHeight = 49

  return (
    <div
      className="fixed right-0 bg-theme-surface border-l border-theme-border flex flex-col shadow-2xl z-40"
      style={{ width: drawerWidth, top: headerHeight, height: `calc(100vh - ${headerHeight}px)` }}
    >
      {/* Resize handle - wider for easier grab, hidden on mobile */}
      <div
        onMouseDown={handleResizeStart}
        className={clsx(
          'absolute left-0 top-0 bottom-0 w-2 cursor-ew-resize z-10 hover:bg-blue-500/50 transition-colors',
          'hidden sm:block', // Hide on mobile
          isResizing && 'bg-blue-500/50'
        )}
      />

      {/* Header */}
      <DrawerHeader
        resource={resource}
        resourceData={resourceData}
        showYaml={showYaml}
        setShowYaml={setShowYaml}
        isRefetching={isRefetching}
        onRefetch={refetch}
        onClose={onClose}
        onCopy={(text) => copyToClipboard(text, 'name')}
        copied={copied === 'name'}
      />

      {/* Content */}
      <div className="flex-1 overflow-y-auto">
        {isLoading ? (
          <div className="flex items-center justify-center h-32 text-theme-text-tertiary">Loading...</div>
        ) : !resourceData ? (
          <div className="flex items-center justify-center h-32 text-theme-text-tertiary">Resource not found</div>
        ) : showYaml ? (
          <YamlView data={resourceData} onCopy={(text) => copyToClipboard(text, 'yaml')} copied={copied === 'yaml'} />
        ) : (
          <ResourceContent
            resource={resource}
            data={resourceData}
            relationships={relationships}
            onCopy={copyToClipboard}
            copied={copied}
            onNavigate={handleNavigateToRelated}
          />
        )}
      </div>
    </div>
  )
}

// ============================================================================
// DRAWER HEADER
// ============================================================================

interface DrawerHeaderProps {
  resource: SelectedResource
  resourceData: any
  showYaml: boolean
  setShowYaml: (show: boolean) => void
  isRefetching: boolean
  onRefetch: () => void
  onClose: () => void
  onCopy: (text: string) => void
  copied: boolean
}

function DrawerHeader({ resource, resourceData, showYaml, setShowYaml, isRefetching, onRefetch, onClose, onCopy, copied }: DrawerHeaderProps) {
  const status = getResourceStatus(resource.kind, resourceData)

  return (
    <div className="border-b border-theme-border shrink-0">
      {/* Top row: badges and controls */}
      <div className="flex items-center justify-between px-4 pt-3 pb-2">
        <div className="flex items-center gap-2 flex-wrap">
          <span className={clsx('px-2 py-0.5 text-xs font-medium rounded border', getKindColor(resource.kind))}>
            {formatKindName(resource.kind)}
          </span>
          {status && (
            <span className={clsx('px-2 py-0.5 text-xs font-medium rounded', status.color)}>
              {status.text}
            </span>
          )}
        </div>
        <div className="flex items-center gap-1">
          <button
            onClick={() => setShowYaml(!showYaml)}
            className={clsx(
              'px-2 py-1 text-xs rounded transition-colors',
              showYaml ? 'bg-blue-500 text-theme-text-primary' : 'text-theme-text-secondary hover:text-theme-text-primary hover:bg-theme-elevated'
            )}
            title="Toggle YAML view"
          >
            <Code className="w-3.5 h-3.5" />
          </button>
          <button
            onClick={() => onRefetch()}
            disabled={isRefetching}
            className="p-1.5 text-theme-text-secondary hover:text-theme-text-primary hover:bg-theme-elevated rounded disabled:opacity-50"
            title="Refresh"
          >
            <RefreshCw className={clsx('w-4 h-4', isRefetching && 'animate-spin')} />
          </button>
          <button onClick={onClose} className="p-1.5 text-theme-text-secondary hover:text-theme-text-primary hover:bg-theme-elevated rounded" title="Close (Esc)">
            <X className="w-4 h-4" />
          </button>
        </div>
      </div>

      {/* Name and namespace */}
      <div className="px-4 pb-3">
        <div className="flex items-center gap-2">
          <h2 className="text-lg font-semibold text-theme-text-primary truncate">{resource.name}</h2>
          <button
            onClick={() => onCopy(resource.name)}
            className="p-1 text-theme-text-secondary hover:text-theme-text-primary hover:bg-theme-elevated rounded shrink-0"
            title="Copy name"
          >
            {copied ? <Check className="w-3.5 h-3.5 text-green-400" /> : <Copy className="w-3.5 h-3.5" />}
          </button>
        </div>
        <p className="text-sm text-theme-text-tertiary">{resource.namespace}</p>
      </div>

      {/* Actions bar */}
      <ActionsBar resource={resource} data={resourceData} />
    </div>
  )
}

// ============================================================================
// ACTIONS BAR
// ============================================================================

import { useToast } from '../ui/Toast'

function ActionsBar({ resource, data }: { resource: SelectedResource; data: any }) {
  const { showCopied } = useToast()
  const kind = resource.kind.toLowerCase()
  const actions: Array<{ icon: any; label: string; command: string; commandLabel: string; disabled?: boolean }> = []

  // Pod-specific actions
  if (kind === 'pods') {
    actions.push({
      icon: FileText,
      label: 'Logs',
      command: `kubectl logs ${resource.name} -n ${resource.namespace} -f`,
      commandLabel: 'Logs command copied',
    })
    actions.push({
      icon: Terminal,
      label: 'Shell',
      command: `kubectl exec -it ${resource.name} -n ${resource.namespace} -- sh`,
      commandLabel: 'Shell command copied',
    })
  }

  // Service port-forward
  if (kind === 'services') {
    const port = data?.spec?.ports?.[0]?.port
    if (port) {
      actions.push({
        icon: ExternalLink,
        label: 'Port Forward',
        command: `kubectl port-forward svc/${resource.name} ${port}:${port} -n ${resource.namespace}`,
        commandLabel: 'Port-forward command copied',
      })
    }
  }

  // Deployment/StatefulSet/DaemonSet restart
  if (['deployments', 'statefulsets', 'daemonsets'].includes(kind)) {
    actions.push({
      icon: RefreshCw,
      label: 'Restart',
      command: `kubectl rollout restart ${kind.slice(0, -1)} ${resource.name} -n ${resource.namespace}`,
      commandLabel: 'Restart command copied',
    })
  }

  // CronJob trigger
  if (kind === 'cronjobs') {
    actions.push({
      icon: Play,
      label: 'Trigger',
      command: `kubectl create job --from=cronjob/${resource.name} ${resource.name}-manual-$(date +%s) -n ${resource.namespace}`,
      commandLabel: 'Trigger command copied',
    })
    const suspended = data?.spec?.suspend
    actions.push({
      icon: suspended ? Play : Pause,
      label: suspended ? 'Resume' : 'Suspend',
      command: `kubectl patch cronjob ${resource.name} -n ${resource.namespace} -p '{"spec":{"suspend":${!suspended}}}'`,
      commandLabel: `${suspended ? 'Resume' : 'Suspend'} command copied`,
    })
  }

  // Job logs
  if (kind === 'jobs') {
    actions.push({
      icon: FileText,
      label: 'Logs',
      command: `kubectl logs job/${resource.name} -n ${resource.namespace} -f`,
      commandLabel: 'Logs command copied',
    })
  }

  // Delete action for all
  const kindSingular = kind.endsWith('s') ? kind.slice(0, -1) : kind
  actions.push({
    icon: Trash2,
    label: 'Delete',
    command: `kubectl delete ${kindSingular} ${resource.name} -n ${resource.namespace}`,
    commandLabel: 'Delete command copied',
  })

  if (actions.length === 0) return null

  return (
    <div className="flex items-center gap-1 px-4 pb-3 flex-wrap">
      {actions.map((action, i) => (
        <button
          key={i}
          onClick={(e) => showCopied(action.command, action.commandLabel, e)}
          disabled={action.disabled}
          className="flex items-center gap-1.5 px-2 py-1 text-xs text-theme-text-secondary hover:text-theme-text-primary hover:bg-theme-elevated rounded transition-colors disabled:opacity-50"
        >
          <action.icon className="w-3.5 h-3.5" />
          {action.label}
        </button>
      ))}
    </div>
  )
}

// ============================================================================
// YAML VIEW
// ============================================================================

import { CodeViewer } from '../ui/CodeViewer'

function YamlView({ data, onCopy, copied }: { data: any; onCopy: (text: string) => void; copied: boolean }) {
  const json = JSON.stringify(data, null, 2)
  return (
    <div className="p-4">
      <div className="flex items-center justify-between mb-2">
        <span className="text-sm font-medium text-theme-text-secondary">Raw JSON</span>
        <button
          onClick={() => onCopy(json)}
          className="flex items-center gap-1 px-2 py-1 text-xs text-theme-text-secondary hover:text-theme-text-primary hover:bg-theme-elevated rounded"
        >
          {copied ? <Check className="w-3.5 h-3.5 text-green-400" /> : <Copy className="w-3.5 h-3.5" />}
          Copy
        </button>
      </div>
      <CodeViewer
        code={json}
        language="json"
        showLineNumbers
        maxHeight="calc(100vh - 250px)"
      />
    </div>
  )
}

// ============================================================================
// RESOURCE CONTENT - Delegates to specific renderers
// ============================================================================

interface ResourceContentProps {
  resource: SelectedResource
  data: any
  relationships?: Relationships
  onCopy: (text: string, key: string) => void
  copied: string | null
  onNavigate?: (ref: ResourceRef) => void
}

function ResourceContent({ resource, data, relationships, onCopy, copied, onNavigate }: ResourceContentProps) {
  const kind = resource.kind.toLowerCase()

  // Fetch events for this resource
  const { data: events, isLoading: eventsLoading } = useResourceEvents(
    resource.kind,
    resource.namespace,
    resource.name
  )

  // Known resource types with specific renderers
  const knownKinds = [
    'pods', 'deployments', 'statefulsets', 'daemonsets', 'replicasets',
    'services', 'ingresses', 'configmaps', 'secrets', 'jobs', 'cronjobs',
    'hpas', 'horizontalpodautoscalers'
  ]
  const isKnownKind = knownKinds.includes(kind)

  return (
    <div className="p-4 space-y-4">
      {/* Kind-specific content - delegates to modular renderers */}
      {kind === 'pods' && <PodRenderer data={data} onCopy={onCopy} copied={copied} />}
      {['deployments', 'statefulsets', 'daemonsets'].includes(kind) && <WorkloadRenderer kind={kind} data={data} />}
      {kind === 'replicasets' && <ReplicaSetRenderer data={data} />}
      {kind === 'services' && <ServiceRenderer data={data} onCopy={onCopy} copied={copied} />}
      {kind === 'ingresses' && <IngressRenderer data={data} />}
      {kind === 'configmaps' && <ConfigMapRenderer data={data} />}
      {kind === 'secrets' && <SecretRenderer data={data} />}
      {kind === 'jobs' && <JobRenderer data={data} />}
      {kind === 'cronjobs' && <CronJobRenderer data={data} />}
      {(kind === 'hpas' || kind === 'horizontalpodautoscalers') && <HPARenderer data={data} />}

      {/* Generic renderer for CRDs and unknown resource types */}
      {!isKnownKind && <GenericRenderer data={data} />}

      {/* Related Resources - clickable links to related items */}
      <RelatedResourcesSection relationships={relationships} onNavigate={onNavigate} />

      {/* Related Events - valuable for debugging */}
      <EventsSection events={events || []} isLoading={eventsLoading} />

      {/* Common sections */}
      <LabelsSection data={data} />
      <AnnotationsSection data={data} />
      <MetadataSection data={data} />
    </div>
  )
}

// ============================================================================
// HELPERS
// ============================================================================

function getResourceStatus(kind: string, data: any): { text: string; color: string } | null {
  if (!data) return null
  const k = kind.toLowerCase()

  // Use the sophisticated status functions from resource-utils
  if (k === 'pods') {
    const status = getPodStatus(data)
    return { text: status.text, color: status.color }
  }

  if (['deployments', 'statefulsets', 'replicasets', 'daemonsets'].includes(k)) {
    const status = getWorkloadStatus(data, k)
    return { text: status.text, color: status.color }
  }

  if (k === 'services') {
    const status = getServiceStatus(data)
    return { text: status.text, color: status.color }
  }

  if (k === 'jobs') {
    const status = getJobStatus(data)
    return { text: status.text, color: status.color }
  }

  if (k === 'cronjobs') {
    const status = getCronJobStatus(data)
    return { text: status.text, color: status.color }
  }

  if (k === 'hpas' || k === 'horizontalpodautoscalers') {
    const status = getHPAStatus(data)
    return { text: status.text, color: status.color }
  }

  // Generic status extraction for CRDs and unknown kinds
  const status = data.status
  if (status) {
    // Check for phase (common pattern)
    if (status.phase) {
      const phase = String(status.phase)
      const healthyPhases = ['Running', 'Active', 'Succeeded', 'Ready', 'Healthy', 'Available', 'Bound']
      const warningPhases = ['Pending', 'Progressing', 'Unknown', 'Terminating']
      const isHealthy = healthyPhases.includes(phase)
      const isWarning = warningPhases.includes(phase)
      return {
        text: phase,
        color: isHealthy ? 'bg-green-500/20 text-green-400' :
               isWarning ? 'bg-yellow-500/20 text-yellow-400' :
               'bg-red-500/20 text-red-400'
      }
    }

    // Check for conditions
    if (status.conditions && Array.isArray(status.conditions)) {
      const readyCondition = status.conditions.find((c: any) =>
        c.type === 'Ready' || c.type === 'Available' || c.type === 'Progressing'
      )
      if (readyCondition) {
        const isReady = readyCondition.status === 'True'
        return {
          text: isReady ? 'Ready' : 'Not Ready',
          color: isReady ? 'bg-green-500/20 text-green-400' : 'bg-yellow-500/20 text-yellow-400'
        }
      }
    }
  }

  return null
}
