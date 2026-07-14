import { useState, useEffect, useRef, useCallback, type ReactNode } from 'react'
import { createPortal } from 'react-dom'
import {
  Settings, X, RotateCcw, RotateCw, Loader2, Copy, Check, Pin, Shield, Lock, Plug,
  Plus, Terminal, Boxes, Activity, GitBranch, Sparkles, SlidersHorizontal, Zap,
  LayoutDashboard, ChevronRight, ExternalLink, Download, AlertTriangle,
  type LucideIcon,
} from 'lucide-react'
import { clsx } from 'clsx'
import { useAnimatedUnmount } from '../../hooks/useAnimatedUnmount'
import { TRANSITION_BACKDROP, TRANSITION_PANEL } from '../../utils/animation'
import { apiUrl, getAuthHeaders, getCredentialsMode } from '../../api/config'
import {
  useCloudRole, useVersionCheck, useClusterInfo, usePrometheusStatus, useArgoStatus,
} from '../../api/client'
import { useCapabilitiesContext } from '../../contexts/CapabilitiesContext'
import { Input } from '@skyhook-io/k8s-ui'
import { Tooltip } from '../ui/Tooltip'
import { AISettingsSection, type AIDraft } from '../diagnose/AISettings'
import { MyPermissionsContent } from './MyPermissionsDialog'
import { useDiagnose } from '../diagnose/DiagnoseContext'

interface Config {
  kubeconfig?: string
  kubeconfigDirs?: string[]
  namespace?: string
  port?: number
  noBrowser?: boolean
  browser?: string
  timelineStorage?: 'memory' | 'sqlite'
  timelineDbPath?: string
  historyLimit?: number
  prometheusUrl?: string
  argoCdUrl?: string
  argoCdInsecureTls?: boolean
  mcp?: boolean | null
}

interface ConfigResponse {
  file: Config
  effective: Config
  isDesktop: boolean
  prometheusHeaderKeys?: string[]
  // True when an Argo CD auth token is stored. The token itself is never
  // returned — the card shows a "configured" placeholder and omits the token
  // from the PUT unless the user changes or clears it.
  argoCdTokenSet?: boolean
  // True when the integration is provisioned from the environment
  // (RADAR_ARGOCD_TOKEN / _TOKEN_FILE) — the card renders read-only because the
  // server refuses UI edits to a declaratively-configured integration.
  argoCdEnvManaged?: boolean
  // Set when environment provisioning was attempted but failed (bad token file,
  // invalid URL, …) — the read-only card surfaces the reason.
  argoCdEnvError?: string
  // A detected Argo CD CLI login (server + user + TLS mode, no token), so the UI
  // can offer "use your CLI session" as a one-click when it will actually work.
  argoCdCliSession?: { server: string; user: string; insecure?: boolean }
}

interface SettingsDialogProps {
  open: boolean
  onClose: () => void
}

// The settings surface splits into three honest apply buckets:
//   • Startup config (kubeconfig, server, timeline, MCP) — persisted by the
//     owner-gated footer to the config file; effect on next launch.
//   • Live integrations (Prometheus, Argo CD) — their own Apply/Connect endpoints
//     re-point the running server; effect immediately, NOT part of footer dirty.
//   • AI diagnose — client-side prefs, self-saving, editable by everyone.
type SectionId =
  | 'overview' | 'perms' | 'connection' | 'prometheus' | 'argocd' | 'ai' | 'advanced'

// Only STARTUP fields count toward footer dirty. Integration fields (prometheusUrl,
// argoCdUrl, argoCdInsecureTls) apply live and are excluded here. Every field is
// normalized so unset≡default doesn't read as a change.
function normalizeStartup(c: Config) {
  return {
    kubeconfig: c.kubeconfig ?? '',
    kubeconfigDirs: c.kubeconfigDirs && c.kubeconfigDirs.length > 0 ? c.kubeconfigDirs.join('\x00') : '',
    namespace: c.namespace ?? '',
    port: c.port ?? null,
    noBrowser: c.noBrowser ?? false,
    browser: c.browser ?? '',
    timelineStorage: c.timelineStorage ?? 'memory',
    timelineDbPath: c.timelineDbPath ?? '',
    historyLimit: c.historyLimit ?? null,
    mcp: c.mcp ?? true,
  }
}

