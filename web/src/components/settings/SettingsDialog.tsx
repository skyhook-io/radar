import { useState, useEffect, useRef, useCallback, type ReactNode } from 'react'
import { createPortal } from 'react-dom'
import { Settings, X, RotateCcw, RotateCw, Loader2, Copy, Check, Pin, Shield, Lock, Plug, Plus } from 'lucide-react'
import { clsx } from 'clsx'
import { useAnimatedUnmount } from '../../hooks/useAnimatedUnmount'
import { TRANSITION_BACKDROP, TRANSITION_PANEL } from '../../utils/animation'
import { apiUrl, getAuthHeaders, getCredentialsMode } from '../../api/config'
import { useCloudRole, useVersionCheck } from '../../api/client'
import { useCapabilitiesContext } from '../../contexts/CapabilitiesContext'
import { Tooltip } from '../ui/Tooltip'
import type { DeploymentMode } from '../../types'

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
  mcp?: boolean | null
}

interface ConfigResponse {
  file: Config
  effective: Config
  isDesktop: boolean
  prometheusHeaderKeys?: string[]
}

interface SettingsDialogProps {
  open: boolean
  onClose: () => void
  onShowMyPermissions?: () => void
}

export function SettingsDialog({ open, onClose, onShowMyPermissions }: SettingsDialogProps) {
  const dialogRef = useRef<HTMLDivElement>(null)
  const { shouldRender, isOpen } = useAnimatedUnmount(open, 200)
  const { data: versionInfo } = useVersionCheck()
  // Radar configuration (kubeconfig, port, integrations…) is host-level and
  // affects every user of this instance, so it's gated to owners. Personal
  // sections (My permissions) stay visible to everyone. Non-Cloud callers
  // (OSS, OIDC, kubectl plugin) have no role and pass — single-user laptops
  // are never locked out of their own config. Backend enforces this too.
  const { canAtLeast } = useCloudRole()
  const capabilities = useCapabilitiesContext()
  const canEditConfig = canAtLeast('owner')
  const [configData, setConfigData] = useState<ConfigResponse | null>(null)
  const [editedConfig, setEditedConfig] = useState<Config>({})
  const [saving, setSaving] = useState(false)
  const [saveMessage, setSaveMessage] = useState<string | null>(null)
  const [configDirty, setConfigDirty] = useState(false)
  const [loadError, setLoadError] = useState<string | null>(null)

  // Load config on open
  useEffect(() => {
    if (!open) return
    setSaveMessage(null)
    setConfigDirty(false)
    setLoadError(null)

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
  }, [open])

  // ESC key
  useEffect(() => {
    if (!open) return
    const handleKeyDown = (e: KeyboardEvent) => {
      if (e.key === 'Escape') {
        e.stopPropagation()
        onClose()
      }
    }
    document.addEventListener('keydown', handleKeyDown, true)
    return () => document.removeEventListener('keydown', handleKeyDown, true)
  }, [open, onClose])

  // Focus trap
  useEffect(() => {
    if (open && dialogRef.current) {
      dialogRef.current.focus()
    }
  }, [open])

  const updateConfigField = useCallback(<K extends keyof Config>(field: K, value: Config[K]) => {
    setEditedConfig((prev) => ({ ...prev, [field]: value }))
    setConfigDirty(true)
    setSaveMessage(null)
  }, [])

  const saveConfig = useCallback(async () => {
    setSaving(true)
    setSaveMessage(null)
    try {
      const res = await fetch(apiUrl('/config'), {
        method: 'PUT',
        credentials: getCredentialsMode(),
        headers: { 'Content-Type': 'application/json', ...getAuthHeaders() },
        body: JSON.stringify(editedConfig),
      })
      if (!res.ok) {
        const data = await res.json().catch(() => null)
        setSaveMessage(`Error: ${data?.error || res.statusText}`)
      } else {
        setConfigDirty(false)
        setSaveMessage('Saved. Changes take effect on next launch.')
      }
    } catch (err) {
      setSaveMessage(`Error: ${err}`)
    } finally {
      setSaving(false)
    }
  }, [editedConfig])

  const resetConfig = useCallback(() => {
    setEditedConfig({})
    setConfigDirty(true)
    setSaveMessage('All fields cleared. Press Save to apply.')
  }, [])

  if (!shouldRender) return null

  const isDesktop = configData?.isDesktop ?? false
  const deploymentMode = capabilities.deployment?.mode ?? 'local'

  return createPortal(
    <div className="fixed inset-0 z-50 flex items-center justify-center">
      {/* Backdrop */}
      <div
        className={clsx(
          'absolute inset-0 bg-black/60 backdrop-blur-sm',
          TRANSITION_BACKDROP,
          isOpen ? 'opacity-100' : 'opacity-0'
        )}
        onClick={onClose}
      />

      {/* Dialog */}
      <div
        ref={dialogRef}
        tabIndex={-1}
        className={clsx(
          'relative bg-theme-surface border border-theme-border shadow-theme-lg w-full outline-none flex flex-col',
          'max-sm:inset-0 max-sm:absolute max-sm:rounded-none max-sm:max-h-full max-sm:border-0',
          'sm:rounded-xl sm:max-w-2xl sm:mx-4 sm:max-h-[85vh]',
          TRANSITION_PANEL,
          isOpen ? 'opacity-100 scale-100' : 'opacity-0 scale-95'
        )}
      >
        {/* Header */}
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
            onClick={onClose}
            className="p-1 text-theme-text-secondary hover:text-theme-text-primary hover:bg-theme-elevated rounded"
          >
            <X className="w-5 h-5" />
          </button>
        </div>

        {/* Content */}
        <div className="overflow-y-auto p-4 flex-1">
          {loadError && (
            <div className="mb-3 px-3 py-2 text-xs text-amber-700 dark:text-amber-300 bg-amber-500/10 border border-amber-500/20 rounded-md">
              {loadError}
            </div>
          )}
          {onShowMyPermissions && (
            <div className="mb-5">
              <SectionLabel>Personal</SectionLabel>
              <div className="rounded-md border border-theme-border bg-theme-elevated/50 p-3">
                <div className="flex items-center justify-between gap-3">
                  <div className="min-w-0">
                    <h3 className="text-sm font-medium text-theme-text-primary">My permissions</h3>
                    <p className="mt-0.5 text-xs text-theme-text-tertiary">
                      View what your current identity can do in this cluster.
                    </p>
                  </div>
                  <button
                    onClick={onShowMyPermissions}
                    className="shrink-0 flex items-center gap-1.5 px-3 py-1.5 text-xs font-medium text-theme-text-secondary hover:text-theme-text-primary hover:bg-theme-hover rounded-md transition-colors"
                  >
                    <Shield className="w-3.5 h-3.5" />
                    Open
                  </button>
                </div>
              </div>
            </div>
          )}

          <SectionLabel>Radar configuration</SectionLabel>
          {canEditConfig ? (
            <StartupConfigTab
              config={editedConfig}
              effectiveConfig={configData?.effective}
              isDesktop={isDesktop}
              deploymentMode={deploymentMode}
              prometheusHeaderKeys={configData?.prometheusHeaderKeys ?? []}
              onChange={updateConfigField}
            />
          ) : (
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
          )}
        </div>

        {/* Footer — only the owner-gated config section is editable, so hide
            the save controls entirely for non-owners (personal sections save
            themselves). */}
        {canEditConfig && (
        <div className="flex items-center justify-between gap-3 px-4 py-2.5 border-t border-theme-border shrink-0">
            <div className="flex items-center gap-2">
              <Tooltip content="Clear all fields — reverts to defaults when saved">
              <button
                onClick={resetConfig}
                disabled={saving}
                className="flex items-center gap-1.5 px-3 py-1.5 text-xs font-medium text-theme-text-secondary hover:text-theme-text-primary hover:bg-theme-elevated rounded-md transition-colors disabled:opacity-50 disabled:pointer-events-none"
              >
                <RotateCcw className="w-3.5 h-3.5" />
                Reset
              </button>
              </Tooltip>
              {saveMessage && (
                <span className={clsx(
                  'text-xs',
                  saveMessage.startsWith('Error') ? 'text-red-400' : 'text-green-400'
                )}>
                  {saveMessage}
                </span>
              )}
            </div>
            <div className="flex items-center gap-3">
              <span className="hidden sm:flex items-center gap-1.5 text-[11px] text-theme-text-tertiary">
                <RotateCw className="w-3 h-3" />
                Applies on next launch
              </span>
              <button
                onClick={saveConfig}
                disabled={saving || !configDirty}
                className="flex items-center gap-1.5 px-4 py-1.5 text-sm font-medium btn-brand rounded-md"
              >
                {saving && <Loader2 className="w-3.5 h-3.5 animate-spin" />}
                Save
              </button>
            </div>
          </div>
        )}
      </div>
    </div>,
    document.body
  )
}

