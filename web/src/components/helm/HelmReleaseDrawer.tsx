import { useState, useCallback, useEffect, useRef } from 'react'
import { flushSync } from 'react-dom'
import { TRANSITION_DRAWER } from '../../utils/animation'
import { useRefreshAnimation } from '../../hooks/useRefreshAnimation'
import { X, Copy, Check, RefreshCw, Package, Code, History, FileText, Settings, Link2, Anchor, GitFork, BookOpen, ArrowUpCircle, Trash2 } from 'lucide-react'
import { clsx } from 'clsx'
import { useHelmRelease, useHelmManifest, useHelmValues, useHelmManifestDiff, useHelmUpgradeInfo, useHelmUninstall, upgradeWithProgress, rollbackWithProgress } from '../../api/client'
import { useQueryClient } from '@tanstack/react-query'
import { ConfirmDialog } from '../ui/ConfirmDialog'
import { Markdown } from '../ui/Markdown'
import type { SelectedHelmRelease, HelmHook, ChartDependency } from '../../types'
import type { NavigateToResource } from '../../utils/navigation'
import { formatDate } from './helm-utils'
import { getHelmStatusColor } from '../../utils/badge-colors'
import { useCanHelmWrite } from '../../contexts/CapabilitiesContext'
import { RevisionHistory } from './RevisionHistory'
import { ManifestViewer } from './ManifestViewer'
import { ValuesViewer } from './ValuesViewer'
import { OwnedResources } from './OwnedResources'
import { ManifestDiffViewer } from './ManifestDiffViewer'

interface HelmReleaseDrawerProps {
  release: SelectedHelmRelease
  onClose: () => void
  onNavigateToResource?: NavigateToResource
  /** Controls slide-in/out animation (driven by useAnimatedUnmount) */
  isOpen?: boolean
}

type TabId = 'overview' | 'history' | 'manifest' | 'values' | 'resources' | 'hooks' | 'diff'

const MIN_WIDTH = 500
const MAX_WIDTH_PERCENT = 0.8
const DEFAULT_WIDTH = 1000