export function SettingsDialog({ open, onClose }: SettingsDialogProps) {
  const dialogRef = useRef<HTMLDivElement>(null)
  const { shouldRender, isOpen } = useAnimatedUnmount(open, 200)
  const { data: versionInfo } = useVersionCheck()
  // Radar configuration (kubeconfig, port, integrations…) is host-level and
  // affects every user of this instance, so it's gated to owners. Personal
  // sections (My permissions, AI diagnose) stay usable by everyone. Non-Cloud
  // callers (OSS, OIDC, kubectl plugin) have no role and pass — single-user
  // laptops are never locked out of their own config. Backend enforces this too.
  const { canAtLeast } = useCloudRole()
  const capabilities = useCapabilitiesContext()
  const canEditConfig = canAtLeast('owner')

  const [configData, setConfigData] = useState<ConfigResponse | null>(null)
  const [editedConfig, setEditedConfig] = useState<Config>({})
  const [saving, setSaving] = useState(false)
  const [saveMessage, setSaveMessage] = useState<string | null>(null)
  const [loadError, setLoadError] = useState<string | null>(null)
  const [section, setSection] = useState<SectionId>('overview')
  const [confirmingClose, setConfirmingClose] = useState(false)

  // AI Diagnosis prefs are client-side (localStorage) and now SELF-SAVING: the
  // section has its own Save that commits the draft to DiagnoseContext, so it's
  // independent of the owner-gated footer. The draft is snapshotted on open.
  const diag = useDiagnose()
  const aiAvailable = diag.available && diag.agents.length > 0
  const [aiDraft, setAiDraft] = useState<AIDraft>({
    agent: diag.selectedAgent,
    isolated: diag.isolated,
    model: diag.model,
    effort: diag.effort,
  })
  const [aiSaved, setAiSaved] = useState(false)
  const aiDirty =
    aiDraft.agent !== diag.selectedAgent ||
    aiDraft.isolated !== diag.isolated ||
    aiDraft.model !== diag.model ||
    aiDraft.effort !== diag.effort

  // Per-bucket normalized dirty. Only startup fields participate; integration
  // fields apply live and never light up the footer.
  const edN = normalizeStartup(editedConfig)
  const svN = normalizeStartup(configData?.file ?? {})
  const clusterDirty =
    edN.kubeconfig !== svN.kubeconfig ||
    edN.kubeconfigDirs !== svN.kubeconfigDirs ||
    edN.namespace !== svN.namespace
  const serverDirty =
    edN.port !== svN.port || edN.noBrowser !== svN.noBrowser || edN.browser !== svN.browser
  const mcpDirty = edN.mcp !== svN.mcp
  const timelineDirty =
    edN.timelineStorage !== svN.timelineStorage ||
    edN.timelineDbPath !== svN.timelineDbPath ||
    edN.historyLimit !== svN.historyLimit
  // Merged-pane dirty for the flat nav (Connection = cluster+server, Advanced = mcp+timeline).
  const connectionDirty = clusterDirty || serverDirty
  const advancedDirty = mcpDirty || timelineDirty
  const startupDirty = configData != null && (connectionDirty || advancedDirty)

  // Load config on open + snapshot AI prefs + pick a default section that's
  // actually accessible to the current identity.
  useEffect(() => {
    if (!open) return
    setSaveMessage(null)
    setLoadError(null)
    setConfirmingClose(false)
    setAiSaved(false)
    setAiDraft({
      agent: diag.selectedAgent,
      isolated: diag.isolated,
      model: diag.model,
      effort: diag.effort,
    })
    // Overview is the landing section — a status-at-a-glance of what Radar is
    // connected to (cluster, integrations, MCP, AI), useful to owners and
    // viewers alike, rather than dropping owners on a config form or everyone
    // on a permissions dump.
    setSection('overview')

    fetch(apiUrl('/config'), { credentials: getCredentialsMode(), headers: getAuthHeaders() })
      .then((res) => {
        if (!res.ok) throw new Error(`HTTP ${res.status}`)
        return res.json()
      })
      .then((data: ConfigResponse) => {
        setConfigData(data)
        setEditedConfig(data.file)
      })
      .catch((err) => {
        console.warn('[settings] Failed to load config:', err)
        setLoadError('Failed to load configuration.')
      })
    // Snapshot-on-open only; we don't want late diag updates to wipe staged edits.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open])

  const updateConfigField = useCallback(<K extends keyof Config>(field: K, value: Config[K]) => {
    setEditedConfig((prev) => ({ ...prev, [field]: value }))
    setSaveMessage(null)
  }, [])

  const saveConfig = useCallback(async (): Promise<boolean> => {
    if (!configData) return false
    setSaving(true)
    setSaveMessage(null)
    try {
      // /config is FULL-REPLACEMENT. Integration fields must carry the
      // LAST-COMMITTED values (from configData.file), never an un-applied draft:
      // sending a typed-but-not-applied argoCdUrl trips the server-side origin
      // guard that clears the stored Argo token, and a stale value would revert a
      // live-applied integration. configData.file is kept in sync on Apply/Connect.
      const body: Config = {
        ...editedConfig,
        prometheusUrl: configData.file.prometheusUrl,
        argoCdUrl: configData.file.argoCdUrl,
        argoCdInsecureTls: configData.file.argoCdInsecureTls,
      }
      const res = await fetch(apiUrl('/config'), {
        method: 'PUT',
        credentials: getCredentialsMode(),
        headers: { 'Content-Type': 'application/json', ...getAuthHeaders() },
        body: JSON.stringify(body),
      })
      if (!res.ok) {
        const data = await res.json().catch(() => null)
        setSaveMessage(`Error: ${data?.error || res.statusText}`)
        return false
      }
      // Advance the committed snapshot so startupDirty settles to false.
      setConfigData((prev) => (prev ? { ...prev, file: body } : prev))
      setSaveMessage('Saved. Restart Radar to apply.')
      return true
    } catch (err) {
      setSaveMessage(`Error: ${err}`)
      return false
    } finally {
      setSaving(false)
    }
  }, [editedConfig, configData])

  // AI prefs are client-side (localStorage) — commit the staged draft now.
  // setSelectedAgent clears model/effort (they're agent-specific), so set the
  // agent first, then restore the draft's model/effort.
  const saveAi = useCallback(() => {
    diag.setSelectedAgent(aiDraft.agent)
    diag.setIsolated(aiDraft.isolated)
    diag.setModel(aiDraft.model)
    diag.setEffort(aiDraft.effort)
    setAiSaved(true)
  }, [diag, aiDraft])

  const discardChanges = useCallback(() => {
    // Revert unsaved startup edits back to the last-saved values (non-
    // destructive). configData.file holds the committed config, including the
    // live integration fields, so restoring it drops drafts without touching
    // what's saved.
    if (!configData) return
    setEditedConfig(configData.file)
    setSaveMessage(null)
  }, [configData])

  const handleSaveAndClose = useCallback(async () => {
    const ok = await saveConfig()
    if (ok) onClose()
  }, [saveConfig, onClose])

  // Close guard: a pending startup edit prompts an inline confirm rather than
  // silently discarding. An unsaved AI draft is re-derivable, so it's fine to
  // drop it on close. Held in a ref so the ESC listener reads current dirtiness.
  const requestCloseRef = useRef<() => void>(() => {})
  requestCloseRef.current = () => {
    if (canEditConfig && startupDirty) setConfirmingClose(true)
    else onClose()
  }

  // ESC key
  useEffect(() => {
    if (!open) return
    const handleKeyDown = (e: KeyboardEvent) => {
      if (e.key === 'Escape') {
        e.stopPropagation()
        requestCloseRef.current()
      }
    }
    document.addEventListener('keydown', handleKeyDown, true)
    return () => document.removeEventListener('keydown', handleKeyDown, true)
  }, [open])

  // Post-save feedback ("Saved. Restart Radar to apply.") is scoped to the
  // section it was saved from. Clear it on navigation so it doesn't linger — with
  // its disabled footer — on a live-apply section where "Restart to apply" is
  // wrong. It still stays put while the user remains on the saved section.
  useEffect(() => { setSaveMessage(null) }, [section])

  // Focus trap
  useEffect(() => {
    if (open && dialogRef.current) {
      dialogRef.current.focus()
    }
  }, [open])

  if (!shouldRender) return null

  const isDesktop = configData?.isDesktop ?? false
  const deploymentMode = capabilities.deployment?.mode ?? 'local'
  const showBrowserLaunchControls = !isDesktop && deploymentMode === 'local'

  // Flat, un-grouped nav ordered as a narrative — at-a-glance, then you, then how
  // Radar connects, then data integrations, then AI, then advanced. The
  // per-section captions carry the restart-vs-live semantics, so group labels
  // would only add visual weight. AI diagnose is always shown (the section
  // explains how to enable it when no agent CLI is installed).
  const navItems: NavItemDef[] = [
    { id: 'overview', label: 'Overview', icon: LayoutDashboard, ownerOnly: false, dirty: false },
    { id: 'perms', label: 'My permissions', icon: Shield, ownerOnly: false, dirty: false },
    { id: 'connection', label: 'Connection', icon: Boxes, ownerOnly: true, dirty: connectionDirty },
    { id: 'prometheus', label: 'Prometheus', icon: Activity, ownerOnly: true, dirty: false },
    { id: 'argocd', label: 'Argo CD', icon: GitBranch, ownerOnly: true, dirty: false },
    { id: 'ai', label: 'AI diagnose', icon: Sparkles, ownerOnly: false, dirty: aiDirty },
    { id: 'advanced', label: 'Advanced', icon: SlidersHorizontal, ownerOnly: true, dirty: advancedDirty },
  ]

  const showFooter = canEditConfig && (confirmingClose || startupDirty || !!saveMessage)

  return createPortal(
    <div className="fixed inset-0 z-50 flex items-center justify-center">
      {/* Backdrop */}
      <div
        className={clsx(
          'absolute inset-0 bg-black/60 backdrop-blur-sm',
          TRANSITION_BACKDROP,
          isOpen ? 'opacity-100' : 'opacity-0'
        )}
        onClick={() => requestCloseRef.current()}
      />

      {/* Dialog */}
      <div
        ref={dialogRef}
        tabIndex={-1}
        className={clsx(
          'relative bg-theme-surface border border-theme-border shadow-theme-lg w-full outline-none flex flex-col',
          'max-sm:inset-0 max-sm:absolute max-sm:rounded-none max-sm:max-h-full max-sm:border-0',
          // Fixed height so the dialog doesn't jump when switching tabs — short
          // tabs leave breathing room, tall ones scroll inside the content pane.
          // max-h keeps it on-screen on short viewports.
          'sm:rounded-xl sm:max-w-4xl sm:mx-4 sm:h-[620px] sm:max-h-[85vh]',
          TRANSITION_PANEL,
          isOpen ? 'opacity-100 scale-100' : 'opacity-0 scale-95'
        )}
      >
        {/* Header — spans both panes */}
        <div className="flex items-center justify-between p-4 border-b border-theme-border shrink-0">
          <div className="flex items-center gap-2">
            <Settings className="w-5 h-5 text-theme-text-secondary" />
            <div className="flex items-baseline gap-2">
              <h2 className="text-lg font-semibold text-theme-text-primary">Settings</h2>
              <span className="text-[11px] text-theme-text-tertiary">
                Radar{versionInfo?.currentVersion ? ` v${versionInfo.currentVersion}` : ''}
                <span className="text-theme-text-disabled"> · by Skyhook</span>
              </span>
            </div>
          </div>
          <button
            onClick={() => requestCloseRef.current()}
            className="p-1 text-theme-text-secondary hover:text-theme-text-primary hover:bg-theme-elevated rounded"
          >
            <X className="w-5 h-5" />
          </button>
        </div>

        {/* Body: sidebar + content */}
        <div className="flex flex-col sm:flex-row flex-1 min-h-0">
          {/* Sidebar (sm+) */}
          <nav
            role="tablist"
            aria-orientation="vertical"
            className="hidden sm:flex sm:flex-col gap-0.5 w-[200px] shrink-0 overflow-y-auto border-r border-theme-border p-3"
          >
            {navItems.map((i) => (
              <NavItem
                key={i.id}
                item={i}
                active={section === i.id}
                disabled={i.ownerOnly && !canEditConfig}
                onSelect={() => setSection(i.id)}
              />
            ))}
          </nav>

          {/* Tab strip (below sm) */}
          <div role="tablist" className="sm:hidden flex gap-1 overflow-x-auto border-b border-theme-border p-2 shrink-0">
            {navItems.map((i) => (
              <NavItem
                key={i.id}
                item={i}
                horizontal
                active={section === i.id}
                disabled={i.ownerOnly && !canEditConfig}
                onSelect={() => setSection(i.id)}
              />
            ))}
          </div>

          {/* Content pane — stable scrollbar gutter so switching between a
              scrolling tab and a short one doesn't shift content sideways. */}
          <div className="flex-1 min-w-0 overflow-y-auto p-4 sm:p-5 [scrollbar-gutter:stable]">
            {loadError && (
              <div className="mb-3 px-3 py-2 text-xs text-amber-700 dark:text-amber-300 bg-amber-500/10 border border-amber-500/20 rounded-md">
                {loadError}
              </div>
            )}

            {/* Overview — status at a glance; the landing section */}
            <div className={clsx(section !== 'overview' && 'hidden')} role="tabpanel">
              <div className="mb-1">
                <h3 className="text-base font-semibold text-theme-text-primary">Overview</h3>
                <p className="mt-0.5 text-xs text-theme-text-tertiary">
                  What this Radar is connected to right now — select a row to manage it.
                </p>
              </div>
              <div className="mt-3">
                <OverviewPanel active={section === 'overview'} onNavigate={setSection} />
              </div>
            </div>

            {/* My permissions — usable by everyone, rendered inline (no launcher) */}
            <div className={clsx(section !== 'perms' && 'hidden')} role="tabpanel">
              <div className="mb-1">
                <h3 className="text-base font-semibold text-theme-text-primary">My permissions</h3>
                <p className="mt-0.5 text-xs text-theme-text-tertiary">
                  What your current identity can do in this cluster — roles bound to you and your
                  effective, flattened permissions.
                </p>
              </div>
              <div className="mt-3">
                <MyPermissionsContent active={section === 'perms'} />
              </div>
            </div>

            {/* Connection — Cluster + Server merged */}
            <SectionPane
              id="connection"
              active={section}
              title="Connection"
              caption="Takes effect on next launch."
              locked={!canEditConfig}
            >
              <div className="space-y-4">
                <SubHeading>Cluster</SubHeading>
                <ClusterSection
                  config={editedConfig}
                  effectiveConfig={configData?.effective}
                  onChange={updateConfigField}
                />
              </div>
              <div className="space-y-4 border-t border-theme-border-subtle pt-4">
                <SubHeading>Server</SubHeading>
                <ServerSection
                  config={editedConfig}
                  effectiveConfig={configData?.effective}
                  isDesktop={isDesktop}
                  showBrowserLaunchControls={showBrowserLaunchControls}
                  onChange={updateConfigField}
                />
              </div>
            </SectionPane>

            {/* Prometheus — live */}
            <SectionPane
              id="prometheus"
              active={section}
              title="Prometheus"
              caption="Applies immediately — no restart."
              live
              locked={!canEditConfig}
            >
              <PrometheusConfigField
                value={editedConfig.prometheusUrl ?? ''}
                configuredHeaderKeys={configData?.prometheusHeaderKeys ?? []}
                onChange={(v) => updateConfigField('prometheusUrl', v || undefined)}
                onApplied={(url) =>
                  setConfigData((prev) =>
                    prev ? { ...prev, file: { ...prev.file, prometheusUrl: url || undefined } } : prev
                  )
                }
              />
            </SectionPane>

            {/* Argo CD — live */}
            <SectionPane
              id="argocd"
              active={section}
              title="Argo CD"
              caption="Applies immediately — no restart."
              live
              locked={!canEditConfig}
            >
              <ArgoCDConfigField
                url={editedConfig.argoCdUrl ?? ''}
                insecureTls={editedConfig.argoCdInsecureTls ?? false}
                tokenSet={configData?.argoCdTokenSet ?? false}
                envManaged={configData?.argoCdEnvManaged ?? false}
                envError={configData?.argoCdEnvError}
                cliSession={configData?.argoCdCliSession}
                onChangeUrl={(v) => updateConfigField('argoCdUrl', v || undefined)}
                onChangeInsecureTls={(v) => updateConfigField('argoCdInsecureTls', v || undefined)}
                onApplied={({ url, insecureTls, tokenSet }) =>
                  setConfigData((prev) =>
                    prev
                      ? {
                          ...prev,
                          file: { ...prev.file, argoCdUrl: url || undefined, argoCdInsecureTls: insecureTls || undefined },
                          argoCdTokenSet: tokenSet,
                        }
                      : prev
                  )
                }
              />
            </SectionPane>

            {/* AI diagnose — self-saving, usable by everyone. Same heading block
                as every other tab; the body is the agent controls (when a CLI is
                installed) or an enable explainer (when not). */}
            <div className={clsx(section !== 'ai' && 'hidden')} role="tabpanel">
              <div className="mb-4">
                <h3 className="text-base font-semibold text-theme-text-primary">AI diagnose</h3>
                <p className="mt-0.5 text-xs text-theme-text-tertiary">
                  Investigate incidents with an AI agent that runs on your own machine — reading
                  logs, events, and topology to explain what's wrong. No Radar cloud, no API key.
                </p>
              </div>
              {aiAvailable ? (
                <div className="space-y-4">
                  <AISettingsSection
                    available={diag.available}
                    agents={diag.agents}
                    draft={aiDraft}
                    onChange={(patch) => {
                      setAiDraft((d) => ({ ...d, ...patch }))
                      setAiSaved(false)
                    }}
                    onHistoryCleared={diag.refreshRuns}
                  />
                  <div className="flex items-center justify-end gap-3">
                    {aiSaved && !aiDirty && (
                      <span className="flex items-center gap-1 text-xs text-green-600 dark:text-green-400/80">
                        <Check className="w-3 h-3" />
                        Saved
                      </span>
                    )}
                    <button
                      onClick={saveAi}
                      disabled={!aiDirty}
                      className="px-4 py-1.5 text-sm font-medium btn-brand rounded-md disabled:opacity-50 disabled:pointer-events-none"
                    >
                      Save
                    </button>
                  </div>
                </div>
              ) : (
                <AIUnavailableNotice />
              )}
            </div>

            {/* Advanced — MCP + Timeline merged */}
            <SectionPane
              id="advanced"
              active={section}
              title="Advanced"
              caption="Takes effect on next launch."
              locked={!canEditConfig}
            >
              <div className="space-y-4">
                <SubHeading>MCP</SubHeading>
                <MCPSection
                  mcpEnabled={editedConfig.mcp ?? true}
                  onToggle={(v) => updateConfigField('mcp', v)}
                  isDesktop={isDesktop}
                  portPinned={editedConfig.port != null && editedConfig.port > 0}
                  onPinPort={(port) => updateConfigField('port', port)}
                />
              </div>
              <div className="space-y-4 border-t border-theme-border-subtle pt-4">
                <SubHeading>Timeline</SubHeading>
                <TimelineSection
                  config={editedConfig}
                  effectiveConfig={configData?.effective}
                  onChange={updateConfigField}
                />
              </div>
            </SectionPane>
          </div>
        </div>

        {/* Footer — owner-gated. Startup config only: AI self-saves, integrations
            apply live. Shown whenever a startup edit is pending (any section),
            while confirming a close, or briefly after a save. */}
        <div
          className={clsx(
            'shrink-0 overflow-hidden transition-all duration-200 ease-out',
            showFooter ? 'max-h-24 opacity-100 border-t border-theme-border' : 'max-h-0 opacity-0 pointer-events-none'
          )}
        >
          <div className="flex items-center justify-between gap-3 px-4 py-2.5">
            {confirmingClose ? (
              <>
                <span className="text-xs text-theme-text-secondary">Unsaved changes.</span>
                <div className="flex items-center gap-2">
                  <button
                    onClick={() => setConfirmingClose(false)}
                    disabled={saving}
                    className="px-3 py-1.5 text-xs font-medium text-theme-text-secondary hover:text-theme-text-primary hover:bg-theme-elevated rounded-md transition-colors disabled:opacity-50"
                  >
                    Keep editing
                  </button>
                  <button
                    onClick={onClose}
                    disabled={saving}
                    className="px-3 py-1.5 text-xs font-medium text-theme-text-secondary hover:text-theme-text-primary hover:bg-theme-elevated rounded-md transition-colors disabled:opacity-50"
                  >
                    Discard
                  </button>
                  <button
                    onClick={handleSaveAndClose}
                    disabled={saving}
                    className="flex items-center gap-1.5 px-4 py-1.5 text-sm font-medium btn-brand rounded-md"
                  >
                    {saving && <Loader2 className="w-3.5 h-3.5 animate-spin" />}
                    Save
                  </button>
                </div>
              </>
            ) : (
              <>
                <div className="flex items-center gap-2">
                  <Tooltip content="Discard unsaved changes and revert to the last saved values">
                    <button
                      onClick={discardChanges}
                      disabled={saving || !startupDirty}
                      className="flex items-center gap-1.5 px-3 py-1.5 text-xs font-medium text-theme-text-secondary hover:text-theme-text-primary hover:bg-theme-elevated rounded-md transition-colors disabled:opacity-50 disabled:pointer-events-none"
                    >
                      <RotateCcw className="w-3.5 h-3.5" />
                      Discard changes
                    </button>
                  </Tooltip>
                  {saveMessage && (
                    <span className={clsx('text-xs', saveMessage.startsWith('Error') ? 'text-red-400' : 'text-green-400')}>
                      {saveMessage}
                    </span>
                  )}
                </div>
                <button
                  onClick={saveConfig}
                  disabled={saving || !startupDirty}
                  className="flex items-center gap-1.5 px-4 py-1.5 text-sm font-medium btn-brand rounded-md"
                >
                  {saving && <Loader2 className="w-3.5 h-3.5 animate-spin" />}
                  Save
                </button>
              </>
            )}
          </div>
        </div>
      </div>
    </div>,
    document.body
  )
}

// -- Sidebar primitives -------------------------------------------------------

interface NavItemDef {
  id: SectionId
  label: string
  icon: LucideIcon
  ownerOnly: boolean
  dirty: boolean
}

// Light subheading separating the two field groups inside a merged pane
// (Cluster/Server, MCP/Timeline).
function SubHeading({ children }: { children: ReactNode }) {
  return (
    <h4 className="text-xs font-semibold uppercase tracking-wider text-theme-text-tertiary">
      {children}
    </h4>
  )
}

function NavItem({
  item,
  active,
  disabled,
  horizontal,
  onSelect,
}: {
  item: NavItemDef
  active: boolean
  disabled: boolean
  horizontal?: boolean
  onSelect: () => void
}) {
  const Icon = item.icon
  const button = (
    <button
      type="button"
      role="tab"
      aria-selected={active}
      onClick={onSelect}
      disabled={disabled}
      className={clsx(
        'group flex items-center gap-2 rounded-md px-2.5 py-1.5 text-sm transition-colors',
        horizontal ? 'shrink-0' : 'w-full text-left',
        active
          ? 'bg-theme-active text-theme-text-primary font-medium'
          : 'text-theme-text-secondary hover:bg-theme-hover hover:text-theme-text-primary',
        disabled && 'opacity-50 cursor-not-allowed hover:bg-transparent hover:text-theme-text-secondary'
      )}
    >
      <Icon className="w-4 h-4 shrink-0" />
      <span className={clsx('truncate', !horizontal && 'flex-1')}>{item.label}</span>
      {disabled ? (
        <Lock className="w-3 h-3 shrink-0 text-theme-text-tertiary" />
      ) : item.dirty ? (
        <Tooltip content="Unsaved changes" delay={200} wrapperClassName="flex shrink-0">
          <span className="w-1.5 h-1.5 rounded-full bg-accent" />
        </Tooltip>
      ) : null}
    </button>
  )
  return disabled ? (
    <Tooltip content="Owner access required" delay={200} wrapperClassName={horizontal ? 'shrink-0' : 'w-full'}>
      {button}
    </Tooltip>
  ) : (
    button
  )
}

// -- Section shell ------------------------------------------------------------

// A section stays MOUNTED (visibility toggled) so local draft state in the live
// integration cards survives switching sidebar items. The caption states the
// section's honest apply semantics.
function SectionPane({
  id,
  active,
  title,
  caption,
  live,
  locked,
  children,
}: {
  id: SectionId
  active: SectionId
  title: string
  caption?: string
  live?: boolean
  locked?: boolean
  children: ReactNode
}) {
  return (
    <div className={clsx(active !== id && 'hidden')} role="tabpanel">
      <div className="mb-4">
        <h3 className="text-base font-semibold text-theme-text-primary">{title}</h3>
        {!locked && caption && <SectionCaption live={live}>{caption}</SectionCaption>}
      </div>
      {locked ? <LockWall /> : <div className="space-y-4">{children}</div>}
    </div>
  )
}

function SectionCaption({ children, live }: { children: ReactNode; live?: boolean }) {
  return (
    <p
      className={clsx(
        'mt-1 flex items-center gap-1.5 text-xs',
        live ? 'text-theme-text-secondary' : 'text-theme-text-tertiary'
      )}
    >
      {live ? <Zap className="w-3 h-3 shrink-0" /> : <RotateCw className="w-3 h-3 shrink-0" />}
      {children}
    </p>
  )
}

function LockWall() {
  return (
    <div className="rounded-md border border-theme-border bg-theme-elevated/50 p-4 flex items-start gap-3">
      <Lock className="w-4 h-4 mt-0.5 shrink-0 text-theme-text-tertiary" />
      <div className="min-w-0">
        <p className="text-sm font-medium text-theme-text-primary">Owner access required</p>
        <p className="mt-0.5 text-xs text-theme-text-tertiary">
          These settings (kubeconfig, server port, timeline, integrations) affect
          every user of this Radar instance, so they're limited to owners. Ask an
          owner if you need a change here.
        </p>
      </div>
    </div>
  )
}

// -- Overview (landing) -------------------------------------------------------

type OverviewTone = 'ok' | 'warn' | 'off' | 'unknown'

interface OverviewRow {
  id: SectionId
  icon: LucideIcon
  label: string
  tone: OverviewTone
  value: string
  detail?: string
  copyable?: boolean
}

// OverviewPanel is the Settings landing: a status-at-a-glance of what Radar is
// connected to right now. Each row navigates to the section that manages it.
// The Argo status query is gated on `active` (it triggers a background reconnect
// probe, so we don't want it firing when Settings opens on another section);
// cluster and Prometheus status are shared app-wide caches, so they're read
// unconditionally.
function OverviewPanel({ active, onNavigate }: { active: boolean; onNavigate: (s: SectionId) => void }) {
  const { data: cluster } = useClusterInfo()
  const { data: prom } = usePrometheusStatus()
  const { data: argo } = useArgoStatus(active)
  const { data: version } = useVersionCheck()
  const capabilities = useCapabilitiesContext()
  const diag = useDiagnose()
  const [copied, setCopied] = useState(false)

  const aiAvailable = diag.available && diag.agents.length > 0
  const agentLabel =
    diag.agents.find((a) => a.name === diag.selectedAgent)?.label ?? diag.agents[0]?.label
  const mcpOn = capabilities.mcpEnabled
  const port = Number(window.location.port) || 80
  const mcpUrl = `http://localhost:${port}/mcp`

  const rows: OverviewRow[] = [
    {
      id: 'connection', icon: Boxes, label: 'Cluster',
      tone: cluster ? 'ok' : 'unknown',
      value: cluster?.context ?? 'Connecting…',
      detail: cluster ? `Kubernetes ${cluster.kubernetesVersion} · ${cluster.nodeCount} nodes` : undefined,
    },
    {
      id: 'prometheus', icon: Activity, label: 'Prometheus',
      tone: prom?.connected ? 'ok' : prom?.available ? 'warn' : 'off',
      value: prom?.connected ? 'Connected' : prom?.available ? 'Not reachable' : 'Not configured',
      detail: prom?.connected ? prom.address : undefined,
    },
    {
      id: 'argocd', icon: GitBranch, label: 'Argo CD',
      tone: argo?.connected ? 'ok' : argo?.configured ? 'warn' : 'off',
      // Configured-but-not-connected is often a permanently rejected/expired
      // token, not a transient reconnect — "Not reachable" matches Prometheus and
      // doesn't imply it will recover on its own.
      value: argo?.connected ? 'Connected' : argo?.configured ? 'Not reachable' : 'Not connected',
      detail: argo?.connected ? argo.address : undefined,
    },
    {
      id: 'advanced', icon: Zap, label: 'MCP',
      tone: mcpOn ? 'ok' : 'off',
      value: mcpOn ? 'On' : 'Off',
      detail: mcpOn ? mcpUrl : undefined,
      copyable: mcpOn,
    },
    {
      id: 'ai', icon: Sparkles, label: 'AI diagnose',
      tone: aiAvailable ? 'ok' : 'off',
      value: aiAvailable ? 'Ready' : 'No agent CLI',
      detail: aiAvailable ? agentLabel : undefined,
    },
  ]

  const copyMcp = () => {
    navigator.clipboard.writeText(mcpUrl)
    setCopied(true)
    setTimeout(() => setCopied(false), 2000)
  }

  return (
    <div className="space-y-4">
      {version?.updateAvailable && (
        <a
          href={version.releaseUrl}
          target="_blank"
          rel="noreferrer"
          className="flex items-center gap-2 px-3 py-2 text-xs rounded-md border border-skyhook-500/30 bg-skyhook-500/10 hover:bg-skyhook-500/15 transition-colors"
        >
          <Download className="w-3.5 h-3.5 shrink-0 text-skyhook-500" />
          <span className="flex-1 text-theme-text-primary">
            Radar {version.latestVersion} is available
            <span className="text-theme-text-tertiary"> — you're on {version.currentVersion}</span>
          </span>
          <ExternalLink className="w-3 h-3 shrink-0 text-theme-text-tertiary" />
        </a>
      )}

      <div className="rounded-md border border-theme-border divide-y divide-theme-border-subtle overflow-hidden">
        {rows.map((row) => {
          const Icon = row.icon
          return (
            <div
              key={row.label}
              role="button"
              tabIndex={0}
              onClick={() => onNavigate(row.id)}
              onKeyDown={(e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); onNavigate(row.id) } }}
              className="group flex items-center gap-3 px-3 py-2.5 cursor-pointer hover:bg-theme-hover transition-colors"
            >
              <Icon className="w-4 h-4 shrink-0 text-theme-text-tertiary" />
              <span className="text-sm text-theme-text-primary w-24 shrink-0 truncate">{row.label}</span>
              <OverviewStatus tone={row.tone} />
              <div className="flex-1 min-w-0 flex items-baseline gap-1.5">
                <span className="text-sm text-theme-text-secondary shrink-0">{row.value}</span>
                {row.detail && (
                  <span className="text-xs text-theme-text-tertiary truncate">{row.detail}</span>
                )}
              </div>
              {row.copyable && (
                <Tooltip content="Copy MCP URL" wrapperClassName="shrink-0">
                  <button
                    onClick={(e) => { e.stopPropagation(); copyMcp() }}
                    className="p-1 text-theme-text-tertiary hover:text-theme-text-primary hover:bg-theme-elevated rounded transition-colors"
                  >
                    {copied ? <Check className="w-3.5 h-3.5 text-green-500" /> : <Copy className="w-3.5 h-3.5" />}
                  </button>
                </Tooltip>
              )}
              <ChevronRight className="w-4 h-4 shrink-0 text-theme-text-disabled group-hover:text-theme-text-tertiary" />
            </div>
          )
        })}
      </div>
    </div>
  )
}