// -- Section label ------------------------------------------------------------

function SectionLabel({ children }: { children: ReactNode }) {
  return (
    <h3 className="text-xs font-medium text-theme-text-secondary uppercase tracking-wider mb-2">
      {children}
    </h3>
  )
}

// A titled card grouping related config fields. Cards (vs thin dividers) give the
// sections clear visual separation in the scroll.
function ConfigSection({ title, children }: { title: string; children: ReactNode }) {
  return (
    <section className="rounded-lg border border-theme-border bg-theme-elevated/30 p-4">
      <h4 className="text-xs font-medium text-theme-text-secondary uppercase tracking-wider mb-3">
        {title}
      </h4>
      <div className="space-y-4">{children}</div>
    </section>
  )
}

// -- Startup Configuration Tab ------------------------------------------------

function StartupConfigTab({
  config,
  effectiveConfig,
  isDesktop,
  deploymentMode,
  prometheusHeaderKeys,
  onChange,
}: {
  config: Config
  effectiveConfig?: Config
  isDesktop: boolean
  deploymentMode: DeploymentMode
  prometheusHeaderKeys: string[]
  onChange: <K extends keyof Config>(field: K, value: Config[K]) => void
}) {
  const showBrowserLaunchControls = !isDesktop && deploymentMode === 'local'
  return (
    <div className="space-y-3">
      <p className="text-xs text-theme-text-tertiary">
        Most changes require a restart to take effect.
        {isDesktop
          ? ' Quit and relaunch Radar to apply.'
          : ' Stop and restart the radar command to apply.'}
      </p>

      <ConfigSection title="Cluster">
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
      </ConfigSection>

      <ConfigSection title="Server">
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
              value={!(config.noBrowser ?? false)}
              onChange={(v) => onChange('noBrowser', !v ? true : undefined)}
            />

            <ConfigField
              label="Browser"
              help="Browser for automatic launch; macOS app names are supported"
              value={config.browser ?? ''}
              effectiveValue={effectiveConfig?.browser}
              placeholder="System default"
              onChange={(v) => onChange('browser', v || undefined)}
            />
          </>
        )}
      </ConfigSection>

      <ConfigSection title="AI Tools">
        <MCPSection
          mcpEnabled={config.mcp ?? true}
          onToggle={(v) => onChange('mcp', v)}
          isDesktop={isDesktop}
          portPinned={config.port != null && config.port > 0}
          onPinPort={(port) => onChange('port', port)}
        />
      </ConfigSection>

      <ConfigSection title="Timeline">
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
      </ConfigSection>

      <ConfigSection title="Integrations">
        <PrometheusConfigField
          value={config.prometheusUrl ?? ''}
          configuredHeaderKeys={prometheusHeaderKeys}
          onChange={(v) => onChange('prometheusUrl', v || undefined)}
        />
      </ConfigSection>
    </div>
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