export function HelmReleaseDrawer({ release, onClose, onNavigateToResource, isOpen = true }: HelmReleaseDrawerProps) {
  const [activeTab, setActiveTab] = useState<TabId>('overview')
  const [copied, setCopied] = useState<string | null>(null)
  const [drawerWidth, setDrawerWidth] = useState(DEFAULT_WIDTH)
  const [isResizing, setIsResizing] = useState(false)
  const [selectedRevision, setSelectedRevision] = useState<number | undefined>(undefined)
  const [showAllValues, setShowAllValues] = useState(false)
  const [diffRevisions, setDiffRevisions] = useState<{ rev1: number; rev2: number } | null>(null)
  const [rollbackRevision, setRollbackRevision] = useState<number | null>(null)
  const [showUninstallConfirm, setShowUninstallConfirm] = useState(false)
  const [showUpgradeConfirm, setShowUpgradeConfirm] = useState(false)
  const resizeStartX = useRef(0)
  const resizeStartWidth = useRef(DEFAULT_WIDTH)
  const canHelmWrite = useCanHelmWrite()

  const { data: releaseDetail, isLoading, refetch: refetchRelease } = useHelmRelease(
    release.namespace,
    release.name
  )
  const [refetch, isRefreshAnimating] = useRefreshAnimation(refetchRelease)

  // Fetch manifest for selected revision (or latest)
  const { data: manifest, isLoading: manifestLoading } = useHelmManifest(
    release.namespace,
    release.name,
    selectedRevision
  )

  // Fetch values
  const { data: values, isLoading: valuesLoading } = useHelmValues(
    release.namespace,
    release.name,
    showAllValues
  )

  // Fetch diff if comparing revisions
  const { data: diffData, isLoading: diffLoading } = useHelmManifestDiff(
    release.namespace,
    release.name,
    diffRevisions?.rev1 || 0,
    diffRevisions?.rev2 || 0
  )

  // Lazy check for upgrade availability
  const { data: upgradeInfo, isLoading: upgradeLoading } = useHelmUpgradeInfo(
    release.namespace,
    release.name
  )

  // Mutations for actions
  const uninstallMutation = useHelmUninstall()
  const queryClient = useQueryClient()
  const [upgradeProgress, setUpgradeProgress] = useState<{ phase: string; message: string }[]>([])
  const [isUpgrading, setIsUpgrading] = useState(false)
  const [rollbackProgress, setRollbackProgress] = useState<{ phase: string; message: string }[]>([])
  const [isRollingBack, setIsRollingBack] = useState(false)

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

  const switchTab = useCallback((tab: TabId) => {
    const update = () => flushSync(() => setActiveTab(tab))
    if (document.startViewTransition) {
      document.startViewTransition(update)
    } else {
      setActiveTab(tab)
    }
  }, [])

  const handleCompareRevisions = (rev1: number, rev2: number) => {
    setDiffRevisions({ rev1, rev2 })
    switchTab('diff')
  }

  const handleViewRevision = (revision: number) => {
    setSelectedRevision(revision)
    switchTab('manifest')
  }

  const handleRollbackRequest = (revision: number) => {
    setRollbackRevision(revision)
  }

  const handleRollbackConfirm = async () => {
    if (rollbackRevision === null) return
    setIsRollingBack(true)
    setRollbackProgress([])

    try {
      await rollbackWithProgress(
        release.namespace,
        release.name,
        rollbackRevision,
        (event) => {
          if (event.type === 'progress' && event.message) {
            setRollbackProgress(prev => [...prev, {
              phase: event.phase || 'progress',
              message: event.message || '',
            }])
          }
        }
      )

      setRollbackProgress(prev => [...prev, {
        phase: 'complete',
        message: `Successfully rolled back to revision ${rollbackRevision}`,
      }])

      queryClient.invalidateQueries({ queryKey: ['helm-releases'] })
      queryClient.invalidateQueries({ queryKey: ['helm-release', release.namespace, release.name] })

      setTimeout(() => {
        setRollbackRevision(null)
        setRollbackProgress([])
        refetch()
        switchTab('resources')
      }, 1500)
    } catch (err) {
      setRollbackProgress(prev => [...prev, {
        phase: 'error',
        message: err instanceof Error ? err.message : 'Rollback failed',
      }])
    } finally {
      setIsRollingBack(false)
    }
  }

  const handleUninstallConfirm = () => {
    uninstallMutation.mutate(
      { namespace: release.namespace, name: release.name },
      {
        onSuccess: () => {
          setShowUninstallConfirm(false)
          onClose()
        },
        onError: () => {
          // Keep dialog open on error so user can see the error state
        },
      }
    )
  }

  const handleUpgradeConfirm = async () => {
    if (!upgradeInfo?.latestVersion) return
    setIsUpgrading(true)
    setUpgradeProgress([])

    try {
      await upgradeWithProgress(
        release.namespace,
        release.name,
        upgradeInfo.latestVersion,
        (event) => {
          if (event.type === 'progress' && event.message) {
            setUpgradeProgress(prev => [...prev, {
              phase: event.phase || 'progress',
              message: event.message || '',
            }])
          }
        }
      )

      setUpgradeProgress(prev => [...prev, {
        phase: 'complete',
        message: `Successfully upgraded to ${upgradeInfo.latestVersion}`,
      }])

      // Invalidate queries
      queryClient.invalidateQueries({ queryKey: ['helm-releases'] })
      queryClient.invalidateQueries({ queryKey: ['helm-release', release.namespace, release.name] })
      queryClient.invalidateQueries({ queryKey: ['helm-upgrade-info', release.namespace, release.name] })
      queryClient.invalidateQueries({ queryKey: ['helm-batch-upgrade-info'] })

      setTimeout(() => {
        setShowUpgradeConfirm(false)
        setUpgradeProgress([])
        refetch()
        switchTab('resources')
      }, 1500)
    } catch (err) {
      setUpgradeProgress(prev => [...prev, {
        phase: 'error',
        message: err instanceof Error ? err.message : 'Upgrade failed',
      }])
    } finally {
      setIsUpgrading(false)
    }
  }

  const headerHeight = 49

  const tabs: { id: TabId; label: string; icon: typeof Package }[] = [
    { id: 'overview', label: 'Overview', icon: Package },
    { id: 'history', label: 'History', icon: History },
    { id: 'manifest', label: 'Manifest', icon: Code },
    { id: 'values', label: 'Values', icon: Settings },
    { id: 'resources', label: 'Resources', icon: Link2 },
    { id: 'hooks', label: 'Hooks', icon: Anchor },
  ]

  // Add diff tab only when comparing
  if (diffRevisions) {
    tabs.push({ id: 'diff', label: 'Diff', icon: FileText })
  }

  return (
    <div
      className={clsx(
        'fixed right-0 bg-theme-surface border-l border-theme-border flex flex-col shadow-2xl z-40',
        TRANSITION_DRAWER,
        isOpen ? 'translate-x-0 opacity-100' : 'translate-x-full opacity-0'
      )}
      style={{ width: drawerWidth, top: headerHeight, height: `calc(100vh - ${headerHeight}px)` }}
    >
      {/* Resize handle */}
      <div
        onMouseDown={handleResizeStart}
        className={clsx(
          'absolute left-0 top-0 bottom-0 w-2 cursor-ew-resize z-10 hover:bg-blue-500/50 transition-colors',
          'hidden sm:block',
          isResizing && 'bg-blue-500/50'
        )}
      />

      {/* Header */}
      <div className="border-b border-theme-border shrink-0">
        <div className="flex items-center justify-between px-4 pt-3 pb-2">
          <div className="flex items-center gap-2 flex-wrap">
            <span className="px-2 py-0.5 text-xs font-medium rounded bg-purple-500/20 text-purple-700 dark:text-purple-300">
              Helm Release
            </span>
            {releaseDetail && (
              <span className={clsx('px-2 py-0.5 text-xs font-medium rounded', getHelmStatusColor(releaseDetail.status))}>
                {releaseDetail.status}
              </span>
            )}
            {/* Upgrade indicator */}
            {upgradeLoading ? (
              <span className="px-2 py-0.5 text-xs font-medium rounded bg-theme-hover/50 text-theme-text-secondary animate-pulse">
                checking...
              </span>
            ) : upgradeInfo?.updateAvailable ? (
              <button
                onClick={() => setShowUpgradeConfirm(true)}
                disabled={!canHelmWrite}
                className={clsx(
                  'flex items-center gap-1 px-2 py-0.5 text-xs font-medium rounded bg-amber-500/20 text-amber-700 dark:text-amber-300 transition-colors',
                  canHelmWrite ? 'hover:bg-amber-500/30 cursor-pointer' : 'opacity-50 cursor-not-allowed'
                )}
                title={canHelmWrite ? `Click to upgrade: ${upgradeInfo.currentVersion} → ${upgradeInfo.latestVersion}${upgradeInfo.repositoryName ? ` (${upgradeInfo.repositoryName})` : ''}` : 'Helm write permissions required (rbac.helm=true)'}
              >
                <ArrowUpCircle className="w-3 h-3" />
                {upgradeInfo.latestVersion}
              </button>
            ) : upgradeInfo && !upgradeInfo.error ? (
              <span className="px-2 py-0.5 text-xs font-medium rounded bg-green-500/20 text-green-700 dark:text-green-300" title="Chart is up to date">
                latest
              </span>
            ) : null}
          </div>
          <div className="flex items-center gap-1">
            <button
              onClick={refetch}
              disabled={isRefreshAnimating}
              className="p-1.5 text-theme-text-secondary hover:text-theme-text-primary hover:bg-theme-elevated rounded disabled:opacity-50"
              title="Refresh"
            >
              <RefreshCw className={clsx('w-4 h-4', isRefreshAnimating && 'animate-spin')} />
            </button>
            <button
              onClick={() => setShowUninstallConfirm(true)}
              disabled={!canHelmWrite}
              className={clsx(
                'p-1.5 rounded',
                canHelmWrite
                  ? 'text-theme-text-secondary hover:text-red-400 hover:bg-red-500/10'
                  : 'text-theme-text-disabled cursor-not-allowed'
              )}
              title={canHelmWrite ? 'Uninstall release' : 'Helm write permissions required (rbac.helm=true)'}
            >
              <Trash2 className="w-4 h-4" />
            </button>
            <button onClick={onClose} className="p-1.5 text-theme-text-secondary hover:text-theme-text-primary hover:bg-theme-elevated rounded" title="Close (Esc)">
              <X className="w-4 h-4" />
            </button>
          </div>
        </div>

        {/* Name and namespace */}
        <div className="px-4 pb-3">
          <div className="flex items-center gap-2">
            <Package className="w-5 h-5 text-purple-400" />
            <h2 className="text-lg font-semibold text-theme-text-primary truncate">{release.name}</h2>
            <button
              onClick={() => copyToClipboard(release.name, 'name')}
              className="p-1 text-theme-text-secondary hover:text-theme-text-primary hover:bg-theme-elevated rounded shrink-0"
              title="Copy name"
            >
              {copied === 'name' ? <Check className="w-3.5 h-3.5 text-green-400" /> : <Copy className="w-3.5 h-3.5" />}
            </button>
          </div>
          <p className="text-sm text-theme-text-tertiary">{release.namespace}</p>
        </div>

        {/* Tabs */}
        <div className="flex items-center gap-1 px-4 pb-2 overflow-x-auto">
          {tabs.map((tab) => (
            <button
              key={tab.id}
              onClick={() => switchTab(tab.id)}
              className={clsx(
                'flex items-center gap-1.5 px-3 py-1.5 text-sm rounded-md transition-colors whitespace-nowrap',
                activeTab === tab.id
                  ? 'bg-theme-elevated text-theme-text-primary'
                  : 'text-theme-text-secondary hover:text-theme-text-primary hover:bg-theme-elevated/50'
              )}
            >
              <tab.icon className="w-3.5 h-3.5" />
              {tab.label}
            </button>
          ))}
        </div>
      </div>

      {/* Content */}
      <div className="flex-1 overflow-y-auto" style={{ viewTransitionName: 'helm-drawer-content' }}>
        {isLoading ? (
          <div className="flex items-center justify-center h-32 text-theme-text-tertiary">Loading...</div>
        ) : !releaseDetail ? (
          <div className="flex items-center justify-center h-32 text-theme-text-tertiary">Release not found</div>
        ) : (
          <>
            {activeTab === 'overview' && (
              <OverviewTab release={releaseDetail} onCopy={copyToClipboard} copied={copied} />
            )}
            {activeTab === 'history' && (
              <RevisionHistory
                history={releaseDetail.history}
                currentRevision={releaseDetail.revision}
                onViewRevision={handleViewRevision}
                onCompare={handleCompareRevisions}
                onRollback={canHelmWrite ? handleRollbackRequest : undefined}
              />
            )}
            {activeTab === 'manifest' && (
              <ManifestViewer
                manifest={manifest || ''}
                isLoading={manifestLoading}
                revision={selectedRevision}
                onCopy={(text) => copyToClipboard(text, 'manifest')}
                copied={copied === 'manifest'}
              />
            )}
            {activeTab === 'values' && (
              <ValuesViewer
                values={values}
                isLoading={valuesLoading}
                showAllValues={showAllValues}
                onToggleAllValues={setShowAllValues}
                onCopy={(text) => copyToClipboard(text, 'values')}
                copied={copied === 'values'}
                namespace={release.namespace}
                name={release.name}
                onApplySuccess={() => refetch()}
              />
            )}
            {activeTab === 'resources' && (
              <OwnedResources
                resources={releaseDetail.resources}
                onNavigate={onNavigateToResource}
              />
            )}
            {activeTab === 'hooks' && (
              <HooksTab hooks={releaseDetail.hooks || []} />
            )}
            {activeTab === 'diff' && diffRevisions && (
              <ManifestDiffViewer
                diff={diffData?.diff || ''}
                isLoading={diffLoading}
                revision1={diffRevisions.rev1}
                revision2={diffRevisions.rev2}
                onClose={() => {
                  setDiffRevisions(null)
                  setActiveTab('history')
                }}
              />
            )}
          </>
        )}
      </div>

      {/* Rollback confirmation dialog */}
      <ConfirmDialog
        open={rollbackRevision !== null}
        onClose={() => {
          setRollbackRevision(null)
          setRollbackProgress([])
          if (isRollingBack) {
            setIsRollingBack(false)
            switchTab('resources')
          }
        }}
        onConfirm={handleRollbackConfirm}
        title="Rollback Release"
        message={`Rollback "${release.name}" to revision ${rollbackRevision}?`}
        details={rollbackProgress.length === 0
          ? `This will create a new revision that reverts the release to the state it was in at revision ${rollbackRevision}. The rollback will be applied to your cluster immediately.`
          : undefined
        }
        confirmLabel="Rollback"
        variant="warning"
        isLoading={isRollingBack}
        isClosable
      >
        {rollbackProgress.length > 0 && <ProgressLog entries={rollbackProgress} />}
      </ConfirmDialog>

      {/* Uninstall confirmation dialog */}
      <ConfirmDialog
        open={showUninstallConfirm}
        onClose={() => setShowUninstallConfirm(false)}
        onConfirm={handleUninstallConfirm}
        title="Uninstall Release"
        message={`Are you sure you want to uninstall "${release.name}"?`}
        details={`This will remove the Helm release and all associated Kubernetes resources from the "${release.namespace}" namespace. This action cannot be undone.`}
        confirmLabel="Uninstall"
        variant="danger"
        isLoading={uninstallMutation.isPending}
      />

      {/* Upgrade confirmation dialog */}
      <ConfirmDialog
        open={showUpgradeConfirm}
        onClose={() => {
          setShowUpgradeConfirm(false)
          setUpgradeProgress([])
          if (isUpgrading) {
            // Upgrade continues server-side — switch to resources tab to monitor
            setIsUpgrading(false)
            switchTab('resources')
          }
        }}
        onConfirm={handleUpgradeConfirm}
        title="Upgrade Release"
        message={`Upgrade "${release.name}" to version ${upgradeInfo?.latestVersion}?`}
        details={upgradeProgress.length === 0
          ? `This will upgrade the chart from version ${upgradeInfo?.currentVersion} to ${upgradeInfo?.latestVersion}. Your existing values will be preserved. The upgrade will be applied to your cluster immediately.`
          : undefined
        }
        confirmLabel="Upgrade"
        variant="warning"
        isLoading={isUpgrading}
        isClosable
      >
        {upgradeProgress.length > 0 && <ProgressLog entries={upgradeProgress} />}
      </ConfirmDialog>
    </div>
  )
}

