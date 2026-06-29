import { useEffect, type ComponentType, type ReactNode } from 'react'
import { ArrowDownUp, ChevronDown, ChevronRight, Clock3, GitBranch, GitCommit, Loader2, Pause, Play, RefreshCw, Settings, Trash2, XCircle, Zap } from 'lucide-react'
import { PaneLoader } from '../ui/PaneLoader'

import { HealthStatusBadge, SyncStatusBadge } from './GitOpsStatusBadge'
import { GitOpsIssuesBand, GitOpsStatusStrip } from './insights'
import { Tooltip } from '../ui/Tooltip'
import type { GitOpsHealthStatus, GitOpsInsight, GitOpsIssue, GitOpsRemediation, SyncStatus } from '../../types'

// ---------------------------------------------------------------------------
// Props
// ---------------------------------------------------------------------------

export interface GitOpsDetailIdentity {
  kind: string           // 'applications' | 'kustomizations' | ...
  group: string          // 'argoproj.io' | 'kustomize.toolkit.fluxcd.io' | ''
  namespace: string
  name: string
  // toolLabel: "ArgoCD" | "FluxCD" — passed through as a chip; kept as a
  // free string so future tools (Komodor, Argo Workflows, etc.) don't need
  // an enum bump here.
  toolLabel: string
  // kindLabel: human-facing kind (e.g. "Application"). Caller does the
  // lookup against its API resource catalog.
  kindLabel: string
}

export interface GitOpsDetailLineage {
  // Parent in a nested-GitOps chain (app-of-apps, Kustomization-applies-
  // Kustomization). Renders an extra clickable breadcrumb segment.
  kind: string
  namespace: string
  name: string
  group: string
}

export interface GitOpsDetailStatus {
  sync: SyncStatus
  health: GitOpsHealthStatus
  // suspended is the *effective* suspended state (status-derived OR
  // suspended-by-Radar via annotations). Caller resolves the OR.
  suspended: boolean
}

export interface GitOpsDetailMetadata {
  // The "spec/config" facts under the title. Caller pre-formats each
  // string — formatSourceRepo, formatDestination, etc.
  project?: string
  repository?: string
  path?: string
  chart?: string
  destination?: string
  destinationNamespace?: string
  // autoSyncMode: free-string description from insights.summary
  // (e.g. "auto-sync (prune)"). Renders as a fact when present.
  autoSyncMode?: string
}

export interface ArgoActionHandlers {
  onSyncRequested: () => void                    // opens caller's sync options dialog
  onRefresh: (kind: 'normal' | 'hard') => void
  onTerminate: () => void
  onSuspend: () => void
  onResume: () => void
  // Per-action busy state for spinner rendering. Caller maintains these
  // from React Query mutations or equivalent.
  syncing: boolean
  refreshing: boolean
  // refreshingKind: which refresh is in flight — 'normal' or 'hard'.
  // Drives which button shows the spinner so the caller doesn't have to
  // disambiguate from a single boolean.
  refreshingKind: 'normal' | 'hard'
  terminating: boolean
  suspending: boolean
  resuming: boolean
  // autoSyncEnabled: caller derives from spec.syncPolicy.automated.
  autoSyncEnabled: boolean
  // isRunning: caller derives from status.operationState.phase === 'Running'.
  isRunning: boolean
}

export interface FluxActionHandlers {
  onReconcile: () => void
  onSyncWithSource: () => void  // only invoked for Kustomization + HelmRelease
  onSuspend: () => void
  onResume: () => void
  reconciling: boolean
  syncingWithSource: boolean
  suspending: boolean
  resuming: boolean
}

export interface GitOpsHelmValuesData {
  yaml: string
  keyCount: number
  source: 'flux' | 'argo-object' | 'argo-string' | 'argo-parameters'
}

export type GitOpsDetailTab = 'topology' | 'changes' | 'activity'

export interface GitOpsDetailLayoutProps {
  // Identity
  identity: GitOpsDetailIdentity
  // Lineage breadcrumb (parent GitOps CR), null when none.
  parent?: GitOpsDetailLineage | null

