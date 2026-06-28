import { useState, useCallback, useEffect, useRef } from 'react'
import { flushSync } from 'react-dom'
import { FetchResult, useDockReservedHeight, compareVersions } from '@skyhook-io/k8s-ui'
import { startViewTransitionSafe } from '@skyhook-io/k8s-ui/utils/view-transition'
import { TRANSITION_DRAWER } from '../../utils/animation'
import { useRefreshAnimation } from '../../hooks/useRefreshAnimation'
import { X, Copy, Check, RefreshCw, Package, Code, History, Settings, Link2, Anchor, GitFork, BookOpen, ArrowUpCircle, Trash2, GitBranch, AlertTriangle, RotateCcw, Clock, GitCompare, ExternalLink } from 'lucide-react'
import { useNavigate } from 'react-router-dom'
import { clsx } from 'clsx'
import { useHelmRelease, useHelmManifest, useHelmValues, useHelmUpgradeInfo, useHelmReleaseVersions, useHelmUninstall, upgradeWithProgress, rollbackWithProgress } from '../../api/client'
import { useQueryClient } from '@tanstack/react-query'
import { ConfirmDialog } from '../ui/ConfirmDialog'
import { Tooltip } from '../ui/Tooltip'
import { Markdown } from '../ui/Markdown'
import type { SelectedHelmRelease, HelmHook, ChartDependency, HelmOperation, HelmOperationInsight, HelmOwnedResource, HookDiagnostic, HookLogEvidence } from '../../types'
import { apiVersionToGroup, kindToPlural, type NavigateToResource } from '../../utils/navigation'
import { formatDate } from './helm-utils'
import { getHelmStatusColor, getKindBadgeColor, getResourceStatusColor, SEVERITY_BADGE, SEVERITY_TEXT } from '../../utils/badge-colors'
import { useCanHelmAct, useCloudRole } from '../../api/client'
import { RoleGatedPanel } from './RoleGatedPanel'
import { RevisionHistory } from './RevisionHistory'
import { ManifestViewer } from './ManifestViewer'
import { ValuesViewer } from './ValuesViewer'
import { OwnedResources } from './OwnedResources'
import { TrackChartSourceDialog } from './TrackChartSourceDialog'

interface HelmReleaseDrawerProps {
  release: SelectedHelmRelease
  onClose: () => void
  onNavigateToResource?: NavigateToResource
  /** Controls slide-in/out animation (driven by useAnimatedUnmount) */
  isOpen?: boolean
}

type TabId = 'overview' | 'history' | 'manifest' | 'values' | 'resources' | 'hooks'

const MIN_WIDTH = 500
const MAX_WIDTH_PERCENT = 0.8
const DEFAULT_WIDTH = 1000