// Shared progress log for streaming Helm operations
function ProgressLog({ entries }: { entries: { phase: string; message: string }[] }) {
  return (
    <div className="space-y-1.5 max-h-48 overflow-auto">
      {entries.map((log, i) => (
        <div key={i} className="flex items-start gap-2 text-xs">
          <span className={clsx(
            'px-1.5 py-0.5 rounded font-medium shrink-0',
            log.phase === 'error' ? 'bg-red-500/20 text-red-400' :
            log.phase === 'complete' ? 'bg-green-500/20 text-green-400' :
            'bg-blue-500/20 text-blue-400'
          )}>
            {log.phase}
          </span>
          <span className={clsx(
            log.phase === 'error' ? 'text-red-400' :
            log.phase === 'complete' ? 'text-green-400' :
            'text-theme-text-secondary'
          )}>
            {log.message}
          </span>
        </div>
      ))}
    </div>
  )
}

// Overview tab content
interface OverviewTabProps {
  release: {
    chart: string
    chartVersion: string
    appVersion: string
    revision: number
    updated: string
    description: string
    notes: string
    readme?: string
    dependencies?: ChartDependency[]
  }
  onCopy: (text: string, key: string) => void
  copied: string | null
}

function OverviewTab({ release, onCopy, copied }: OverviewTabProps) {
  return (
    <div className="p-4 space-y-4">
      {/* Chart info */}
      <div className="bg-theme-elevated/30 rounded-lg p-4">
        <h3 className="text-sm font-medium text-theme-text-secondary mb-3">Chart Information</h3>
        <dl className="grid grid-cols-2 gap-3 text-sm">
          <div>
            <dt className="text-theme-text-tertiary">Chart</dt>
            <dd className="text-theme-text-primary font-medium">{release.chart}</dd>
          </div>
          <div>
            <dt className="text-theme-text-tertiary">Chart Version</dt>
            <dd className="text-theme-text-primary">{release.chartVersion}</dd>
          </div>
          <div>
            <dt className="text-theme-text-tertiary">App Version</dt>
            <dd className="text-theme-text-primary">{release.appVersion || '-'}</dd>
          </div>
          <div>
            <dt className="text-theme-text-tertiary">Revision</dt>
            <dd className="text-theme-text-primary">{release.revision}</dd>
          </div>
          <div className="col-span-2">
            <dt className="text-theme-text-tertiary">Updated</dt>
            <dd className="text-theme-text-primary">{formatDate(release.updated)}</dd>
          </div>
        </dl>
      </div>

      {/* Description */}
      {release.description && (
        <div className="bg-theme-elevated/30 rounded-lg p-4">
          <h3 className="text-sm font-medium text-theme-text-secondary mb-2">Description</h3>
          <p className="text-sm text-theme-text-secondary">{release.description}</p>
        </div>
      )}

      {/* Notes */}
      {release.notes && (
        <div className="bg-theme-elevated/30 rounded-lg p-4">
          <div className="flex items-center justify-between mb-2">
            <h3 className="text-sm font-medium text-theme-text-secondary">Release Notes</h3>
            <button
              onClick={() => onCopy(release.notes, 'notes')}
              className="flex items-center gap-1 px-2 py-1 text-xs text-theme-text-secondary hover:text-theme-text-primary hover:bg-theme-elevated rounded"
            >
              {copied === 'notes' ? <Check className="w-3.5 h-3.5 text-green-400" /> : <Copy className="w-3.5 h-3.5" />}
              Copy
            </button>
          </div>
          <div className="text-xs bg-theme-base/50 rounded p-3 max-h-64 overflow-auto">
            <Markdown>{release.notes}</Markdown>
          </div>
        </div>
      )}

      {/* Dependencies */}
      {release.dependencies && release.dependencies.length > 0 && (
        <div className="bg-theme-elevated/30 rounded-lg p-4">
          <div className="flex items-center gap-2 mb-3">
            <GitFork className="w-4 h-4 text-theme-text-secondary" />
            <h3 className="text-sm font-medium text-theme-text-secondary">Chart Dependencies</h3>
          </div>
          <div className="space-y-2">
            {release.dependencies.map((dep) => (
              <div key={dep.name} className="flex items-center justify-between bg-theme-base/50 rounded p-2 text-sm">
                <div className="flex items-center gap-2">
                  <span className="text-theme-text-primary font-medium">{dep.name}</span>
                  <span className="text-theme-text-tertiary">{dep.version}</span>
                </div>
                <div className="flex items-center gap-2">
                  {dep.condition && (
                    <span className="text-xs text-theme-text-tertiary">{dep.condition}</span>
                  )}
                  <span className={clsx(
                    'px-1.5 py-0.5 text-xs rounded',
                    dep.enabled
                      ? 'bg-green-500/20 text-green-400'
                      : 'bg-theme-hover/50 text-theme-text-secondary'
                  )}>
                    {dep.enabled ? 'enabled' : 'disabled'}
                  </span>
                </div>
              </div>
            ))}
          </div>
        </div>
      )}

      {/* README */}
      {release.readme && (
        <div className="bg-theme-elevated/30 rounded-lg p-4">
          <div className="flex items-center justify-between mb-2">
            <div className="flex items-center gap-2">
              <BookOpen className="w-4 h-4 text-theme-text-secondary" />
              <h3 className="text-sm font-medium text-theme-text-secondary">Chart README</h3>
            </div>
            <button
              onClick={() => onCopy(release.readme!, 'readme')}
              className="flex items-center gap-1 px-2 py-1 text-xs text-theme-text-secondary hover:text-theme-text-primary hover:bg-theme-elevated rounded"
            >
              {copied === 'readme' ? <Check className="w-3.5 h-3.5 text-green-400" /> : <Copy className="w-3.5 h-3.5" />}
              Copy
            </button>
          </div>
          <div className="text-xs bg-theme-base/50 rounded p-3 max-h-96 overflow-auto">
            <Markdown>{release.readme}</Markdown>
          </div>
        </div>
      )}
    </div>
  )
}