  // Status
  status: GitOpsDetailStatus | null
  // Caller resolves terminating + tooltip text. `actionDisabledTooltip` is
  // what disabled action buttons show; `chipTooltip` is what the
  // [Terminating] chip shows.
  terminating: boolean
  terminatingChipTooltip: string
  terminatingActionTooltip: string

  // Spec/config row
  detail: GitOpsDetailMetadata

  // Insights — drives StatusStrip + IssuesBand
  insight: GitOpsInsight | null
  insightLoading: boolean
  // Issue interactions — caller wires onSelectIssue to jump to Changes tab
  // and highlight the affected resource.
  onSelectIssue?: (issue: GitOpsIssue) => void
  // Remediation — caller handles each remediation kind (e.g. create-namespace
  // applies a manifest and triggers a follow-on sync).
  onRemediate?: (remediation: GitOpsRemediation) => void
  remediationPending?: boolean

  // Helm values disclosure — caller renders the values payload; this prop
  // controls whether the disclosure shows up + its open/closed state.
  helmValues?: GitOpsHelmValuesData | null
  helmValuesOpen?: boolean
  onToggleHelmValues?: () => void
  // helmValuesContent is the rendered YAML viewer (CodeViewer in OSS;
  // hub-web brings its own). Kept as a slot so the layout doesn't depend
  // on the OSS-specific CodeViewer component.
  helmValuesContent?: ReactNode

  // Action buttons — Argo + Flux. Exactly one applies for any given CR.
  isArgoApp: boolean
  isFlux: boolean
  isFluxWorkload: boolean  // Kustomization | HelmRelease — gates the
                            // "Sync with source" button
  argo?: ArgoActionHandlers
  flux?: FluxActionHandlers

  // Tab state
  activeTab: GitOpsDetailTab
  onTabChange: (tab: GitOpsDetailTab) => void
  // Fullscreen mode hides the page chrome (header + status strip + tabs
  // become a single slim title bar) and lets the body region expand to
  // fill the viewport. Caller toggles + tracks state.
  fullscreen: boolean
  onToggleFullscreen: () => void
  // Tab body — caller renders the body for the active tab. The shell
  // provides the tab bar + chrome; the body is fully caller-owned so
  // hub-web and OSS can wire their own data sources without the layout
  // knowing.
  renderTabBody: (ctx: { tab: GitOpsDetailTab; fullscreen: boolean }) => ReactNode
  // Optional: top-right tab-bar accessory (Clear filters + Fullscreen
  // buttons in OSS, can be anything else in hub-web).
  renderTabBarAccessory?: (ctx: { tab: GitOpsDetailTab; fullscreen: boolean }) => ReactNode
  // Optional: counts/summary string in the middle of the tab bar (e.g.
  // OSS's TopologyCounts). Caller decides when to render it.
  renderTabBarCounts?: (ctx: { tab: GitOpsDetailTab }) => ReactNode

  // Loading / error state for the underlying resource fetch — caller
  // surfaces these on the body region (shell renders a spinner / error
  // banner that replaces the body).
  resourceLoading?: boolean
  resourceError?: Error | null

  // Navigation
  onNavigateRoot: () => void                  // back to /gitops list
  onNavigateParent?: () => void               // breadcrumb parent click
  // (No keyboard-shortcut prop on the layout — registration is
  // router-aware so callers wire `useRegisterShortcut` or equivalent
  // themselves.)

  // Fleet context — when true, render a destination cluster chip in the
  // header next to the title. Hub-web sets this to surface cross-cluster
  // lineage; OSS leaves it false so single-cluster Radar's UX is unchanged.
  isFleetContext?: boolean
  destinationCluster?: { id: string; name: string }

  // Tab-title side effect — sets document.title to "<name> · Radar" while
  // mounted, restores on unmount. Opt-in so hub-web fleet detail can pick
  // its own title format ("<name> — Fleet GitOps").
  manageDocumentTitle?: boolean
  documentTitleSuffix?: string                // defaults to " · Radar"