export function HelmReleaseDrawer({ release, onClose, onNavigateToResource, isOpen = true }: HelmReleaseDrawerProps) {
  const navigate = useNavigate()
  const [activeTab, setActiveTab] = useState<TabId>('overview')
  const [copied, setCopied] = useState<string | null>(null)
  const [drawerWidth, setDrawerWidth] = useState(DEFAULT_WIDTH)
  const [viewportWidth, setViewportWidth] = useState(() => (typeof window === 'undefined' ? DEFAULT_WIDTH : window.innerWidth))
  const [isResizing, setIsResizing] = useState(false)
  const [selectedRevision, setSelectedRevision] = useState<number | undefined>(undefined)
  const [showAllValues, setShowAllValues] = useState(false)
  const [rollbackRevision, setRollbackRevision] = useState<number | null>(null)
  const [showUninstallConfirm, setShowUninstallConfirm] = useState(false)
  const [showUpgradeConfirm, setShowUpgradeConfirm] = useState(false)
  const [showTrackSource, setShowTrackSource] = useState(false)
  const [selectedVersion, setSelectedVersion] = useState<string | null>(null)
  const resizeStartX = useRef(0)
  const resizeStartWidth = useRef(DEFAULT_WIDTH)
  const { allowed: canHelmWrite, reason: helmActReason } = useCanHelmAct()
  // Cloud viewers can't view release manifests / values / diffs
  // (backend gate at requireCloudRole('member')). Skip the queries
  // when the role would 403 — saves a round-trip and avoids a
  // transient error state under the role-gated panel.
  const { canAtLeast } = useCloudRole()
  const canViewSensitive = canAtLeast('member')
  const helmNamespace = release.storageNamespace || release.namespace

  const { data: releaseDetail, isLoading, error: releaseError, refetch: refetchRelease } = useHelmRelease(
    helmNamespace,
    release.name
  )
  const [refetch, isRefreshAnimating] = useRefreshAnimation(refetchRelease)

  // Fetch manifest for selected revision (or latest)
  const { data: manifest, isLoading: manifestLoading } = useHelmManifest(
    helmNamespace,
    release.name,
    selectedRevision,
    canViewSensitive,
  )

  // Fetch values
  const { data: values, isLoading: valuesLoading } = useHelmValues(
    helmNamespace,
    release.name,
    showAllValues,
    canViewSensitive,
    selectedRevision,
  )

  // Lazy check for upgrade availability
  const { data: upgradeInfo, isLoading: upgradeLoading, error: upgradeError } = useHelmUpgradeInfo(
    helmNamespace,
    release.name
  )
  const upgradeErrorMessage = upgradeError instanceof Error ? upgradeError.message : 'Upgrade check failed'

  // Available versions for the upgrade dialog's picker — only fetched while the
  // confirm dialog is open. Default the selection to latest when it opens.
  const { data: availableVersions } = useHelmReleaseVersions(helmNamespace, release.name, showUpgradeConfirm)
  const targetVersion = selectedVersion ?? upgradeInfo?.latestVersion ?? ''
  // Semver compare, not list-position: the installed version may be older than
  // the newest-N versions the picker shows, so it isn't always in the list.
  const isDowngrade = Boolean(
    targetVersion && upgradeInfo?.currentVersion &&
    compareVersions(targetVersion, upgradeInfo.currentVersion) === -1
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

  useEffect(() => {
    const handleWindowResize = () => setViewportWidth(window.innerWidth)
    handleWindowResize()
    window.addEventListener('resize', handleWindowResize)
    return () => window.removeEventListener('resize', handleWindowResize)
  }, [])

  const minDrawerWidth = Math.min(MIN_WIDTH, viewportWidth)
  const maxDrawerWidth = viewportWidth < MIN_WIDTH ? viewportWidth : Math.max(MIN_WIDTH, viewportWidth * MAX_WIDTH_PERCENT)
  const renderedDrawerWidth = Math.max(minDrawerWidth, Math.min(drawerWidth, maxDrawerWidth))

  // Resize handlers
  const handleResizeStart = useCallback((e: React.MouseEvent) => {
    e.preventDefault()
    setIsResizing(true)
    resizeStartX.current = e.clientX
    resizeStartWidth.current = renderedDrawerWidth
  }, [renderedDrawerWidth])

  useEffect(() => {
    if (!isResizing) return

    document.body.style.cursor = 'ew-resize'
    document.body.style.userSelect = 'none'

    const handleMouseMove = (e: MouseEvent) => {
      const deltaX = resizeStartX.current - e.clientX
      const newWidth = resizeStartWidth.current + deltaX
      const viewport = window.innerWidth
      const minWidth = Math.min(MIN_WIDTH, viewport)
      const maxWidth = viewport < MIN_WIDTH ? viewport : Math.max(MIN_WIDTH, viewport * MAX_WIDTH_PERCENT)
      setDrawerWidth(Math.max(minWidth, Math.min(newWidth, maxWidth)))
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
    // Swallow the InvalidStateError the API rejects with on rapid
    // tab clicks (SKY-833 bug 49); fall back synchronously when the
    // API isn't available.
    startViewTransitionSafe(() => flushSync(() => setActiveTab(tab)))
  }, [])

  const handleCompareRevisions = useCallback((rev1: number, rev2: number) => {
    const params = new URLSearchParams()
    const globalNamespaces = new URLSearchParams(window.location.search).get('namespaces')
    if (globalNamespaces) params.set('namespaces', globalNamespaces)
    params.set('release', `${release.namespace}/${release.name}`)
    if (release.storageNamespace) params.set('releaseStorage', release.storageNamespace)
    params.set('revision1', String(rev1))
    params.set('revision2', String(rev2))
    navigate({ pathname: '/helm/compare', search: params.toString() })
  }, [navigate, release.name, release.namespace, release.storageNamespace])

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
        helmNamespace,
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
      queryClient.invalidateQueries({ queryKey: ['helm-release', helmNamespace, release.name] })

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
      { namespace: helmNamespace, name: release.name },
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
    if (!targetVersion) return
    setIsUpgrading(true)
    setUpgradeProgress([])

    try {
      await upgradeWithProgress(
        helmNamespace,
        release.name,
        targetVersion,
        upgradeInfo?.repositoryName,
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
        message: `Successfully upgraded to ${targetVersion}`,
      }])

      // Invalidate queries
      queryClient.invalidateQueries({ queryKey: ['helm-releases'] })
      queryClient.invalidateQueries({ queryKey: ['helm-release', helmNamespace, release.name] })
      queryClient.invalidateQueries({ queryKey: ['helm-upgrade-info', helmNamespace, release.name] })
      queryClient.invalidateQueries({ queryKey: ['helm-batch-upgrade-info'] })

      setTimeout(() => {
        setShowUpgradeConfirm(false)
        setUpgradeProgress([])
        setSelectedVersion(null)
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
  const dockInset = useDockReservedHeight()

  const tabs: { id: TabId; label: string; icon: typeof Package }[] = [
    { id: 'overview', label: 'Overview', icon: Package },
    { id: 'history', label: 'History', icon: History },
    { id: 'manifest', label: 'Manifest', icon: Code },
    { id: 'values', label: 'Values', icon: Settings },
    { id: 'resources', label: 'Resources', icon: Link2 },
    { id: 'hooks', label: 'Hooks', icon: Anchor },
  ]

  return (
    <div
      className={clsx(
        'fixed right-0 bg-theme-surface border-l border-theme-border flex flex-col shadow-drawer z-40',
        TRANSITION_DRAWER,
        isOpen ? 'translate-x-0 opacity-100' : 'translate-x-full opacity-0'
      )}
      style={{ width: renderedDrawerWidth, top: headerHeight, height: `calc(100vh - ${headerHeight}px - ${dockInset}px)` }}
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
            <span className={clsx('badge', SEVERITY_BADGE.info)}>
              Helm Release
            </span>
            {releaseDetail && (
              <span className={clsx('badge', getHelmStatusColor(releaseDetail.status))}>
                {releaseDetail.status}
              </span>
            )}
            {/* Upgrade indicator */}
            {upgradeLoading ? (
              <span className="badge bg-theme-hover/50 text-theme-text-secondary animate-pulse">
                checking...
              </span>
            ) : upgradeError ? (
              <Tooltip content={upgradeErrorMessage ?? ''}>
              <span
                className="badge bg-theme-hover/50 text-theme-text-secondary"
              >
                upgrade check failed
              </span>
              </Tooltip>
            ) : upgradeInfo?.updateAvailable && releaseDetail?.managedByFluxHelmRelease ? (
              // Route-only for GitOps-managed releases: a direct `helm upgrade`
              // would be reverted at the next reconcile, so surface the available
              // version as info and point at GitOps rather than offer the upgrade.
              <Tooltip content={`${upgradeInfo.latestVersion} available — managed by Flux, upgrade via the GitOps view (a direct upgrade would be reverted at the next reconcile).`}>
              <span className={clsx('badge', SEVERITY_BADGE.warning, 'opacity-90')}>
                <ArrowUpCircle className="w-3 h-3" />
                {upgradeInfo.latestVersion}
              </span>
              </Tooltip>
            ) : upgradeInfo?.updateAvailable ? (
              <Tooltip content={canHelmWrite ? `Click to upgrade: ${upgradeInfo.currentVersion} → ${upgradeInfo.latestVersion}${upgradeInfo.repositoryName ? ` (${upgradeInfo.repositoryName})` : upgradeInfo.sourceType === 'oci' ? ' (OCI)' : ''}` : helmActReason}>
              <button
                onClick={() => setShowUpgradeConfirm(true)}
                disabled={!canHelmWrite}
                className={clsx(
                  'badge transition-colors disabled:pointer-events-none', SEVERITY_BADGE.warning,
                  canHelmWrite ? 'hover:bg-amber-500/30 cursor-pointer' : 'opacity-50 cursor-not-allowed'
                )}
              >
                <ArrowUpCircle className="w-3 h-3" />
                {upgradeInfo.latestVersion}
              </button>
              </Tooltip>
            ) : upgradeInfo && !upgradeInfo.error ? (
              <Tooltip content="Chart is up to date">
              <span className={clsx('badge', SEVERITY_BADGE.success)}>
                latest
              </span>
              </Tooltip>
            ) : upgradeInfo?.error ? (
              releaseDetail?.managedByFluxHelmRelease ? (
                // Managed by Flux — the "Managed by Flux" badge routes to GitOps,
                // where the chart source lives. Don't push a Helm source here.
                <Tooltip content="Chart source is managed by Flux — track upgrades from the GitOps view.">
                <span className="badge bg-theme-hover/50 text-theme-text-secondary">
                  source via GitOps
                </span>
                </Tooltip>
              ) : upgradeInfo.untracked ? (
                // Helm doesn't record the install source. Offer to register one so
                // Radar can track upgrades for the user's own (e.g. OCI) charts.
                <Tooltip content={canHelmWrite ? "Radar can't tell where this chart was installed from. Register your chart source to track upgrades." : upgradeInfo.error}>
                <button
                  onClick={() => canHelmWrite && setShowTrackSource(true)}
                  disabled={!canHelmWrite}
                  className={clsx(
                    'badge transition-colors disabled:pointer-events-none bg-theme-hover/50 text-theme-text-secondary',
                    canHelmWrite ? 'hover:bg-theme-hover cursor-pointer' : 'opacity-50 cursor-not-allowed'
                  )}
                >
                  <Link2 className="w-3 h-3" />
                  upgrade source not tracked
                </button>
                </Tooltip>
              ) : (
                // Repo-side error (stale/broken index, classic ambiguity) — not an
                // OCI tracking issue, so surface it without steering to registration.
                <Tooltip content={upgradeInfo.error}>
                <span className="badge bg-theme-hover/50 text-theme-text-secondary">
                  upgrade source unresolved
                </span>
                </Tooltip>
              )
            ) : null}
          </div>
          <div className="flex items-center gap-1">
            <Tooltip content="Refresh">
            <button
              onClick={refetch}
              disabled={isRefreshAnimating}
              className="p-1.5 text-theme-text-secondary hover:text-theme-text-primary hover:bg-theme-elevated rounded disabled:opacity-50 disabled:pointer-events-none"
            >
              <RefreshCw className={clsx('w-4 h-4', isRefreshAnimating && 'animate-spin')} />
            </button>
            </Tooltip>
            <Tooltip content={canHelmWrite ? 'Uninstall release' : helmActReason}>
            <button
              onClick={() => setShowUninstallConfirm(true)}
              disabled={!canHelmWrite}
              className={clsx(
                'p-1.5 rounded disabled:pointer-events-none',
                canHelmWrite
                  ? 'text-theme-text-secondary hover:text-red-400 hover:bg-red-500/10'
                  : 'text-theme-text-disabled cursor-not-allowed'
              )}
            >
              <Trash2 className="w-4 h-4" />
            </button>
            </Tooltip>
            <Tooltip content="Close (Esc)">
            <button onClick={onClose} className="p-1.5 text-theme-text-secondary hover:text-theme-text-primary hover:bg-theme-elevated rounded">
              <X className="w-4 h-4" />
            </button>
            </Tooltip>
          </div>
        </div>

        {/* Name and namespace */}
        <div className="px-4 pb-3">
          <div className="flex items-center gap-2">
            <Package className="w-5 h-5 text-purple-400" />
            <h2 className="text-lg font-semibold text-theme-text-primary truncate">{release.name}</h2>
            <Tooltip content="Copy name" wrapperClassName="shrink-0">
            <button
              onClick={() => copyToClipboard(release.name, 'name')}
              className="p-1 text-theme-text-secondary hover:text-theme-text-primary hover:bg-theme-elevated rounded shrink-0"
            >
              {copied === 'name' ? <Check className="w-3.5 h-3.5 text-green-400" /> : <Copy className="w-3.5 h-3.5" />}
            </button>
            </Tooltip>
          </div>
          <p className="text-sm text-theme-text-tertiary">{release.namespace}</p>
          {releaseDetail?.managedByFluxHelmRelease && (
            <Tooltip content={`Installed by Flux helm-controller via HelmRelease ${releaseDetail.managedByFluxHelmRelease}. Changes here would be reverted at the next reconcile.`} wrapperClassName="mt-1">
            <button
              type="button"
              onClick={() => {
                const [ns, name] = releaseDetail.managedByFluxHelmRelease!.split('/')
                navigate(`/gitops/detail/helmreleases/${encodeURIComponent(ns || '_')}/${encodeURIComponent(name)}`)
              }}
              className="inline-flex items-center gap-1 rounded border border-amber-500/40 bg-amber-500/10 px-1.5 py-0.5 text-[11px] text-amber-700 dark:text-amber-400 hover:bg-amber-500/20 transition-colors"
            >
              <GitBranch className="w-3 h-3" />
              Managed by Flux · {releaseDetail.managedByFluxHelmRelease}
            </button>
            </Tooltip>
          )}
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
        {!releaseDetail ? (
          <FetchResult loading={isLoading} error={releaseError} notFoundMessage="Release not found" className="h-32" />
        ) : (
          <>
            <HelmOperationBanner
              operation={releaseDetail.lastOperation}
              operationInsight={releaseDetail.operationInsight}
              releaseNamespace={releaseDetail.namespace}
              managedByFluxHelmRelease={releaseDetail.managedByFluxHelmRelease}
              hookDiagnostics={releaseDetail.hookDiagnostics}
              canCompare={canViewSensitive}
              onCompare={handleCompareRevisions}
              onNavigateToResource={onNavigateToResource}
            />
            {activeTab === 'overview' && (
              <OverviewTab release={releaseDetail} onCopy={copyToClipboard} copied={copied} />
            )}
            {activeTab === 'history' && (
              <RevisionHistory
                history={releaseDetail.history}
                currentRevision={releaseDetail.revision}
                operations={mergeHelmOperations(releaseDetail.operations, releaseDetail.lastOperation)}
                onViewRevision={handleViewRevision}
                onCompare={handleCompareRevisions}
                onRollback={canHelmWrite ? handleRollbackRequest : undefined}
              />
            )}
            {activeTab === 'manifest' && (
              <RoleGatedPanel min="member" feature="release manifests">
                <ManifestViewer
                  manifest={manifest || ''}
                  isLoading={manifestLoading}
                  revision={selectedRevision}
                  onCopy={(text) => copyToClipboard(text, 'manifest')}
                  copied={copied === 'manifest'}
                />
              </RoleGatedPanel>
            )}
            {activeTab === 'values' && (
              <RoleGatedPanel min="member" feature="release values">
                <ValuesViewer
                  values={values}
                  isLoading={valuesLoading}
                  showAllValues={showAllValues}
                  onToggleAllValues={setShowAllValues}
                  onCopy={(text) => copyToClipboard(text, 'values')}
                  copied={copied === 'values'}
                  namespace={helmNamespace}
                  name={release.name}
                  revision={selectedRevision}
                  currentRevision={releaseDetail.revision}
                  onApplySuccess={() => refetch()}
                />
              </RoleGatedPanel>
            )}
            {activeTab === 'resources' && (
              <OwnedResources
                resources={releaseDetail.resources}
                onNavigate={onNavigateToResource}
              />
            )}
            {activeTab === 'hooks' && (
              <HooksTab hooks={releaseDetail.hooks || []} hookDiagnostics={releaseDetail.hookDiagnostics || []} />
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
          setSelectedVersion(null)
          if (isUpgrading) {
            // Upgrade continues server-side — switch to resources tab to monitor
            setIsUpgrading(false)
            switchTab('resources')
          }
        }}
        onConfirm={handleUpgradeConfirm}
        title="Upgrade Release"
        message={`Upgrade "${release.name}" to version ${targetVersion}?`}
        details={upgradeProgress.length === 0
          ? `The chart will move from version ${upgradeInfo?.currentVersion} to ${targetVersion}. Your existing values will be preserved. The change is applied to your cluster immediately.`
          : undefined
        }
        confirmLabel={isDowngrade ? 'Downgrade' : 'Upgrade'}
        variant="warning"
        isLoading={isUpgrading}
        isClosable
      >
        {upgradeProgress.length === 0 && availableVersions && availableVersions.length > 1 && (
          <div className="mb-1">
            <label htmlFor="upgrade-version" className="block text-sm font-medium text-theme-text-secondary mb-1.5">
              Target version
            </label>
            <select
              id="upgrade-version"
              value={targetVersion}
              onChange={(e) => setSelectedVersion(e.target.value)}
              disabled={isUpgrading}
              className="w-full px-3 py-2 bg-theme-elevated border border-theme-border-light rounded-lg text-sm text-theme-text-primary focus:outline-none focus:ring-2 focus:ring-accent disabled:opacity-50"
            >
              {availableVersions.map((v) => (
                <option key={v} value={v}>
                  {v}
                  {v === upgradeInfo?.latestVersion ? ' (latest)' : ''}
                  {v === upgradeInfo?.currentVersion ? ' (current)' : ''}
                </option>
              ))}
            </select>
            {isDowngrade && (
              <p className="mt-1 text-xs text-amber-600 dark:text-amber-400">
                This is a downgrade from {upgradeInfo?.currentVersion}.
              </p>
            )}
            {availableVersions.length >= 50 && (
              <p className="mt-1 text-xs text-theme-text-tertiary">
                Showing the 50 newest versions. Type to filter.
              </p>
            )}
          </div>
        )}
        {upgradeProgress.length > 0 && <ProgressLog entries={upgradeProgress} />}
      </ConfirmDialog>

      <TrackChartSourceDialog
        open={showTrackSource}
        onClose={() => setShowTrackSource(false)}
        chartName={releaseDetail?.chart}
      />
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
            log.phase === 'error' ? SEVERITY_BADGE.error :
            log.phase === 'complete' ? SEVERITY_BADGE.success :
            SEVERITY_BADGE.info
          )}>
            {log.phase}
          </span>
          <span className={clsx(
            log.phase === 'error' ? SEVERITY_TEXT.error :
            log.phase === 'complete' ? SEVERITY_TEXT.success :
            'text-theme-text-secondary'
          )}>
            {log.message}
          </span>
        </div>
      ))}
    </div>
  )
}