function OverviewStatus({ tone }: { tone: OverviewTone }) {
  const cls =
    tone === 'ok' ? 'bg-green-500'
      : tone === 'warn' ? 'bg-amber-500'
        : tone === 'unknown' ? 'bg-theme-text-tertiary animate-pulse'
          : 'bg-theme-text-disabled'
  return <span className={clsx('w-2 h-2 rounded-full shrink-0', cls)} />
}

// AIUnavailableNotice is the body of the AI diagnose tab when no supported agent
// CLI is installed — the heading/description are provided by the tab itself, so
// this is just the enable explainer (keeping the feature discoverable to whoever
// would set it up).
function AIUnavailableNotice() {
  return (
    <div className="rounded-md border border-theme-border bg-theme-elevated/50 p-3">
      <p className="text-sm font-medium text-theme-text-primary">No supported agent CLI found</p>
      <p className="mt-1 text-xs text-theme-text-tertiary">
        Install <span className="text-theme-text-secondary">Claude Code</span> or{' '}
        <span className="text-theme-text-secondary">Codex</span>, then restart Radar — this tab
        will show the agent, model, and effort controls.
      </p>
    </div>
  )
}

// -- Startup section bodies ---------------------------------------------------

function ClusterSection({
  config,
  effectiveConfig,
  onChange,
}: {
  config: Config
  effectiveConfig?: Config
  onChange: <K extends keyof Config>(field: K, value: Config[K]) => void
}) {
  return (
    <>
      <ConfigField
        label="Kubeconfig"
        help="Path to kubeconfig file"
        value={config.kubeconfig ?? ''}
        effectiveValue={effectiveConfig?.kubeconfig}
        placeholder="~/.kube/config"
        onChange={(v) => onChange('kubeconfig', v || undefined)}
      />
      <ConfigArrayField
        label="Kubeconfig Directories"
        help="Comma-separated directories containing kubeconfig files"
        value={config.kubeconfigDirs}
        effectiveValue={effectiveConfig?.kubeconfigDirs}
        placeholder="/path/to/dir1, /path/to/dir2"
        onChange={(v) => onChange('kubeconfigDirs', v)}
      />
      <ConfigField
        label="Default Namespace"
        help="Startup default only — change the active namespace live anytime from the header switcher"
        value={config.namespace ?? ''}
        effectiveValue={effectiveConfig?.namespace}
        placeholder="All namespaces"
        onChange={(v) => onChange('namespace', v || undefined)}
      />
    </>
  )
}