  // Children slot — for dialogs (SyncOptionsDialog, RollbackDialog, …)
  // that should portal to body. Caller owns dialog state. Children render
  // outside the layout's main flex column.
  children?: ReactNode
}

// ---------------------------------------------------------------------------
// Layout
// ---------------------------------------------------------------------------

export function GitOpsDetailLayout(props: GitOpsDetailLayoutProps) {
  const {
    identity,
    parent,
    status,
    terminating,
    terminatingChipTooltip,
    terminatingActionTooltip,
    detail,
    insight,
    insightLoading,
    onSelectIssue,
    onRemediate,
    remediationPending,
    helmValues,
    helmValuesOpen,
    onToggleHelmValues,
    helmValuesContent,
    isArgoApp,
    isFlux,
    isFluxWorkload,
    argo,
    flux,
    activeTab,
    onTabChange,
    fullscreen,
    onToggleFullscreen,
    renderTabBody,
    renderTabBarAccessory,
    renderTabBarCounts,
    resourceLoading,
    resourceError,
    onNavigateRoot,
    onNavigateParent,
    isFleetContext,
    destinationCluster,
    manageDocumentTitle,
    documentTitleSuffix,
    children,
  } = props

  // Document title side effect — opt-in so hub-web can take ownership of
  // its own title format.
  useEffect(() => {
    if (!manageDocumentTitle) return
    const previous = document.title
    const suffix = documentTitleSuffix ?? ' · Radar'
    document.title = `${identity.name}${suffix}`
    return () => { document.title = previous }
  }, [identity.name, manageDocumentTitle, documentTitleSuffix])

  const effectiveSuspended = status?.suspended ?? false
  // graphFullscreen hides everything chrome-side; the body region expands.
  // Mirrors the OSS GitOpsDetailView shell behavior so the visual chrome
  // class set carries through.
  const bodyShellClass = fullscreen
    ? 'fixed inset-0 z-[80] flex min-h-0 min-w-0 flex-col bg-theme-base'
    : 'flex min-h-0 min-w-0 flex-1 flex-col overflow-hidden'

  return (
    <div className="flex h-full min-h-0 min-w-0 flex-1 flex-col overflow-hidden bg-theme-base">
      {!fullscreen && (
        <div className="shrink-0 border-b border-theme-border bg-theme-base px-4 py-3">
          <div className="flex flex-wrap items-start justify-between gap-3">
            <div className="min-w-0">
              {/* Breadcrumb row */}
              <div className="flex flex-wrap items-center gap-2">
                <button
                  type="button"
                  onClick={onNavigateRoot}
                  className="shrink-0 text-xs font-medium text-sky-500 transition-colors hover:text-sky-400"
                >
                  GitOps
                </button>
                <span className="shrink-0 text-xs text-theme-text-tertiary">/</span>
                {parent && onNavigateParent && (
                  <>
                    <button
                      type="button"
                      onClick={onNavigateParent}
                      className="shrink-0 truncate max-w-[200px] text-xs font-medium text-sky-500 transition-colors hover:text-sky-400"
                      title={`Open parent: ${parent.namespace ? `${parent.namespace}/` : ''}${parent.name}`}
                    >
                      {parent.namespace ? `${parent.namespace}/` : ''}{parent.name}
                    </button>
                    <span className="shrink-0 text-xs text-theme-text-tertiary">/</span>
                  </>
                )}
                <h1 className="min-w-0 truncate text-lg font-semibold text-theme-text-primary">
                  {identity.namespace ? `${identity.namespace}/` : ''}{identity.name}
                </h1>
                {/* Status badges suppressed on Terminating — last-observed
                    sync/health values become stale the moment deletion is
                    initiated; the Terminating chip becomes the sole status
                    indicator. */}
                {status && !terminating && (
                  <>
                    <SyncStatusBadge sync={status.sync} suspended={effectiveSuspended} />
                    {!effectiveSuspended && <HealthStatusBadge health={status.health} />}
                  </>
                )}
                {terminating && (
                  <Tooltip content={terminatingChipTooltip}>
                    <span className="badge border border-orange-500/40 bg-orange-500/10 text-orange-400">
                      <Trash2 className="h-3 w-3" />
                      Terminating
                    </span>
                  </Tooltip>
                )}
                <span className="inline-flex shrink-0 items-center rounded border border-theme-border bg-theme-hover/50 px-1.5 py-0.5 text-[11px] font-medium text-theme-text-secondary">
                  {identity.toolLabel} · {identity.kindLabel}
                </span>
                {isFleetContext && destinationCluster && (
                  // Cross-cluster lineage chip — fleet-only. Renders the
                  // destination cluster alongside the tool/kind chip so
                  // operators see at a glance which cluster the workloads
                  // actually live in. Clickable navigation lives one level
                  // up (the chip in the fleet list page) so the detail
                  // header stays compact.
                  <span className="inline-flex shrink-0 items-center gap-1 rounded border border-sky-500/40 bg-sky-500/10 px-1.5 py-0.5 text-[11px] font-medium text-sky-400">
                    Deployed to: {destinationCluster.name}
                  </span>
                )}
              </div>
              {/* Spec/config row */}
              <div className="mt-2 flex flex-wrap gap-x-5 gap-y-0.5 text-[11px] text-theme-text-tertiary">
                <AppFact label="Project" value={detail.project || '-'} />
                {detail.repository && <AppFact label="Source" value={detail.repository} />}
                {detail.path && <AppFact label="Path" value={detail.path} />}
                {detail.chart && <AppFact label="Chart" value={detail.chart} />}
                <AppFact label="Destination" value={detail.destination || '-'} />
                {detail.autoSyncMode && <AppFact label="Sync mode" value={detail.autoSyncMode} />}
              </div>
            </div>
            {/* Action buttons */}
            <div className="flex flex-wrap items-center gap-2">
              {isArgoApp && argo && (
                <>
                  <ActionButton
                    label="Sync…"
                    description="Apply manifests from Git to the cluster. Opens an options dialog (prune, dry-run, revision)."
                    icon={ArrowDownUp}
                    loading={argo.syncing}
                    onClick={argo.onSyncRequested}
                    disabled={effectiveSuspended || terminating}
                    disabledReason={terminating ? terminatingActionTooltip : undefined}
                    primary
                  />
                  <ActionButton
                    label="Refresh"
                    description="Re-check Git for new commits and recompute sync status. Doesn't apply anything."
                    icon={RefreshCw}
                    loading={argo.refreshing && argo.refreshingKind === 'normal'}
                    onClick={() => argo.onRefresh('normal')}
                  />
                  <ActionButton
                    label="Hard refresh"
                    description="Like Refresh, but also bypasses Argo's manifest cache (re-renders Helm/Kustomize)."
                    icon={Zap}
                    loading={argo.refreshing && argo.refreshingKind === 'hard'}
                    onClick={() => argo.onRefresh('hard')}
                  />
                  {argo.isRunning && (
                    <ActionButton
                      label="Terminate"
                      description="Cancel the in-progress sync operation."
                      icon={XCircle}
                      loading={argo.terminating}
                      onClick={argo.onTerminate}
                      danger
                    />
                  )}
                  {argo.autoSyncEnabled ? (
                    <ActionButton
                      label="Disable auto-sync"
                      description="Stop Argo from automatically syncing Git changes. Manual Sync still works."
                      icon={Pause}
                      loading={argo.suspending}
                      onClick={argo.onSuspend}
                      disabled={terminating}
                      disabledReason={terminating ? terminatingActionTooltip : undefined}
                    />
                  ) : (
                    <ActionButton
                      label="Enable auto-sync"
                      description="Re-enable automatic syncing of Git changes to the cluster."
                      icon={Play}
                      loading={argo.resuming}
                      onClick={argo.onResume}
                      disabled={terminating}
                      disabledReason={terminating ? terminatingActionTooltip : undefined}
                    />
                  )}
                </>
              )}
              {isFlux && flux && (
                <>
                  <ActionButton
                    label="Reconcile"
                    description="Tell Flux to reconcile this resource now: re-read its source, re-apply the manifests, update status. Skips waiting for the regular reconciliation interval."
                    icon={RefreshCw}
                    loading={flux.reconciling}
                    onClick={flux.onReconcile}
                    disabled={effectiveSuspended || terminating}
                    disabledReason={terminating ? terminatingActionTooltip : undefined}
                    primary
                  />
                  {isFluxWorkload && (
                    <ActionButton
                      label="Sync with source"
                      description="Reconcile the upstream source CR (GitRepository/HelmRepository) first — re-fetching from Git or Helm — then reconcile this resource against the refreshed source."
                      icon={GitCommit}
                      loading={flux.syncingWithSource}
                      onClick={flux.onSyncWithSource}
                      disabled={terminating}
                      disabledReason={terminating ? terminatingActionTooltip : undefined}
                    />
                  )}
                  {effectiveSuspended ? (
                    <ActionButton
                      label="Resume"
                      description="Resume Flux reconciliation. Flux will start applying changes from the source again on its normal interval."
                      icon={Play}
                      loading={flux.resuming}
                      onClick={flux.onResume}
                      disabled={terminating}
                      disabledReason={terminating ? terminatingActionTooltip : undefined}
                    />
                  ) : (
                    <ActionButton
                      label="Suspend"
                      description="Pause Flux reconciliation. The resource stays exactly as-is — new commits in the source won't be applied until you resume."
                      icon={Pause}
                      loading={flux.suspending}
                      onClick={flux.onSuspend}
                      disabled={terminating}
                      disabledReason={terminating ? terminatingActionTooltip : undefined}
                    />
                  )}
                </>
              )}
            </div>
          </div>
        </div>
      )}
      {!fullscreen && (
        <>
          <GitOpsStatusStrip insight={insight ?? undefined} loading={insightLoading} />
          <GitOpsIssuesBand
            issues={insight?.issues}
            terminating={terminating}
            onSelectIssue={onSelectIssue}
            remediationPending={remediationPending}
            onRemediate={onRemediate}
          />
          {helmValues && onToggleHelmValues && (
            <div className="shrink-0 border-b border-theme-border bg-theme-base">
              <button
                type="button"
                onClick={onToggleHelmValues}
                className="flex w-full items-center gap-2 px-4 py-2 text-left text-xs text-theme-text-secondary hover:bg-theme-hover"
                aria-expanded={helmValuesOpen}
              >
                {helmValuesOpen ? (
                  <ChevronDown className="h-3.5 w-3.5 shrink-0" />
                ) : (
                  <ChevronRight className="h-3.5 w-3.5 shrink-0" />
                )}
                <Settings className="h-3.5 w-3.5 shrink-0 text-theme-text-tertiary" />
                <span className="font-medium text-theme-text-primary">Helm values</span>
                <span className="tabular-nums text-theme-text-tertiary">
                  {helmValues.keyCount} {helmValues.keyCount === 1 ? 'key' : 'keys'}
                </span>
                {helmValues.source === 'argo-parameters' && (
                  <span className="text-[10px] uppercase tracking-wider text-theme-text-tertiary">parameters</span>
                )}
              </button>
              {helmValuesOpen && helmValuesContent && (
                <div className="border-t border-theme-border bg-theme-surface px-4 py-3">
                  {helmValuesContent}
                </div>
              )}
            </div>
          )}
        </>
      )}

      {resourceLoading ? (
        <PaneLoader label="Loading GitOps resource…" className="flex-1" />
      ) : resourceError ? (
        <div className="p-4 text-sm text-red-500">Failed to load resource: {resourceError.message}</div>
      ) : (
        <div className={bodyShellClass}>
          <div className="flex shrink-0 items-center justify-between gap-3 border-b border-theme-border bg-theme-base px-4 py-2">
            <div className="flex items-center gap-1 rounded-md border border-theme-border bg-theme-surface p-1">
              <ViewButton active={activeTab === 'topology'} icon={GitBranch} label="Topology" onClick={() => onTabChange('topology')} />
              <ViewButton active={activeTab === 'changes'} icon={GitCommit} label="Resources" onClick={() => onTabChange('changes')} />
              <ViewButton active={activeTab === 'activity'} icon={Clock3} label="Activity" onClick={() => onTabChange('activity')} />
            </div>
            {fullscreen ? (
              <div className="min-w-0 flex-1 truncate text-sm font-medium text-theme-text-primary">
                {identity.namespace ? `${identity.namespace}/` : ''}{identity.name}
              </div>
            ) : (
              renderTabBarCounts?.({ tab: activeTab })
            )}
            <div className="flex items-center gap-2">
              {renderTabBarAccessory?.({ tab: activeTab, fullscreen })}
              {activeTab === 'topology' && (
                <button
                  type="button"
                  onClick={onToggleFullscreen}
                  className="rounded-md border border-theme-border bg-theme-surface px-2 py-1 text-xs text-theme-text-secondary hover:bg-theme-hover hover:text-theme-text-primary"
                >
                  {fullscreen ? 'Exit fullscreen' : 'Fullscreen'}
                </button>
              )}
            </div>
          </div>

          {renderTabBody({ tab: activeTab, fullscreen })}
        </div>
      )}
      {children}
    </div>
  )
}