function mergeHelmOperations(operations: HelmOperation[] | undefined, lastOperation: HelmOperation | undefined): HelmOperation[] {
  const merged: HelmOperation[] = []
  const seen = new Set<string>()
  for (const op of [...(operations || []), ...(lastOperation ? [lastOperation] : [])]) {
    const key = helmOperationKey(op)
    if (seen.has(key)) continue
    seen.add(key)
    merged.push(op)
  }
  return merged
}

function helmOperationKey(operation: HelmOperation): string {
  return [
    operation.kind,
    operation.status,
    operation.revision || 0,
    operation.failedRevision || 0,
    operation.rollbackRevision || 0,
    operation.targetRevision || 0,
  ].join(':')
}

function HelmOperationBanner({
  operation,
  operationInsight,
  releaseNamespace,
  managedByFluxHelmRelease,
  hookDiagnostics,
  canCompare,
  onCompare,
  onNavigateToResource,
}: {
  operation?: HelmOperation
  operationInsight?: HelmOperationInsight
  releaseNamespace: string
  managedByFluxHelmRelease?: string
  hookDiagnostics?: HookDiagnostic[]
  canCompare?: boolean
  onCompare?: (rev1: number, rev2: number) => void
  onNavigateToResource?: NavigateToResource
}) {
  const primaryHookDiagnostic = hookDiagnostics?.[0]
  if ((!operation || !shouldShowOperationBanner(operation)) && !primaryHookDiagnostic) {
    return null
  }
  if (!operation || !shouldShowOperationBanner(operation)) {
    const tone = hookDiagnosticTone(primaryHookDiagnostic!)
    return (
      <div className="m-4 mb-0 card-inner-lg">
        <div className="flex items-start gap-3">
          <AlertTriangle className={clsx('mt-0.5 h-5 w-5 shrink-0', SEVERITY_TEXT[tone])} />
          <div className="min-w-0 flex-1">
            <div className="flex flex-wrap items-center gap-2">
              <span className="text-sm font-medium text-theme-text-primary">Helm hook needs attention</span>
              <span className={clsx('badge-sm', SEVERITY_BADGE[tone])}>{primaryHookDiagnostic!.phase}</span>
            </div>
            <HookSignal diagnostic={primaryHookDiagnostic!} />
          </div>
        </div>
      </div>
    )
  }

  const isFailure = operation.status === 'failed'
  const isPending = operation.status === 'stuck_pending'
  const tone: 'error' | 'warning' | 'info' = isFailure ? 'error' : operation.kind === 'rollback' ? 'info' : 'warning'
  const Icon = operation.kind === 'upgrade_rolled_back' || operation.kind === 'rollback' ? RotateCcw : isPending ? Clock : AlertTriangle
  const title = operationTitle(operation)
  const statusLabel = operation.status.replace(/_/g, ' ')
  const showStatusBadge = !(operation.status === 'failed' && title.toLowerCase().includes('failed'))

  return (
    <div className="m-4 mb-0 card-inner-lg">
      <div className="flex items-start gap-3">
        <Icon className={clsx('mt-0.5 h-5 w-5 shrink-0', SEVERITY_TEXT[tone])} />
        <div className="min-w-0 flex-1">
          <div className="flex flex-wrap items-center gap-2">
            <span className="text-sm font-medium text-theme-text-primary">{title}</span>
            {showStatusBadge && (
              <span className={clsx('badge-sm', SEVERITY_BADGE[tone])}>{statusLabel}</span>
            )}
            <OperationRevisionChips operation={operation} />
          </div>
          <p className="mt-1 text-sm text-theme-text-secondary">{operation.message}</p>
          {operation.failureDescription && (
            <Tooltip content={operation.failureDescription} wrapperClassName="mt-1 flex">
              <span className="line-clamp-2 text-xs text-theme-text-tertiary">
                {operation.failureDescription}
              </span>
            </Tooltip>
          )}
          {operation.kind === 'upgrade_rolled_back' && (
            <p className="mt-1 text-xs text-theme-text-tertiary">
              Helm history does not record whether <code className="inline-code text-[11px]">--atomic</code> was set; the rollback is inferred from adjacent release revisions.
            </p>
          )}
          <OperationInsightSignals
            insight={operationInsight}
            releaseNamespace={releaseNamespace}
            canCompare={Boolean(canCompare)}
            onCompare={onCompare}
            onNavigateToResource={onNavigateToResource}
          />
          {primaryHookDiagnostic && <HookSignal diagnostic={primaryHookDiagnostic} />}
          {managedByFluxHelmRelease && (
            <p className="mt-1 text-xs text-theme-text-tertiary">
              This release is managed by Flux HelmRelease {managedByFluxHelmRelease}; direct Helm changes may be reconciled back.
            </p>
          )}
        </div>
      </div>
    </div>
  )
}