function ServerSection({
  config,
  effectiveConfig,
  isDesktop,
  showBrowserLaunchControls,
  onChange,
}: {
  config: Config
  effectiveConfig?: Config
  isDesktop: boolean
  showBrowserLaunchControls: boolean
  onChange: <K extends keyof Config>(field: K, value: Config[K]) => void
}) {
  return (
    <>
      <ConfigNumberField
        label="Port"
        help={isDesktop
          ? 'Fixed server port (leave empty for random). Set this to keep a stable MCP endpoint.'
          : 'Server port'}
        value={config.port}
        effectiveValue={effectiveConfig?.port}
        placeholder={isDesktop ? 'Random' : '9280'}
        onChange={(v) => onChange('port', v)}
      />

      {showBrowserLaunchControls && (
        <>
          <ConfigToggle
            label="Open browser on start"
            description="Automatically open the Radar UI in your browser when Radar starts — turn off to run headless and open the URL yourself."
            value={!(config.noBrowser ?? false)}
            onChange={(v) => onChange('noBrowser', !v ? true : undefined)}
          />
          <ConfigField
            label="Browser"
            help="Browser to open the UI in — a macOS app name (Google Chrome, Safari) or a Linux/Windows command (google-chrome). Leave blank for your system default."
            value={config.browser ?? ''}
            effectiveValue={effectiveConfig?.browser}
            placeholder="System default"
            onChange={(v) => onChange('browser', v || undefined)}
          />
        </>
      )}
    </>
  )
}