// Hooks tab content
interface HooksTabProps {
  hooks: HelmHook[]
}

function HooksTab({ hooks }: HooksTabProps) {
  if (hooks.length === 0) {
    return (
      <div className="flex flex-col items-center justify-center h-48 text-theme-text-tertiary">
        <Anchor className="w-8 h-8 mb-2 opacity-50" />
        <p>No hooks defined for this release</p>
      </div>
    )
  }

  const getHookStatusColor = (status?: string) => {
    if (!status) return 'bg-theme-hover/50 text-theme-text-secondary'
    switch (status.toLowerCase()) {
      case 'succeeded':
        return 'bg-green-500/20 text-green-400'
      case 'failed':
        return 'bg-red-500/20 text-red-400'
      case 'running':
        return 'bg-blue-500/20 text-blue-400'
      default:
        return 'bg-theme-hover/50 text-theme-text-secondary'
    }
  }

  const getEventColor = (event: string) => {
    if (event.includes('delete')) return 'bg-red-500/20 text-red-400 border-red-500/30'
    if (event.includes('install')) return 'bg-green-500/20 text-green-400 border-green-500/30'
    if (event.includes('upgrade')) return 'bg-blue-500/20 text-blue-400 border-blue-500/30'
    if (event.includes('rollback')) return 'bg-yellow-500/20 text-yellow-400 border-yellow-500/30'
    return 'bg-theme-hover/50 text-theme-text-secondary border-theme-border'
  }

  return (
    <div className="p-4 space-y-3">
      <p className="text-sm text-theme-text-secondary mb-4">
        Helm hooks are executed at specific points during the release lifecycle.
      </p>
      {hooks.map((hook) => (
        <div key={hook.name} className="bg-theme-elevated/30 rounded-lg p-4">
          <div className="flex items-start justify-between mb-2">
            <div>
              <div className="flex items-center gap-2">
                <span className="text-theme-text-primary font-medium">{hook.name}</span>
                <span className="px-1.5 py-0.5 text-xs rounded bg-theme-hover/50 text-theme-text-secondary">
                  {hook.kind}
                </span>
              </div>
              <div className="flex items-center gap-2 mt-1 text-xs text-theme-text-tertiary">
                <span>Weight: {hook.weight}</span>
              </div>
            </div>
            {hook.status && (
              <span className={clsx('px-2 py-0.5 text-xs rounded', getHookStatusColor(hook.status))}>
                {hook.status}
              </span>
            )}
          </div>
          <div className="flex flex-wrap gap-1.5 mt-2">
            {hook.events.map((event) => (
              <span
                key={event}
                className={clsx('px-2 py-0.5 text-xs rounded border', getEventColor(event))}
              >
                {event}
              </span>
            ))}
          </div>
        </div>
      ))}
    </div>
  )
}