function OperationInsightSignals({
  insight,
  releaseNamespace,
  canCompare,
  onCompare,
  onNavigateToResource,
}: {
  insight?: HelmOperationInsight
  releaseNamespace: string
  canCompare: boolean
  onCompare?: (rev1: number, rev2: number) => void
  onNavigateToResource?: NavigateToResource
}) {
  const compare = insight?.suggestedCompare
  const primaryResource = insight?.primaryResource
  const relatedCount = primaryResource
    ? Math.max(0, (insight?.signalCount ?? 0) - 1)
    : insight?.relatedResources?.length ?? 0
  const showCompare = Boolean(compare && canCompare && onCompare)
  if (!primaryResource && !showCompare) {
    return null
  }

  return (
    <div className="mt-2 space-y-2">
      {primaryResource && (
        <PrimaryOperationResourceSignal
          resource={primaryResource}
          releaseNamespace={releaseNamespace}
          relatedCount={relatedCount}
          onNavigateToResource={onNavigateToResource}
        />
      )}
      {showCompare && compare && (
        <Tooltip content={compare.reason || 'Compare the two Helm revisions'}>
          <button
            type="button"
            onClick={() => onCompare?.(compare.revision1, compare.revision2)}
            className="inline-flex items-center gap-1.5 rounded-md border border-theme-border-light bg-theme-elevated px-2.5 py-1.5 text-xs font-medium text-theme-text-primary shadow-theme-sm transition-colors hover:bg-theme-hover focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent"
          >
            <GitCompare className="h-3.5 w-3.5" />
            Compare rev {compare.revision1} to {compare.revision2}
          </button>
        </Tooltip>
      )}
    </div>
  )
}