function TimelineSection({
  config,
  effectiveConfig,
  onChange,
}: {
  config: Config
  effectiveConfig?: Config
  onChange: <K extends keyof Config>(field: K, value: Config[K]) => void
}) {
  return (
    <>
      <div>
        <label className="block text-sm font-medium text-theme-text-primary mb-1">
          Storage Backend
        </label>
        <select
          value={config.timelineStorage ?? 'memory'}
          onChange={(e) => onChange('timelineStorage', e.target.value === 'memory' ? undefined : e.target.value as 'sqlite')}
          className="w-full px-3 py-1.5 text-sm bg-theme-elevated border border-theme-border rounded-md text-theme-text-primary focus:outline-none focus:border-skyhook-500"
        >
          <option value="memory">Memory (default)</option>
          <option value="sqlite">SQLite (persistent)</option>
        </select>
        <EffectiveHint current={config.timelineStorage} effective={effectiveConfig?.timelineStorage} />
      </div>

      <ConfigNumberField
        label="History Limit"
        help="Maximum events to retain"
        value={config.historyLimit}
        effectiveValue={effectiveConfig?.historyLimit}
        placeholder="10000"
        onChange={(v) => onChange('historyLimit', v)}
      />
    </>
  )
}

// -- MCP Section --------------------------------------------------------------

function MCPSection({
  mcpEnabled,
  onToggle,
  isDesktop,
  portPinned,
  onPinPort,
}: {
  mcpEnabled: boolean
  onToggle: (value: boolean) => void
  isDesktop: boolean
  portPinned: boolean
  onPinPort: (port: number) => void
}) {
  const [copied, setCopied] = useState(false)

  const currentPort = Number(window.location.port) || 80
  const mcpUrl = `http://localhost:${currentPort}/mcp`

  const handleCopy = () => {
    navigator.clipboard.writeText(mcpUrl)
    setCopied(true)
    setTimeout(() => setCopied(false), 2000)
  }

  const handlePinPort = () => {
    onPinPort(currentPort)
  }

  return (
    <div className="space-y-3">
      <p className="text-xs text-theme-text-tertiary">
        Lets AI tools (Claude Code, Cursor, …) query and act on this cluster through Radar over the
        Model Context Protocol. Point your agent at the endpoint below.
      </p>
      <ConfigToggle
        label="MCP Server"
        value={mcpEnabled}
        onChange={onToggle}
      />

      {mcpEnabled && (
        <div className="space-y-2 pl-0.5">
          <div>
            <label className="block text-xs text-theme-text-secondary mb-1">MCP Endpoint</label>
            <div className="flex items-center gap-2">
              <code className="flex-1 px-2.5 py-1.5 text-xs font-mono bg-theme-elevated border border-theme-border rounded-md text-theme-text-primary truncate">
                {mcpUrl}
              </code>
              <Tooltip content="Copy MCP URL" wrapperClassName="shrink-0">
              <button
                onClick={handleCopy}
                className="p-1.5 text-theme-text-tertiary hover:text-theme-text-primary hover:bg-theme-elevated rounded-md transition-colors"
              >
                {copied ? <Check className="w-3.5 h-3.5 text-green-500" /> : <Copy className="w-3.5 h-3.5" />}
              </button>
              </Tooltip>
            </div>
          </div>

          {isDesktop && !portPinned && (
            <div className="flex items-start gap-2 px-2.5 py-2 text-xs bg-amber-500/10 border border-amber-500/20 rounded-md">
              <span className="text-amber-700 dark:text-amber-300 flex-1">
                Port changes on every restart. Pin it to keep a stable MCP endpoint.
              </span>
              <button
                onClick={handlePinPort}
                className="shrink-0 flex items-center gap-1 px-2 py-0.5 text-xs font-medium text-amber-800 dark:text-amber-200 hover:text-amber-900 dark:hover:text-white bg-amber-500/20 hover:bg-amber-500/30 rounded transition-colors"
              >
                <Pin className="w-3 h-3" />
                Pin port {currentPort}
              </button>
            </div>
          )}

          {isDesktop && portPinned && (
            <p className="text-xs text-green-600 dark:text-green-400/80 px-0.5">
              Port is pinned. MCP endpoint will remain stable across restarts.
            </p>
          )}
        </div>
      )}
    </div>
  )
}

// -- Prometheus (live-appliable) ----------------------------------------------

// The Prometheus URL can be re-pointed without a restart — the metrics path reads
// it from a mutable global. "Apply now" hits PUT /integrations/prometheus, which
// persists the URL AND re-points the running client, then probes it so we can
// confirm reachability inline. onApplied notifies the parent so the footer's
// last-committed snapshot stays in sync (see saveConfig). No EffectiveHint here —
// a restart-diff hint would contradict the whole point of applying live.
type ApplyState =
  | { status: 'idle' }
  | { status: 'applying' }
  | { status: 'connected'; address: string }
  | { status: 'unreachable'; error: string } // persisted, but the probe failed
  | { status: 'failed'; error: string }       // request itself failed — nothing saved

type HeaderRow = { key: string; value: string }