// ---------------------------------------------------------------------------
// Small subcomponents kept private to keep the public surface narrow.
// AppFact, ActionButton, ViewButton are tightly coupled to the layout's
// visual language; if a caller needs them standalone we can export later.
// ---------------------------------------------------------------------------

function AppFact({ label, value }: { label: string; value: string }) {
  // inline-flex sizes each fact to its content; flex-wrap on the parent
  // handles row breaks. max-w-full + truncate guard against single facts
  // wider than the screen (very long destination URLs).
  return (
    <span className="inline-flex min-w-0 max-w-full items-baseline gap-1">
      <span className="shrink-0 text-theme-text-tertiary">{label}:</span>
      <Tooltip content={value} delay={400} wrapperClassName="min-w-0">
        <span className="block truncate text-theme-text-primary">{value}</span>
      </Tooltip>
    </span>
  )
}

function ViewButton({
  active,
  icon: Icon,
  label,
  onClick,
}: {
  active: boolean
  icon: ComponentType<{ className?: string }>
  label: string
  onClick: () => void
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      className={`inline-flex items-center gap-1.5 rounded px-2.5 py-1 text-xs font-medium transition-colors ${
        active
          ? 'bg-skyhook-500 text-white'
          : 'text-theme-text-secondary hover:bg-theme-hover hover:text-theme-text-primary'
      }`}
    >
      <Icon className="h-3.5 w-3.5" />
      {label}
    </button>
  )
}