function PrimaryOperationResourceSignal({
  resource,
  releaseNamespace,
  relatedCount,
  onNavigateToResource,
}: {
  resource: HelmOwnedResource
  releaseNamespace: string
  relatedCount: number
  onNavigateToResource?: NavigateToResource
}) {
  const canNavigate = Boolean(onNavigateToResource)
  const resourceLabel = `${resource.kind}/${resource.name}`
  const resourceTarget = resource.namespace ? `${resource.namespace}/${resource.name}` : resource.name
  const showNamespace = Boolean(resource.namespace && resource.namespace !== releaseNamespace)
  const resourceTooltip = `Open ${resource.kind} ${resourceTarget}`
  const showInlineReady = Boolean(resource.ready && !resource.summary)
  const openResource = () => {
    onNavigateToResource?.({
      kind: kindToPlural(resource.kind),
      namespace: resource.namespace,
      name: resource.name,
      group: apiVersionToGroup(resource.apiVersion),
    })
  }

  return (
    <div className="rounded-md bg-theme-base/50 p-2 text-xs">
      <div className="flex flex-wrap items-center gap-2">
        <span className="font-medium text-theme-text-secondary">Likely resource to inspect</span>
        {canNavigate ? (
          <Tooltip content={resourceTooltip}>
            <button
              type="button"
              onClick={openResource}
              aria-label={resourceTooltip}
              className={clsx(
                'badge-sm max-w-full min-w-0 transition-colors hover:brightness-95 focus:outline-none focus:ring-2 focus:ring-accent focus:ring-offset-1 focus:ring-offset-theme-base',
                getKindBadgeColor(resource.kind)
              )}
            >
              <span className="min-w-0 truncate">{resourceLabel}</span>
              <ExternalLink className="h-3 w-3 shrink-0 opacity-70" />
            </button>
          </Tooltip>
        ) : (
          <span className={clsx('badge-sm max-w-full min-w-0', getKindBadgeColor(resource.kind))}>
            <span className="min-w-0 truncate">{resourceLabel}</span>
          </span>
        )}
        {showNamespace && <span className="text-theme-text-tertiary">ns/{resource.namespace}</span>}
        {showInlineReady && <span className="font-mono text-theme-text-secondary">{resource.ready}</span>}
        {resource.status && (
          <span className={clsx('badge-sm', getResourceStatusColor(resource.status))}>{resource.status}</span>
        )}
      </div>
      {(resource.issue || resource.summary || resource.message || relatedCount > 0) && (
        <div className="mt-1 text-theme-text-tertiary">
          {[
            resource.issue || resource.summary || resource.message,
            relatedCount > 0 ? `${relatedCount} more affected resource${relatedCount === 1 ? '' : 's'} in Resources tab` : null,
          ]
            .filter(Boolean)
            .join(' - ')}
        </div>
      )}
    </div>
  )
}