function PrometheusConfigField({
  value,
  onChange,
  configuredHeaderKeys,
  onApplied,
}: {
  value: string
  onChange: (value: string) => void
  configuredHeaderKeys: string[]
  onApplied?: (url: string) => void
}) {
  const [apply, setApply] = useState<ApplyState>({ status: 'idle' })
  // null = not editing headers (preserve what's stored). A non-null array means
  // the user opened the editor; on Apply we send it verbatim, replacing all
  // stored headers (values are write-only, so the server never sends them back).
  const [headerRows, setHeaderRows] = useState<HeaderRow[] | null>(null)
  // Show the server's configured header keys, but let a successful apply override
  // optimistically (config isn't refetched). Derived from the prop — not a
  // mount-time snapshot — so it stays correct as config loads asynchronously.
  const [appliedKeys, setAppliedKeys] = useState<string[] | null>(null)
  const storedKeys = appliedKeys ?? configuredHeaderKeys

  const clearStatus = () => {
    if (apply.status !== 'applying') setApply({ status: 'idle' })
  }

  // Footer Reset (and any external edit) clears the URL field without a keystroke;
  // drop a stale "Connected"/"Saved" status so it doesn't describe an emptied field.
  useEffect(() => {
    setApply((s) => (s.status === 'idle' || s.status === 'applying' ? s : { status: 'idle' }))
  }, [value])

  const handleApply = async () => {
    setApply({ status: 'applying' })
    // Decide what to do with headers. undefined = leave them untouched. Only send
    // a replacement when the editor has real content, or {} when the user emptied
    // every row (explicit clear) — blank in-progress rows must NOT wipe stored
    // secrets just because the editor happens to be open for a URL-only change.
    let editedHeaders: Record<string, string> | undefined
    if (headerRows !== null) {
      const entered = Object.fromEntries(
        headerRows
          .map((r) => [r.key.trim(), r.value] as const)
          .filter(([k, v]) => k !== '' && v !== '')
      )
      if (Object.keys(entered).length > 0) editedHeaders = entered
      else if (headerRows.length === 0) editedHeaders = {}
    }
    try {
      const res = await fetch(apiUrl('/integrations/prometheus'), {
        method: 'PUT',
        credentials: getCredentialsMode(),
        headers: { 'Content-Type': 'application/json', ...getAuthHeaders() },
        body: JSON.stringify({
          prometheusUrl: value.trim(),
          ...(editedHeaders !== undefined ? { headers: editedHeaders } : {}),
        }),
      })
      const data = await res.json().catch(() => null)
      if (!res.ok) {
        setApply({ status: 'failed', error: data?.error || res.statusText })
        return
      }
      onApplied?.(value.trim())
      if (editedHeaders !== undefined) {
        setAppliedKeys(Object.keys(editedHeaders).sort())
      }
      if (headerRows !== null) {
        setHeaderRows(null)
      }
      if (data?.connected) {
        setApply({ status: 'connected', address: data.address || value.trim() })
      } else {
        setApply({ status: 'unreachable', error: data?.error || 'not reachable' })
      }
    } catch (err) {
      setApply({ status: 'failed', error: String(err) })
    }
  }

  return (
    <div>
      <p className="text-xs text-theme-text-tertiary mb-3">
        Powers the CPU / memory graphs, usage history, and rightsizing hints on workload and node pages.
      </p>
      <label className="block text-sm font-medium text-theme-text-primary mb-1">
        Server URL
      </label>
      <p className="text-xs text-theme-text-tertiary mb-1">
        Manual Prometheus / VictoriaMetrics URL — set this to skip auto-discovery.
      </p>
      <div className="flex items-center gap-2">
        <Input
          value={value}
          onChange={(e) => onChange(e.target.value)}
          placeholder="http://prometheus-server.monitoring:9090"
          className="flex-1 min-w-0 px-3 py-1.5 text-sm bg-theme-elevated border border-theme-border rounded-md text-theme-text-primary placeholder:text-theme-text-tertiary focus:outline-none focus:border-skyhook-500"
        />
        <Tooltip content="Apply this URL to the running server now — no restart" wrapperClassName="shrink-0">
          <button
            onClick={handleApply}
            disabled={apply.status === 'applying'}
            className="flex items-center gap-1.5 px-3 py-1.5 text-xs font-medium btn-brand rounded-md disabled:opacity-50"
          >
            {apply.status === 'applying'
              ? <Loader2 className="w-3.5 h-3.5 animate-spin" />
              : <Plug className="w-3.5 h-3.5" />}
            Apply now
          </button>
        </Tooltip>
      </div>
      {apply.status === 'connected' ? (
        <p className="mt-1 flex items-center gap-1 text-xs text-green-600 dark:text-green-400/80">
          <Check className="w-3 h-3 shrink-0" />
          Connected to {apply.address} — applied, no restart needed
        </p>
      ) : apply.status === 'unreachable' ? (
        <p className="mt-1 text-xs text-amber-600 dark:text-amber-400/80">
          Saved, but not reachable: {apply.error}
        </p>
      ) : apply.status === 'failed' ? (
        <p className="mt-1 text-xs text-red-600 dark:text-red-400/80">
          Couldn't apply: {apply.error}
        </p>
      ) : (
        <p className="mt-1 text-xs text-theme-text-tertiary">
          Applies immediately — no restart needed.
        </p>
      )}

      {/* Auth headers — for token / multi-tenant backends (Bearer, X-Scope-OrgID). */}
      <div className="mt-3">
        {headerRows === null ? (
          <div className="flex items-center justify-between gap-2">
            <span className="text-xs text-theme-text-tertiary">
              {storedKeys.length > 0
                ? <>Auth headers: <span className="text-theme-text-secondary">{storedKeys.join(', ')}</span> <span className="text-theme-text-disabled">(values hidden)</span></>
                : 'No auth headers'}
            </span>
            <button
              onClick={() => { setHeaderRows([{ key: '', value: '' }]); clearStatus() }}
              className="shrink-0 text-xs font-medium text-accent-text hover:underline"
            >
              {storedKeys.length > 0 ? 'Edit headers' : 'Add auth headers'}
            </button>
          </div>
        ) : (
          <div className="rounded-md border border-theme-border bg-theme-elevated/40 p-2.5 space-y-2">
            {headerRows.map((row, i) => (
              <div key={i} className="flex items-center gap-2">
                <Input
                  value={row.key}
                  onChange={(e) => {
                    setHeaderRows((rows) => rows!.map((r, j) => j === i ? { ...r, key: e.target.value } : r))
                    clearStatus()
                  }}
                  placeholder="Header (e.g. Authorization)"
                  className="flex-1 min-w-0 px-2.5 py-1.5 text-xs bg-theme-base border border-theme-border rounded-md text-theme-text-primary placeholder:text-theme-text-tertiary focus:outline-none focus:border-skyhook-500"
                />
                <input
                  type="password"
                  value={row.value}
                  onChange={(e) => {
                    setHeaderRows((rows) => rows!.map((r, j) => j === i ? { ...r, value: e.target.value } : r))
                    clearStatus()
                  }}
                  placeholder="Value (e.g. Bearer …)"
                  className="flex-1 min-w-0 px-2.5 py-1.5 text-xs bg-theme-base border border-theme-border rounded-md text-theme-text-primary placeholder:text-theme-text-tertiary focus:outline-none focus:border-skyhook-500"
                />
                <Tooltip content="Remove header" wrapperClassName="shrink-0">
                  <button
                    onClick={() => setHeaderRows((rows) => rows!.filter((_, j) => j !== i))}
                    className="p-1 text-theme-text-tertiary hover:text-theme-text-primary hover:bg-theme-hover rounded"
                  >
                    <X className="w-3.5 h-3.5" />
                  </button>
                </Tooltip>
              </div>
            ))}
            <div className="flex items-center justify-between gap-2">
              <button
                onClick={() => setHeaderRows((rows) => [...rows!, { key: '', value: '' }])}
                className="flex items-center gap-1 text-xs font-medium text-accent-text hover:underline"
              >
                <Plus className="w-3 h-3" /> Add header
              </button>
              <button
                onClick={() => { setHeaderRows(null); clearStatus() }}
                className="text-xs text-theme-text-tertiary hover:text-theme-text-primary"
              >
                Cancel
              </button>
            </div>
            <p className="text-xs text-theme-text-tertiary">
              Saved when you click Apply now. Entered headers replace all stored
              ones — values are hidden, so re-enter any you want to keep. Leave
              blank to keep existing headers unchanged.
            </p>
          </div>
        )}
      </div>
    </div>
  )
}

// -- Argo CD (live-appliable) -------------------------------------------------

// The Argo CD integration powers the full Git-rendered desired-vs-live diff on
// GitOps Application pages. Like Prometheus, it applies to the running server
// without a restart: both actions PUT /integrations/argocd, which persists the
// settings AND re-points the running client, then verifies reachability (the
// server returns 400 distinguishing unreachable from an invalid token). onApplied
// notifies the parent so the footer's last-committed snapshot stays in sync. The
// token is write-only — never returned, and omitted from the PUT unless the user
// types a new value or clicks Clear.
type ArgoState =
  | { status: 'idle' }
  | { status: 'connecting' }
  | { status: 'connected' }
  | { status: 'error'; error: string }

function ArgoCDConfigField({
  url,
  insecureTls,
  tokenSet,
  envManaged,
  envError,
  cliSession,
  onChangeUrl,
  onChangeInsecureTls,
  onApplied,
}: {
  url: string
  insecureTls: boolean
  tokenSet: boolean
  envManaged?: boolean
  envError?: string
  cliSession?: { server: string; user: string; insecure?: boolean }
  onChangeUrl: (value: string) => void
  onChangeInsecureTls: (value: boolean) => void
  onApplied?: (v: { url: string; insecureTls: boolean; tokenSet: boolean }) => void
}) {
  if (envManaged) {
    return <ArgoCDEnvManagedField url={url} insecureTls={insecureTls} envError={envError} />
  }
  return (
    <ArgoCDEditableField
      url={url}
      insecureTls={insecureTls}
      tokenSet={tokenSet}
      cliSession={cliSession}
      onChangeUrl={onChangeUrl}
      onChangeInsecureTls={onChangeInsecureTls}
      onApplied={onApplied}
    />
  )
}