function ActionButton({
  label,
  description,
  icon: Icon,
  loading,
  disabled,
  disabledReason,
  danger,
  primary,
  onClick,
}: {
  label: string
  description?: string
  icon: ComponentType<{ className?: string }>
  loading?: boolean
  disabled?: boolean
  disabledReason?: string
  danger?: boolean
  primary?: boolean
  onClick: () => void
}) {
  // primary → brand fill (one per page); danger → red (destructive);
  // default → bordered ghost on theme surface (secondary actions).
  const variantClass = primary
    ? 'btn-brand'
    : danger
      ? 'border border-red-500/40 bg-red-500/10 text-red-500 hover:bg-red-500/20'
      : 'border border-theme-border bg-theme-surface text-theme-text-secondary hover:bg-theme-hover hover:text-theme-text-primary'
  const tooltip = disabled && disabledReason ? disabledReason : (description || label)
  return (
    <Tooltip content={tooltip}>
      <button
        type="button"
        onClick={onClick}
        disabled={loading || disabled}
        className={`inline-flex items-center gap-1.5 rounded-md px-2.5 py-1.5 text-xs font-medium transition-colors disabled:cursor-not-allowed disabled:opacity-50 ${variantClass}`}
      >
        {loading ? <Loader2 className="h-3.5 w-3.5 animate-spin" /> : <Icon className="h-3.5 w-3.5" />}
        {label}
      </button>
    </Tooltip>
  )
}