function hookDiagnosticTone(diagnostic: HookDiagnostic): 'error' | 'warning' {
  return diagnostic.phase.toLowerCase() === 'failed' ? 'error' : 'warning'
}

function HookSignal({ diagnostic }: { diagnostic: HookDiagnostic }) {
  return (
    <div className="mt-2 rounded-md bg-theme-base/50 p-2 text-xs">
      <div className="font-medium text-theme-text-secondary">
        Hook signal: {formatHookRef(diagnostic)} is {diagnostic.phase}
      </div>
      <div className="mt-1 text-theme-text-tertiary">{diagnostic.message}</div>
      {diagnostic.evidence?.summary && (
        <div className="mt-1 text-theme-text-secondary">{diagnostic.evidence.summary}</div>
      )}
      {diagnostic.evidenceUnavailableReason && (
        <div className="mt-1 text-theme-text-tertiary">{diagnostic.evidenceUnavailableReason}</div>
      )}
    </div>
  )
}

function shouldShowOperationBanner(operation: HelmOperation): boolean {
  return operation.kind === 'upgrade_rolled_back' || operation.kind === 'rollback' || operation.status === 'failed' || operation.status === 'stuck_pending'
}

function operationTitle(operation: HelmOperation): string {
  switch (operation.kind) {
    case 'upgrade_rolled_back':
      return 'Helm rolled back after failed upgrade'
    case 'rollback':
      return 'Helm rollback applied'
    case 'pending':
      return 'Helm operation may be stuck'
    case 'upgrade_failed':
      return 'Helm upgrade failed'
    case 'release_failed':
      return 'Helm release failed'
    default:
      return 'Helm operation'
  }
}