// Read-only card for the environment-managed integration: the token/URL/TLS come
// from the deployment (RADAR_ARGOCD_TOKEN / _TOKEN_FILE / _URL), so editing here
// is disabled — the server refuses the PUT. Shows the effective endpoint and points
// the operator at the deployment for changes.
function ArgoCDEnvManagedField({
  url,
  insecureTls,
  envError,
}: {
  url: string
  insecureTls: boolean
  envError?: string
}) {
  if (envError) {
    return (
      <div>
        <p className="text-xs text-theme-text-tertiary mb-3">
          Powers the full Git-rendered desired-vs-live diff on GitOps Application pages — what Git
          declares vs what&apos;s actually running.
        </p>
        <div className="rounded-md border border-red-500/30 bg-red-500/[0.07] p-3">
          <p className="flex items-center gap-1.5 text-sm font-medium text-red-600 dark:text-red-400/90">
            <AlertTriangle className="w-3.5 h-3.5 shrink-0" />
            Environment Argo CD configuration is invalid
          </p>
          <p className="mt-1 text-xs text-theme-text-secondary break-words">{envError}</p>
          <p className="mt-2 text-xs text-theme-text-tertiary">
            The deployment sets <code className="inline-code">RADAR_ARGOCD_TOKEN</code> (or{' '}
            <code className="inline-code">RADAR_ARGOCD_TOKEN_FILE</code>), but it couldn&apos;t be
            used, so the deep diff is disabled. Fix the deployment&apos;s environment or Secret and
            restart; check the Radar pod logs for details.
          </p>
        </div>
      </div>
    )
  }
  return (
    <div>
      <p className="text-xs text-theme-text-tertiary mb-3">
        Powers the full Git-rendered desired-vs-live diff on GitOps Application pages — what Git
        declares vs what&apos;s actually running.
      </p>
      <div className="rounded-md border border-skyhook-500/30 bg-skyhook-500/[0.07] p-3">
        <p className="flex items-center gap-1.5 text-sm font-medium text-theme-text-primary">
          <Terminal className="w-3.5 h-3.5 shrink-0 text-skyhook-500" />
          Configured from the environment
        </p>
        <p className="mt-1 text-xs text-theme-text-tertiary">
          The token is provided by the deployment via{' '}
          <code className="inline-code">RADAR_ARGOCD_TOKEN</code> (or{' '}
          <code className="inline-code">RADAR_ARGOCD_TOKEN_FILE</code>), so this integration is
          read-only here. Edit the deployment&apos;s environment or Secret and restart to change it.
        </p>
        <dl className="mt-3 space-y-1 text-xs">
          <div className="flex gap-2">
            <dt className="w-24 shrink-0 text-theme-text-tertiary">Server</dt>
            <dd className="min-w-0 truncate text-theme-text-secondary">
              {url || 'auto-discovered in this cluster'}
            </dd>
          </div>
          <div className="flex gap-2">
            <dt className="w-24 shrink-0 text-theme-text-tertiary">Token</dt>
            <dd className="text-theme-text-secondary">provided via environment</dd>
          </div>
          {insecureTls && (
            <div className="flex gap-2">
              <dt className="w-24 shrink-0 text-theme-text-tertiary">TLS</dt>
              <dd className="text-amber-600 dark:text-amber-400/80">verification skipped</dd>
            </div>
          )}
        </dl>
      </div>
      <p className="mt-2 text-xs text-theme-text-tertiary">
        If the diff isn&apos;t loading, verify the token is valid for this cluster&apos;s Argo CD and
        check the Radar pod logs.
      </p>
    </div>
  )
}

function ArgoCDEditableField({
  url,
  insecureTls,
  tokenSet,
  cliSession,
  onChangeUrl,
  onChangeInsecureTls,
  onApplied,
}: {
  url: string
  insecureTls: boolean
  tokenSet: boolean
  cliSession?: { server: string; user: string; insecure?: boolean }
  onChangeUrl: (value: string) => void
  onChangeInsecureTls: (value: boolean) => void
  onApplied?: (v: { url: string; insecureTls: boolean; tokenSet: boolean }) => void
}) {
  const [state, setState] = useState<ArgoState>({ status: 'idle' })
  // Token editor three-way state: `touched` = user typed a value (replaces the
  // stored token); `cleared` = user hit Clear (send "" to wipe it); neither =
  // leave the stored token untouched (omit from the PUT).
  const [token, setToken] = useState('')
  const [tokenTouched, setTokenTouched] = useState(false)
  const [tokenCleared, setTokenCleared] = useState(false)
  // Optimistic "is a token stored" after a successful apply (config isn't
  // refetched), so the placeholder reflects the new reality without a reopen.
  const [tokenSetLocal, setTokenSetLocal] = useState<boolean | null>(null)
  const effectiveTokenSet = tokenSetLocal ?? tokenSet

  const clearStatus = () => setState((s) => (s.status === 'connecting' ? s : { status: 'idle' }))

  // Drop a stale "Connected"/error line when the URL changes out from under us
  // (footer Reset, external edit) so it doesn't describe an emptied field.
  useEffect(() => {
    setState((s) => (s.status === 'idle' || s.status === 'connecting' ? s : { status: 'idle' }))
  }, [url])

  const put = async (body: Record<string, unknown>, resultingTokenSet?: boolean) => {
    setState({ status: 'connecting' })
    try {
      const res = await fetch(apiUrl('/integrations/argocd'), {
        method: 'PUT',
        credentials: getCredentialsMode(),
        headers: { 'Content-Type': 'application/json', ...getAuthHeaders() },
        body: JSON.stringify(body),
      })
      const data = await res.json().catch(() => null)
      if (!res.ok) {
        setState({ status: 'error', error: data?.error || res.statusText })
        return
      }
      // Reset the token editor so the field reflects the now-stored token.
      setToken('')
      setTokenTouched(false)
      setTokenCleared(false)
      if (resultingTokenSet !== undefined) setTokenSetLocal(resultingTokenSet)
      onApplied?.({
        url: (body.argoCdUrl as string) ?? '',
        insecureTls: (body.argoCdInsecureTls as boolean) ?? false,
        tokenSet: resultingTokenSet ?? effectiveTokenSet,
      })
      setState({ status: 'connected' })
    } catch (err) {
      setState({ status: 'error', error: String(err) })
    }
  }

  const handleConnect = () => {
    let argoCdToken: string | undefined
    let resultingTokenSet = effectiveTokenSet
    if (tokenCleared) {
      argoCdToken = ''
      resultingTokenSet = false
    } else if (tokenTouched && token !== '') {
      argoCdToken = token
      resultingTokenSet = true
    }
    put(
      {
        argoCdUrl: url.trim(),
        argoCdInsecureTls: insecureTls,
        ...(argoCdToken !== undefined ? { argoCdToken } : {}),
      },
      resultingTokenSet,
    )
  }

  const handleUseCliToken = () => {
    // Only reachable from the "Use this session" button, which renders solely
    // when a session was detected. The detected session is for its own server
    // (which may differ from the URL field) — connect to that server with its TLS
    // mode (Argo CD is https; the insecure flag covers self-signed) and reflect
    // it in the form. An explicit URL is required: the server rejects useCliToken
    // with an empty URL rather than routing the CLI token to a discovered server.
    if (!cliSession) return
    const sessionUrl = /^https?:\/\//i.test(cliSession.server)
      ? cliSession.server
      : `https://${cliSession.server}`
    const insecure = cliSession.insecure ?? false
    onChangeUrl(sessionUrl)
    onChangeInsecureTls(insecure)
    put({ argoCdUrl: sessionUrl, argoCdInsecureTls: insecure, useCliToken: true }, true)
  }

  const showConfiguredPlaceholder = effectiveTokenSet && !tokenTouched && !tokenCleared
  const connecting = state.status === 'connecting'

  return (
    <div>
      <p className="text-xs text-theme-text-tertiary mb-3">
        Connect your Argo CD server for the full Git-rendered desired-vs-live diff on GitOps
        Application pages — what Git declares vs what's actually running. Without it, Radar falls
        back to a lighter annotation-based drift that can miss fields.
      </p>

      <label className="block text-sm font-medium text-theme-text-primary mb-1">Server URL</label>
      <p className="text-xs text-theme-text-tertiary mb-1">
        Leave blank to auto-discover the argocd-server in this cluster, or enter its API URL.
      </p>
      <Input
        value={url}
        onChange={(e) => { onChangeUrl(e.target.value); clearStatus() }}
        placeholder="auto-discover argocd-server"
        className="w-full px-3 py-1.5 text-sm bg-theme-elevated border border-theme-border rounded-md text-theme-text-primary placeholder:text-theme-text-tertiary focus:outline-none focus:border-skyhook-500"
      />

      {cliSession && (
        <div className="mt-3 rounded-md border border-skyhook-500/30 bg-skyhook-500/[0.07] p-3">
          <div className="flex items-start justify-between gap-3">
            <div className="min-w-0">
              <p className="flex items-center gap-1.5 text-sm font-medium text-theme-text-primary">
                <Terminal className="w-3.5 h-3.5 shrink-0 text-skyhook-500" />
                Argo CD CLI session found
              </p>
              <p className="mt-0.5 truncate text-xs text-theme-text-tertiary">
                {cliSession.user && cliSession.user !== cliSession.server ? (
                  <><span className="text-theme-text-secondary">{cliSession.user}</span> @ {cliSession.server}</>
                ) : (
                  <span className="text-theme-text-secondary">{cliSession.server}</span>
                )}
              </p>
            </div>
            <button
              onClick={handleUseCliToken}
              disabled={connecting}
              className="shrink-0 flex items-center gap-1.5 px-3 py-1.5 text-xs font-medium btn-brand rounded-md disabled:opacity-50"
            >
              {connecting ? <Loader2 className="w-3.5 h-3.5 animate-spin" /> : <Plug className="w-3.5 h-3.5" />}
              Use this session
            </button>
          </div>
          <p className="mt-2 text-[11px] text-theme-text-tertiary">
            Reuses your <code className="inline-code">argocd login</code> token — nothing to generate or paste.
          </p>
        </div>
      )}

      <label className="block text-sm font-medium text-theme-text-primary mt-3 mb-1">
        {cliSession ? 'Or paste a token' : 'Auth token'}
      </label>
      <p className="text-xs text-theme-text-tertiary mb-1">
        Lets Radar read your applications. Create one with{' '}
        <code className="inline-code">argocd account generate-token</code>, or in the Argo CD UI under
        Settings → Accounts.
      </p>
      <div className="flex items-center gap-2">
        <input
          type="password"
          value={showConfiguredPlaceholder ? '' : token}
          onChange={(e) => { setToken(e.target.value); setTokenTouched(true); setTokenCleared(false); clearStatus() }}
          placeholder={showConfiguredPlaceholder ? '•••• configured' : 'Argo CD auth token'}
          className="flex-1 min-w-0 px-3 py-1.5 text-sm bg-theme-elevated border border-theme-border rounded-md text-theme-text-primary placeholder:text-theme-text-tertiary focus:outline-none focus:border-skyhook-500"
        />
        {effectiveTokenSet && !tokenCleared && (
          <button
            onClick={() => { setToken(''); setTokenTouched(false); setTokenCleared(true); clearStatus() }}
            className="shrink-0 px-2.5 py-1.5 text-xs font-medium text-theme-text-secondary hover:text-theme-text-primary hover:bg-theme-elevated rounded-md transition-colors"
          >
            Clear
          </button>
        )}
      </div>
      {tokenCleared && (
        <p className="mt-1 text-xs text-amber-600 dark:text-amber-400/80">Token will be cleared on save.</p>
      )}

      <label className="mt-2 flex items-center gap-2 cursor-pointer">
        <input
          type="checkbox"
          checked={insecureTls}
          onChange={(e) => { onChangeInsecureTls(e.target.checked); clearStatus() }}
          className="h-3.5 w-3.5 accent-skyhook-600"
        />
        <span className="text-xs text-theme-text-secondary">Skip TLS verification (self-signed Argo CD server)</span>
      </label>

      <div className="mt-3">
        <button
          onClick={handleConnect}
          disabled={connecting}
          className="flex items-center gap-1.5 px-3 py-1.5 text-xs font-medium btn-brand rounded-md disabled:opacity-50"
        >
          {connecting ? <Loader2 className="w-3.5 h-3.5 animate-spin" /> : <Plug className="w-3.5 h-3.5" />}
          Connect &amp; save
        </button>
      </div>

      {state.status === 'connected' ? (
        <p className="mt-2 flex items-center gap-1 text-xs text-green-600 dark:text-green-400/80">
          <Check className="w-3 h-3 shrink-0" />
          Connected to Argo CD — applied, no restart needed
        </p>
      ) : state.status === 'error' ? (
        <p className="mt-2 text-xs text-red-600 dark:text-red-400/80">{state.error}</p>
      ) : (
        <p className="mt-2 text-xs text-theme-text-tertiary">
          Connecting verifies the URL and token before anything is saved.
        </p>
      )}
    </div>
  )
}

