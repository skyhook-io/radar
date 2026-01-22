import { useState, useCallback, useEffect, useRef } from 'react'
import {
  X,
  Copy,
  Check,
  RefreshCw,
  Terminal,
  FileText,
  Trash2,
  Play,
  Pause,
  Code,
  Pencil,
  Save,
  XCircle,
  AlertTriangle,
} from 'lucide-react'
import { clsx } from 'clsx'
import { stringify as yamlStringify } from 'yaml'
import { useResource, useResourceEvents, useUpdateResource } from '../../api/client'
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
import { useOpenTerminal, useOpenLogs } from '../dock'
import { PortForwardButton } from '../portforward/PortForwardButton'
import { useToast } from '../ui/Toast'
import { CodeViewer } from '../ui/CodeViewer'
import { YamlEditor } from '../ui/YamlEditor'

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
  const [isEditing, setIsEditing] = useState(false)
  const [editedYaml, setEditedYaml] = useState('')
  const [yamlErrors, setYamlErrors] = useState<string[]>([])
  const [saveSuccess, setSaveSuccess] = useState(false)
  const [drawerWidth, setDrawerWidth] = useState(DEFAULT_WIDTH)
  const [isResizing, setIsResizing] = useState(false)
  const resizeStartX = useRef(0)
  const resizeStartWidth = useRef(DEFAULT_WIDTH)

  const updateResource = useUpdateResource()

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

  // Convert resource to YAML for editing
  const convertToYaml = useCallback((data: any) => {
    if (!data) return ''
    // Clean up the object for editing - remove status and managed fields
    const cleaned = { ...data }
    delete cleaned.status
    if (cleaned.metadata) {
      delete cleaned.metadata.managedFields
      delete cleaned.metadata.resourceVersion
      delete cleaned.metadata.uid
      delete cleaned.metadata.creationTimestamp
      delete cleaned.metadata.generation
    }
    return yamlStringify(cleaned, { lineWidth: 0, indent: 2 })
  }, [])

  // Start editing
  const handleStartEdit = useCallback(() => {
    setEditedYaml(convertToYaml(resourceData))
    setYamlErrors([])
    setIsEditing(true)
  }, [resourceData, convertToYaml])

  // Cancel editing
  const handleCancelEdit = useCallback(() => {
    setIsEditing(false)
    setEditedYaml('')
    setYamlErrors([])
  }, [])

  // Save changes
  const handleSaveEdit = useCallback(async () => {
    if (yamlErrors.length > 0) return

    try {
      await updateResource.mutateAsync({
        kind: resource.kind,
        namespace: resource.namespace,
        name: resource.name,
        yaml: editedYaml,
      })
      setIsEditing(false)
      setEditedYaml('')
      setSaveSuccess(true)
      // Small delay to allow K8s to process the update before refreshing
      setTimeout(() => {
        refetch()
        // Clear success state after animation completes
        setTimeout(() => setSaveSuccess(false), 2000)
      }, 1000)
    } catch (error) {
      // Error is handled by the mutation
    }
  }, [updateResource, resource, editedYaml, yamlErrors, refetch])

  // Handle YAML validation
  const handleYamlValidate = useCallback((_isValid: boolean, errors: string[]) => {
    setYamlErrors(errors)
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

      {/* Success animation overlay */}
      {saveSuccess && <SaveSuccessAnimation />}

      {/* Content */}
      <div className="flex-1 overflow-y-auto">
        {isLoading ? (
          <div className="flex items-center justify-center h-32 text-theme-text-tertiary">Loading...</div>
        ) : !resourceData ? (
          <div className="flex items-center justify-center h-32 text-theme-text-tertiary">Resource not found</div>
        ) : showYaml ? (
          <YamlView
            data={resourceData}
            kind={resource.kind}
            onCopy={(text) => copyToClipboard(text, 'yaml')}
            copied={copied === 'yaml'}
            isEditing={isEditing}
            editedYaml={editedYaml}
            onEditedYamlChange={setEditedYaml}
            onValidate={handleYamlValidate}
            yamlErrors={yamlErrors}
            isSaving={updateResource.isPending}
            saveError={updateResource.error?.message}
            onStartEdit={handleStartEdit}
            onCancelEdit={handleCancelEdit}
            onSaveEdit={handleSaveEdit}
          />
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
// ACTIONS BAR - Interactive buttons that change based on resource kind
// ============================================================================

function ActionsBar({ resource, data }: { resource: SelectedResource; data: any }) {
  const { showCopied } = useToast()
  const openTerminal = useOpenTerminal()
  const openLogs = useOpenLogs()
  const kind = resource.kind.toLowerCase()

  const isRunning = kind === 'pods' ? data?.status?.phase === 'Running' : true
  const containers = data?.spec?.containers?.map((c: any) => c.name) || []

  const handleOpenTerminal = () => {
    if (resource.namespace && resource.name && containers.length > 0) {
      openTerminal({
        namespace: resource.namespace,
        podName: resource.name,
        containerName: containers[0],
        containers,
      })
    }
  }

  const handleOpenLogs = () => {
    if (resource.namespace && resource.name && containers.length > 0) {
      openLogs({
        namespace: resource.namespace,
        podName: resource.name,
        containers,
      })
    }
  }

  return (
    <div className="flex items-center gap-2 px-4 pb-3 flex-wrap">
      {/* Pod actions */}
      {kind === 'pods' && (
        <>
          {isRunning && (
            <button
              onClick={handleOpenTerminal}
              className="flex items-center gap-1.5 px-3 py-1.5 text-xs font-medium text-white bg-blue-600 hover:bg-blue-700 rounded-lg transition-colors"
            >
              <Terminal className="w-3.5 h-3.5" />
              Terminal
            </button>
          )}
          <button
            onClick={handleOpenLogs}
            className="flex items-center gap-1.5 px-3 py-1.5 text-xs font-medium text-white bg-slate-600 hover:bg-slate-500 rounded-lg transition-colors"
          >
            <FileText className="w-3.5 h-3.5" />
            Logs
          </button>
          {isRunning && resource.namespace && resource.name && (
            <PortForwardButton
              type="pod"
              namespace={resource.namespace}
              name={resource.name}
              className="!px-3 !py-1.5 !text-xs"
            />
          )}
        </>
      )}

      {/* Service actions */}
      {kind === 'services' && resource.namespace && resource.name && (
        <PortForwardButton
          type="service"
          namespace={resource.namespace}
          name={resource.name}
          className="!px-3 !py-1.5 !text-xs"
        />
      )}

      {/* Workload actions - restart command */}
      {['deployments', 'statefulsets', 'daemonsets'].includes(kind) && (
        <button
          onClick={(e) => showCopied(
            `kubectl rollout restart ${kind.slice(0, -1)} ${resource.name} -n ${resource.namespace}`,
            'Restart command copied',
            e
          )}
          className="flex items-center gap-1.5 px-3 py-1.5 text-xs font-medium text-white bg-slate-600 hover:bg-slate-500 rounded-lg transition-colors"
        >
          <RefreshCw className="w-3.5 h-3.5" />
          Restart
        </button>
      )}

      {/* CronJob actions */}
      {kind === 'cronjobs' && (
        <>
          <button
            onClick={(e) => showCopied(
              `kubectl create job --from=cronjob/${resource.name} ${resource.name}-manual-$(date +%s) -n ${resource.namespace}`,
              'Trigger command copied',
              e
            )}
            className="flex items-center gap-1.5 px-3 py-1.5 text-xs font-medium text-white bg-blue-600 hover:bg-blue-700 rounded-lg transition-colors"
          >
            <Play className="w-3.5 h-3.5" />
            Trigger
          </button>
          <button
            onClick={(e) => {
              const suspended = data?.spec?.suspend
              showCopied(
                `kubectl patch cronjob ${resource.name} -n ${resource.namespace} -p '{"spec":{"suspend":${!suspended}}}'`,
                `${suspended ? 'Resume' : 'Suspend'} command copied`,
                e
              )
            }}
            className="flex items-center gap-1.5 px-3 py-1.5 text-xs font-medium text-white bg-slate-600 hover:bg-slate-500 rounded-lg transition-colors"
          >
            {data?.spec?.suspend ? <Play className="w-3.5 h-3.5" /> : <Pause className="w-3.5 h-3.5" />}
            {data?.spec?.suspend ? 'Resume' : 'Suspend'}
          </button>
        </>
      )}

      {/* Job logs */}
      {kind === 'jobs' && (
        <button
          onClick={(e) => showCopied(
            `kubectl logs job/${resource.name} -n ${resource.namespace} -f`,
            'Logs command copied',
            e
          )}
          className="flex items-center gap-1.5 px-3 py-1.5 text-xs font-medium text-white bg-slate-600 hover:bg-slate-500 rounded-lg transition-colors"
        >
          <FileText className="w-3.5 h-3.5" />
          Logs
        </button>
      )}

      {/* Delete action for all - shown as secondary/danger style */}
      <button
        onClick={(e) => {
          const kindSingular = kind.endsWith('s') ? kind.slice(0, -1) : kind
          showCopied(
            `kubectl delete ${kindSingular} ${resource.name} -n ${resource.namespace}`,
            'Delete command copied',
            e
          )
        }}
        className="flex items-center gap-1.5 px-3 py-1.5 text-xs font-medium text-red-400 hover:text-white hover:bg-red-600 border border-red-400/50 hover:border-red-600 rounded-lg transition-colors"
      >
        <Trash2 className="w-3.5 h-3.5" />
        Delete
      </button>
    </div>
  )
}

// ============================================================================
// SUCCESS ANIMATION
// ============================================================================

function SaveSuccessAnimation() {
  return (
    <div className="absolute top-0 left-0 right-0 z-50 pointer-events-none">
      <div className="flex justify-center animate-fade-in-out">
        <div className="mt-2 px-4 py-2 bg-green-600/90 dark:bg-green-500/90 backdrop-blur-sm rounded-lg shadow-lg flex items-center gap-2">
          <Check className="w-4 h-4 text-white" />
          <span className="text-white text-sm font-medium">Saved</span>
        </div>
      </div>
    </div>
  )
}

// ============================================================================
// YAML VIEW
// ============================================================================

interface YamlViewProps {
  data: any
  kind: string
  onCopy: (text: string) => void
  copied: boolean
  isEditing: boolean
  editedYaml: string
  onEditedYamlChange: (yaml: string) => void
  onValidate: (isValid: boolean, errors: string[]) => void
  yamlErrors: string[]
  isSaving: boolean
  saveError?: string
  onStartEdit: () => void
  onCancelEdit: () => void
  onSaveEdit: () => void
}

// Get edit warning for resource types with limited editability
function getEditWarning(kind: string): { message: string; tip: string; learnMoreUrl?: string } | null {
  const k = kind.toLowerCase()
  if (k === 'pods' || k === 'pod') {
    return {
      message: 'Pods have limited editability.',
      tip: 'Green highlighted lines can be changed. Edit the parent Deployment instead for other fields.',
      learnMoreUrl: 'https://kubernetes.io/docs/concepts/workloads/pods/#pod-update-and-replacement'
    }
  }
  if (k === 'jobs' || k === 'job') {
    return {
      message: 'Jobs cannot be modified after creation.',
      tip: 'Delete and recreate the Job to make changes.',
      learnMoreUrl: 'https://kubernetes.io/docs/concepts/workloads/controllers/job/'
    }
  }
  return null
}

// Parse and simplify Kubernetes error messages
function formatSaveError(error: string): { summary: string; details?: string } {
  // Extract the main error message
  if (error.includes('is invalid:')) {
    const parts = error.split('is invalid:')
    const errorPart = parts[1]?.trim() || ''

    // Look for the field and reason
    if (errorPart.includes('Forbidden:')) {
      const forbiddenMatch = errorPart.match(/([^:]+):\s*Forbidden:\s*([^.{]+)/)
      if (forbiddenMatch) {
        return {
          summary: `Cannot update ${forbiddenMatch[1]}: ${forbiddenMatch[2].trim()}`,
          details: error.length > 200 ? error : undefined
        }
      }
    }

    // Generic invalid error
    const summaryMatch = errorPart.match(/^([^{]+)/)
    if (summaryMatch) {
      return {
        summary: summaryMatch[1].trim(),
        details: error.length > 200 ? error : undefined
      }
    }
  }

  // Truncate very long errors
  if (error.length > 150) {
    return {
      summary: error.substring(0, 150) + '...',
      details: error
    }
  }

  return { summary: error }
}

function YamlView({
  data,
  kind,
  onCopy,
  copied,
  isEditing,
  editedYaml,
  onEditedYamlChange,
  onValidate,
  yamlErrors,
  isSaving,
  saveError,
  onStartEdit,
  onCancelEdit,
  onSaveEdit,
}: YamlViewProps) {
  const [showErrorDetails, setShowErrorDetails] = useState(false)

  // Convert to YAML for display (read-only mode)
  const yamlContent = yamlStringify(data, { lineWidth: 0, indent: 2 })

  const editWarning = getEditWarning(kind)
  const formattedError = saveError ? formatSaveError(saveError) : null

  if (isEditing) {
    return (
      <div className="flex flex-col h-full">
        {/* Edit mode header */}
        <div className="flex items-center justify-between px-4 py-2 border-b border-theme-border bg-theme-elevated/50">
          <div className="flex items-center gap-2">
            <Pencil className="w-4 h-4 text-blue-400" />
            <span className="text-sm font-medium text-theme-text-primary">Editing Resource</span>
          </div>
          <div className="flex items-center gap-2">
            <button
              onClick={onCancelEdit}
              disabled={isSaving}
              className="flex items-center gap-1 px-3 py-1.5 text-xs text-theme-text-secondary hover:text-theme-text-primary hover:bg-theme-surface rounded border border-theme-border disabled:opacity-50"
            >
              <XCircle className="w-3.5 h-3.5" />
              Cancel
            </button>
            <button
              onClick={onSaveEdit}
              disabled={isSaving || yamlErrors.length > 0}
              className="flex items-center gap-1 px-3 py-1.5 text-xs text-white bg-blue-600 hover:bg-blue-700 rounded disabled:opacity-50 disabled:cursor-not-allowed"
            >
              {isSaving ? (
                <RefreshCw className="w-3.5 h-3.5 animate-spin" />
              ) : (
                <Save className="w-3.5 h-3.5" />
              )}
              {isSaving ? 'Saving...' : 'Save'}
            </button>
          </div>
        </div>

        {/* Resource-specific warning */}
        {editWarning && (
          <div className="px-4 py-2.5 bg-amber-500/10 dark:bg-yellow-500/10 border-b border-amber-300 dark:border-yellow-500/30">
            <div className="flex items-start gap-2">
              <AlertTriangle className="w-4 h-4 text-amber-600 dark:text-yellow-400 mt-0.5 shrink-0" />
              <div className="text-xs">
                <span className="font-medium text-amber-700 dark:text-yellow-300">{editWarning.message}</span>
                <span className="text-amber-600 dark:text-yellow-400/80 ml-1">{editWarning.tip}</span>
                {editWarning.learnMoreUrl && (
                  <a
                    href={editWarning.learnMoreUrl}
                    target="_blank"
                    rel="noopener noreferrer"
                    className="ml-1.5 text-blue-600 dark:text-blue-400 hover:underline"
                  >
                    Learn more →
                  </a>
                )}
              </div>
            </div>
          </div>
        )}

        {/* Validation errors */}
        {yamlErrors.length > 0 && (
          <div className="px-4 py-2 bg-red-500/10 border-b border-red-300 dark:border-red-500/30">
            <div className="flex items-start gap-2">
              <AlertTriangle className="w-4 h-4 text-red-600 dark:text-red-400 mt-0.5 shrink-0" />
              <div className="text-xs text-red-600 dark:text-red-400">
                {yamlErrors.map((err, i) => (
                  <div key={i}>{err}</div>
                ))}
              </div>
            </div>
          </div>
        )}

        {/* Save error */}
        {formattedError && (
          <div className="px-4 py-2 bg-red-500/10 border-b border-red-300 dark:border-red-500/30">
            <div className="flex items-start gap-2">
              <AlertTriangle className="w-4 h-4 text-red-600 dark:text-red-400 mt-0.5 shrink-0" />
              <div className="text-xs text-red-600 dark:text-red-400 flex-1">
                <div className="font-medium">Save failed</div>
                <div className="mt-1">{formattedError.summary}</div>
                {formattedError.details && (
                  <button
                    onClick={() => setShowErrorDetails(!showErrorDetails)}
                    className="mt-1 text-red-500 dark:text-red-300 hover:text-red-700 dark:hover:text-red-200 underline"
                  >
                    {showErrorDetails ? 'Hide details' : 'Show details'}
                  </button>
                )}
                {showErrorDetails && formattedError.details && (
                  <pre className="mt-2 p-2 bg-red-500/10 rounded text-[10px] whitespace-pre-wrap break-all max-h-40 overflow-auto">
                    {formattedError.details}
                  </pre>
                )}
              </div>
            </div>
          </div>
        )}

        {/* Editor */}
        <div className="flex-1 min-h-0">
          <YamlEditor
            value={editedYaml}
            onChange={onEditedYamlChange}
            onValidate={onValidate}
            height="100%"
            kind={kind}
          />
        </div>
      </div>
    )
  }

  // Read-only mode
  return (
    <div className="p-4">
      <div className="flex items-center justify-between mb-2">
        <span className="text-sm font-medium text-theme-text-secondary">YAML</span>
        <div className="flex items-center gap-2">
          <button
            onClick={onStartEdit}
            className="flex items-center gap-1 px-2 py-1 text-xs text-blue-400 hover:text-blue-300 hover:bg-theme-elevated rounded"
          >
            <Pencil className="w-3.5 h-3.5" />
            Edit
          </button>
          <button
            onClick={() => onCopy(yamlContent)}
            className="flex items-center gap-1 px-2 py-1 text-xs text-theme-text-secondary hover:text-theme-text-primary hover:bg-theme-elevated rounded"
          >
            {copied ? <Check className="w-3.5 h-3.5 text-green-400" /> : <Copy className="w-3.5 h-3.5" />}
            Copy
          </button>
        </div>
      </div>
      <CodeViewer
        code={yamlContent}
        language="yaml"
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