function OperationRevisionChips({ operation }: { operation: HelmOperation }) {
  const chips: Array<{ key: string; label: string; className: string }> = []
  if (operation.failedRevision) {
    chips.push({ key: 'failed', label: `failed rev ${operation.failedRevision}`, className: SEVERITY_BADGE.error })
  }
  if (operation.rollbackRevision) {
    chips.push({ key: 'rollback', label: `rollback rev ${operation.rollbackRevision}`, className: SEVERITY_BADGE.warning })
  }
  if (operation.targetRevision) {
    chips.push({ key: 'target', label: `target rev ${operation.targetRevision}`, className: SEVERITY_BADGE.info })
  }
  if (!operation.failedRevision && !operation.rollbackRevision && operation.revision) {
    chips.push({ key: 'revision', label: `rev ${operation.revision}`, className: SEVERITY_BADGE.neutral })
  }
  if (chips.length === 0) {
    return null
  }
  return (
    <>
      {chips.map((chip) => (
        <span key={chip.key} className={clsx('badge-sm', chip.className)}>{chip.label}</span>
      ))}
    </>
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
                    'badge-sm',
                    dep.enabled
                      ? SEVERITY_BADGE.success
                      : SEVERITY_BADGE.neutral
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
  hookDiagnostics: HookDiagnostic[]
}

function HooksTab({ hooks, hookDiagnostics }: HooksTabProps) {
  if (hooks.length === 0) {
    return (
      <div className="flex flex-col items-center justify-center h-48 text-theme-text-tertiary">
        <Anchor className="w-8 h-8 mb-2 opacity-50" />
        <p>No hooks defined for this release</p>
      </div>
    )
  }

  const getHookStatusColor = (status?: string) => {
    if (!status) return SEVERITY_BADGE.neutral
    switch (status.toLowerCase()) {
      case 'succeeded':
        return SEVERITY_BADGE.success
      case 'failed':
        return SEVERITY_BADGE.error
      case 'running':
        return SEVERITY_BADGE.info
      default:
        return SEVERITY_BADGE.neutral
    }
  }

  const getEventColor = (event: string) => {
    if (event.includes('delete')) return SEVERITY_BADGE.error
    if (event.includes('install')) return SEVERITY_BADGE.success
    if (event.includes('upgrade')) return SEVERITY_BADGE.info
    if (event.includes('rollback')) return SEVERITY_BADGE.warning
    return SEVERITY_BADGE.neutral
  }

  const diagnostics = new Map<string, HookDiagnostic>()
  for (const diagnostic of hookDiagnostics) {
    diagnostics.set(hookDiagnosticKey(diagnostic.namespace, diagnostic.kind, diagnostic.name), diagnostic)
    diagnostics.set(hookDiagnosticKey(undefined, diagnostic.kind, diagnostic.name), diagnostic)
  }

  return (
    <div className="p-4 space-y-3">
      {hooks.map((hook) => {
        const diagnostic = diagnostics.get(hookDiagnosticKey(hook.namespace, hook.kind, hook.name))
          || diagnostics.get(hookDiagnosticKey(undefined, hook.kind, hook.name))
        return (
        <div key={`${hook.namespace || '_'}:${hook.kind}:${hook.name}`} className="bg-theme-elevated/30 rounded-lg p-4">
          <div className="flex items-start justify-between gap-3 mb-2">
            <div className="min-w-0">
              <div className="flex flex-wrap items-center gap-2">
                <span className="break-all text-theme-text-primary font-medium">{hook.name}</span>
                <span className="badge-sm shrink-0 bg-theme-hover/50 text-theme-text-secondary">
                  {hook.kind}
                </span>
              </div>
              <div className="mt-1 flex flex-wrap gap-x-3 gap-y-1 text-xs text-theme-text-tertiary">
                {hook.namespace && <span className="break-all">Namespace: {hook.namespace}</span>}
                <span>Weight: {hook.weight}</span>
                {hook.path && <span className="break-all">Path: {hook.path}</span>}
              </div>
            </div>
            {hook.status && (
              <span className={clsx('badge', getHookStatusColor(hook.status))}>
                {hook.status}
              </span>
            )}
          </div>
          <div className="flex flex-wrap gap-1.5 mt-2">
            {hook.events.map((event) => (
              <span
                key={event}
                className={clsx('badge', getEventColor(event))}
              >
                {event}
              </span>
            ))}
          </div>
          {(hook.startedAt || hook.completedAt || hook.deletePolicies?.length || hook.outputLogPolicies?.length) && (
            <div className="mt-3 grid grid-cols-1 gap-2 text-xs text-theme-text-tertiary sm:grid-cols-2">
              {hook.startedAt && (
                <div>
                  <span className="text-theme-text-disabled">Started: </span>
                  <span>{formatDate(hook.startedAt)}</span>
                </div>
              )}
              {hook.completedAt && (
                <div>
                  <span className="text-theme-text-disabled">Completed: </span>
                  <span>{formatDate(hook.completedAt)}</span>
                </div>
              )}
              {hook.deletePolicies && hook.deletePolicies.length > 0 && (
                <div className="sm:col-span-2">
                  <span className="text-theme-text-disabled">Delete policies: </span>
                  <span>{hook.deletePolicies.join(', ')}</span>
                </div>
              )}
              {hook.outputLogPolicies && hook.outputLogPolicies.length > 0 && (
                <div className="sm:col-span-2">
                  <span className="text-theme-text-disabled">Output log policies: </span>
                  <span>{hook.outputLogPolicies.join(', ')}</span>
                </div>
              )}
            </div>
          )}
          {diagnostic && <HookDiagnosticEvidence diagnostic={diagnostic} />}
        </div>
        )
      })}
    </div>
  )
}

function hookDiagnosticKey(namespace: string | undefined, kind: string | undefined, name: string | undefined): string {
  return `${namespace || ''}/${(kind || '').toLowerCase()}/${name || ''}`
}

function formatHookRef(hook: Pick<HookDiagnostic, 'namespace' | 'kind' | 'name'>): string {
  return `${hook.namespace ? `${hook.namespace}/` : ''}${hook.name} (${hook.kind})`
}

function HookDiagnosticEvidence({ diagnostic }: { diagnostic: HookDiagnostic }) {
  const evidence = diagnostic.evidence

  return (
    <div className="mt-3 border-t border-theme-border/60 pt-3 text-xs">
      <div className="flex flex-wrap items-center gap-2">
        <span className="font-medium text-theme-text-secondary">Diagnostic evidence</span>
        <span className={clsx('badge-sm', diagnostic.phase.toLowerCase() === 'failed' ? SEVERITY_BADGE.error : SEVERITY_BADGE.warning)}>
          {diagnostic.phase}
        </span>
        {evidence?.summary && <span className="text-theme-text-tertiary">{evidence.summary}</span>}
      </div>
      <div className="mt-1 text-theme-text-tertiary">{diagnostic.message}</div>
      {diagnostic.evidenceUnavailableReason && (
        <div className="mt-1 text-theme-text-tertiary">{diagnostic.evidenceUnavailableReason}</div>
      )}
      {evidence && (
        <div className="mt-3 space-y-3">
          {evidence.jobs && evidence.jobs.length > 0 && (
            <div className="space-y-1">
              {evidence.jobs.map((job) => (
                <div key={`${job.namespace || ''}/${job.name}`} className="grid grid-cols-[80px_1fr] gap-2 rounded bg-theme-base/40 px-2 py-1">
                  <span className="text-theme-text-tertiary">Job</span>
                  <span className="min-w-0 text-theme-text-primary">
                    <span className="break-all font-medium">{job.namespace ? `${job.namespace}/` : ''}{job.name}</span>
                    {job.status && <span className="ml-2 text-theme-text-secondary">{job.status}</span>}
                    <span className="ml-2 text-theme-text-tertiary">
                      active {job.active || 0} · succeeded {job.succeeded || 0} · failed {job.failed || 0}
                    </span>
                  </span>
                  {job.conditions?.map((condition) => (
                    <span key={condition} className="col-start-2 text-theme-text-tertiary">{condition}</span>
                  ))}
                </div>
              ))}
            </div>
          )}

          {evidence.pods && evidence.pods.length > 0 && (
            <div className="space-y-1">
              {evidence.pods.map((pod) => (
                <div key={`${pod.namespace || ''}/${pod.name}`} className="grid grid-cols-[80px_1fr] gap-2 rounded bg-theme-base/40 px-2 py-1">
                  <span className="text-theme-text-tertiary">Pod</span>
                  <span className="min-w-0 text-theme-text-primary">
                    <span className="break-all font-medium">{pod.namespace ? `${pod.namespace}/` : ''}{pod.name}</span>
                    {pod.phase && <span className="ml-2 text-theme-text-secondary">{pod.phase}</span>}
                    {pod.ready && <span className="ml-2 text-theme-text-tertiary">{pod.ready} ready</span>}
                    {Boolean(pod.restartCount) && <span className="ml-2 text-theme-text-tertiary">{pod.restartCount} restarts</span>}
                  </span>
                  {(pod.reason || pod.message) && (
                    <span className="col-start-2 text-theme-text-tertiary">
                      {pod.reason && <span className="font-medium">{pod.reason}: </span>}
                      {pod.message}
                    </span>
                  )}
                </div>
              ))}
            </div>
          )}

          {evidence.events && evidence.events.length > 0 && (
            <div className="space-y-1">
              {evidence.events.map((event, index) => (
                <div key={`${event.involvedKind}/${event.involvedName}/${event.reason || index}`} className="grid grid-cols-[80px_1fr] gap-2 rounded bg-theme-base/40 px-2 py-1">
                  <span className="text-theme-text-tertiary">Event</span>
                  <span className="min-w-0 text-theme-text-primary">
                    <span className={clsx('badge-sm mr-2', event.type === 'Warning' ? SEVERITY_BADGE.warning : SEVERITY_BADGE.neutral)}>
                      {event.type || 'Event'}
                    </span>
                    <span className="font-medium">{event.reason || 'Unknown'}</span>
                    <span className="ml-2 break-all text-theme-text-tertiary">{event.involvedKind}/{event.involvedName}</span>
                  </span>
                  {event.message && <span className="col-start-2 text-theme-text-tertiary">{event.message}</span>}
                </div>
              ))}
            </div>
          )}

          {evidence.logs && evidence.logs.length > 0 && (
            <div className="space-y-2">
              {evidence.logs.map((log, index) => (
                <HookLogEvidenceBlock key={`${log.pod}/${log.container}/${log.previous ? 'previous' : 'current'}/${index}`} log={log} />
              ))}
            </div>
          )}

          {evidence.errors && evidence.errors.length > 0 && (
            <div className="space-y-1 text-theme-text-tertiary">
              {evidence.errors.map((error) => (
                <div key={error}>Evidence read error: {error}</div>
              ))}
            </div>
          )}
        </div>
      )}
    </div>
  )
}

function HookLogEvidenceBlock({ log }: { log: HookLogEvidence }) {
  const visibleLines = (log.lines || []).slice(0, 8)

  return (
    <div className="rounded bg-theme-base/40 px-2 py-2">
      <div className="mb-1 flex flex-wrap items-center gap-2 text-theme-text-secondary">
        <span className="font-medium">Logs</span>
        <span className="break-all">{log.pod}/{log.container}</span>
        {log.previous && <span className="badge-sm bg-theme-hover/50 text-theme-text-secondary">previous</span>}
        {log.fallback && <span className="text-theme-text-tertiary">fallback tail</span>}
      </div>
      {log.error ? (
        <div className="text-theme-text-tertiary">{log.error}</div>
      ) : (
        <>
          <pre className="max-h-44 overflow-auto whitespace-pre-wrap rounded bg-theme-base px-2 py-1 font-mono text-[11px] leading-5 text-theme-text-secondary">
            {visibleLines.join('\n')}
          </pre>
          {log.lines && log.lines.length > visibleLines.length && (
            <div className="mt-1 text-theme-text-tertiary">+{log.lines.length - visibleLines.length} more lines in API response</div>
          )}
        </>
      )}
    </div>
  )
}