// -- Shared Field Components --------------------------------------------------

function ConfigField({
  label,
  help,
  value,
  effectiveValue,
  placeholder,
  onChange,
}: {
  label: string
  help?: string
  value: string
  effectiveValue?: string
  placeholder?: string
  onChange: (value: string) => void
}) {
  return (
    <div>
      <label className="block text-sm font-medium text-theme-text-primary mb-1">
        {label}
      </label>
      {help && <p className="text-xs text-theme-text-tertiary mb-1">{help}</p>}
      <Input
        value={value}
        onChange={(e) => onChange(e.target.value)}
        placeholder={placeholder}
        className="w-full px-3 py-1.5 text-sm bg-theme-elevated border border-theme-border rounded-md text-theme-text-primary placeholder:text-theme-text-tertiary focus:outline-none focus:border-skyhook-500"
      />
      <EffectiveHint current={value || undefined} effective={effectiveValue} />
    </div>
  )
}

// Comma-separated list input. Keeps a local string buffer so intermediate states
// like "foo," or "foo,," survive — parsing into an array on every keystroke
// (split/trim/filter) would otherwise strip trailing commas before they re-render.
// The focus flag is load-bearing: without it, every parent re-render during typing
// would overwrite `text` with the canonical joined form and wipe the keystroke.
// On blur the buffer resyncs to the canonical "a, b" form.
function ConfigArrayField({
  label,
  help,
  value,
  effectiveValue,
  placeholder,
  onChange,
}: {
  label: string
  help?: string
  value?: string[]
  effectiveValue?: string[]
  placeholder?: string
  onChange: (value: string[] | undefined) => void
}) {
  const canonical = (v?: string[]) => v?.join(', ') ?? ''
  const [text, setText] = useState(() => canonical(value))
  const focusedRef = useRef(false)

  useEffect(() => {
    if (!focusedRef.current) setText(canonical(value))
  }, [value])

  const commit = (raw: string) => {
    const parts = raw.split(',').map(s => s.trim()).filter(Boolean)
    onChange(parts.length > 0 ? parts : undefined)
  }

  return (
    <div>
      <label className="block text-sm font-medium text-theme-text-primary mb-1">
        {label}
      </label>
      {help && <p className="text-xs text-theme-text-tertiary mb-1">{help}</p>}
      <Input
        value={text}
        onFocus={() => { focusedRef.current = true }}
        onBlur={() => {
          focusedRef.current = false
          setText(canonical(value))
        }}
        onChange={(e) => {
          setText(e.target.value)
          commit(e.target.value)
        }}
        placeholder={placeholder}
        className="w-full px-3 py-1.5 text-sm bg-theme-elevated border border-theme-border rounded-md text-theme-text-primary placeholder:text-theme-text-tertiary focus:outline-none focus:border-skyhook-500"
      />
      <EffectiveHint current={canonical(value) || undefined} effective={canonical(effectiveValue) || undefined} />
    </div>
  )
}

function ConfigNumberField({
  label,
  help,
  value,
  effectiveValue,
  placeholder,
  onChange,
}: {
  label: string
  help?: string
  value?: number
  effectiveValue?: number
  placeholder?: string
  onChange: (value: number | undefined) => void
}) {
  return (
    <div>
      <label className="block text-sm font-medium text-theme-text-primary mb-1">
        {label}
      </label>
      {help && <p className="text-xs text-theme-text-tertiary mb-1">{help}</p>}
      <input
        type="number"
        value={value ?? ''}
        onChange={(e) => onChange(e.target.value ? parseInt(e.target.value, 10) || undefined : undefined)}
        placeholder={placeholder}
        className="w-full px-3 py-1.5 text-sm bg-theme-elevated border border-theme-border rounded-md text-theme-text-primary placeholder:text-theme-text-tertiary focus:outline-none focus:border-skyhook-500"
      />
      <EffectiveHint current={value} effective={effectiveValue} />
    </div>
  )
}

function ConfigToggle({
  label,
  description,
  value,
  onChange,
}: {
  label: string
  description?: string
  value: boolean
  onChange: (value: boolean) => void
}) {
  return (
    <label className="flex items-start justify-between gap-3 py-1 cursor-pointer">
      <span className="min-w-0">
        <span className="block text-sm text-theme-text-primary">{label}</span>
        {description && (
          <span className="mt-0.5 block text-xs text-theme-text-tertiary">{description}</span>
        )}
      </span>
      <button
        role="switch"
        aria-checked={value}
        onClick={() => onChange(!value)}
        className={clsx(
          'relative w-9 h-5 shrink-0 rounded-full transition-colors',
          value ? 'bg-skyhook-600' : 'bg-theme-elevated border border-theme-border'
        )}
      >
        <span
          className={clsx(
            'absolute top-0.5 left-0.5 w-4 h-4 rounded-full bg-white transition-transform shadow-sm',
            value && 'translate-x-4'
          )}
        />
      </button>
    </label>
  )
}

function EffectiveHint({
  current,
  effective,
}: {
  current?: string | number
  effective?: string | number
}) {
  if (!effective || effective === current) return null
  const currentStr = current != null ? String(current) : ''
  const effectiveStr = String(effective)
  if (currentStr === effectiveStr) return null

  return (
    <p className="text-xs text-amber-600 dark:text-amber-400/80 mt-0.5">
      Currently running: {effectiveStr} (restart to apply)
    </p>
  )
}