// Unlike the rest of this dialog, the Prometheus URL can be re-pointed without
// a restart — the metrics path reads it from a mutable global. "Apply now" hits
// PUT /integrations/prometheus, which persists the URL AND re-points the running
// client, then probes it so we can confirm reachability inline. The global Save
// still persists this field too (taking effect next launch, like everything
// else); Apply is the shortcut to "now". No EffectiveHint here — the per-field
// restart-diff hint would contradict the whole point of applying live.
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
}: {
  value: string
  onChange: (value: string) => void
  configuredHeaderKeys: string[]
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
      <label className="block text-sm font-medium text-theme-text-primary mb-1">
        Prometheus URL
      </label>
      <p className="text-xs text-theme-text-tertiary mb-1">
        Manual Prometheus/VictoriaMetrics URL (skips auto-discovery)
      </p>
      <div className="flex items-center gap-2">
        <input
          type="text"
          value={value}
          onChange={(e) => onChange(e.target.value)}
          placeholder="http://prometheus-server.monitoring:9090"
          className="flex-1 min-w-0 px-3 py-1.5 text-sm bg-theme-elevated border border-theme-border rounded-md text-theme-text-primary placeholder:text-theme-text-tertiary focus:outline-none focus:border-skyhook-500"
        />
        <Tooltip content="Apply this URL to the running server now — no restart" wrapperClassName="shrink-0">
          <button
            onClick={handleApply}
            disabled={apply.status === 'applying'}
            className="flex items-center gap-1.5 px-3 py-1.5 text-xs font-medium text-theme-text-secondary hover:text-theme-text-primary hover:bg-theme-elevated border border-theme-border rounded-md transition-colors disabled:opacity-50"
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
                <input
                  type="text"
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
      <input
        type="text"
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
      <input
        type="text"
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
  value,
  onChange,
}: {
  label: string
  value: boolean
  onChange: (value: boolean) => void
}) {
  return (
    <label className="flex items-center justify-between py-1 cursor-pointer group">
      <span className="text-sm text-theme-text-primary group-hover:text-theme-text-primary">{label}</span>
      <button
        role="switch"
        aria-checked={value}
        onClick={() => onChange(!value)}
        className={clsx(
          'relative w-9 h-5 rounded-full transition-colors',
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
