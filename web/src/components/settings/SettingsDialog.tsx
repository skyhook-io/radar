import { useState, useEffect, useRef, useCallback } from 'react'
import { createPortal } from 'react-dom'
import { Settings, X, RotateCcw, Loader2, Copy, Check, Pin } from 'lucide-react'
import { clsx } from 'clsx'
import { useAnimatedUnmount } from '../../hooks/useAnimatedUnmount'
import { TRANSITION_BACKDROP, TRANSITION_PANEL } from '../../utils/animation'
import { apiUrl, getAuthHeaders, getCredentialsMode } from '../../api/config'

interface Config {
  kubeconfig?: string
  kubeconfigDirs?: string[]
  namespace?: string
  port?: number
  noBrowser?: boolean
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
}

interface SettingsDialogProps {
  open: boolean
  onClose: () => void
}

export function SettingsDialog({ open, onClose }: SettingsDialogProps) {
  const dialogRef = useRef<HTMLDivElement>(null)
  const { shouldRender, isOpen } = useAnimatedUnmount(open, 200)
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
          'sm:rounded-xl sm:max-w-xl sm:mx-4 sm:max-h-[85vh]',
          TRANSITION_PANEL,
          isOpen ? 'opacity-100 scale-100' : 'opacity-0 scale-95'
        )}
      >
        {/* Header */}
        <div className="flex items-center justify-between p-4 border-b border-theme-border shrink-0">
          <div className="flex items-center gap-2">
            <Settings className="w-5 h-5 text-theme-text-secondary" />
            <h2 className="text-lg font-semibold text-theme-text-primary">Settings</h2>
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
          <StartupConfigTab
            config={editedConfig}
            effectiveConfig={configData?.effective}
            isDesktop={isDesktop}
            onChange={updateConfigField}
          />
        </div>

        {/* Footer */}
        <div className="flex items-center justify-between gap-3 p-4 border-t border-theme-border shrink-0">
            <div className="flex items-center gap-2">
              <button
                onClick={resetConfig}
                disabled={saving}
                className="flex items-center gap-1.5 px-3 py-1.5 text-xs font-medium text-theme-text-secondary hover:text-theme-text-primary hover:bg-theme-elevated rounded-md transition-colors disabled:opacity-50"
                title="Reset all configuration to defaults"
              >
                <RotateCcw className="w-3.5 h-3.5" />
                Reset
              </button>
              {saveMessage && (
                <span className={clsx(
                  'text-xs',
                  saveMessage.startsWith('Error') ? 'text-red-400' : 'text-green-400'
                )}>
                  {saveMessage}
                </span>
              )}
            </div>
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
    </div>,
    document.body
  )
}

// -- Startup Configuration Tab ------------------------------------------------

function StartupConfigTab({
  config,
  effectiveConfig,
  isDesktop,
  onChange,
}: {
  config: Config
  effectiveConfig?: Config
  isDesktop: boolean
  onChange: <K extends keyof Config>(field: K, value: Config[K]) => void
}) {
  return (
    <div className="space-y-4">
      <p className="text-xs text-theme-text-tertiary">
        Changes require a restart to take effect.
        {isDesktop
          ? ' Quit and relaunch Radar to apply.'
          : ' Stop and restart the radar command to apply.'}
      </p>

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
        help="Initial namespace filter on startup"
        value={config.namespace ?? ''}
        effectiveValue={effectiveConfig?.namespace}
        placeholder="All namespaces"
        onChange={(v) => onChange('namespace', v || undefined)}
      />

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

      {!isDesktop && (
        <ConfigToggle
          label="Open browser on start"
          value={!(config.noBrowser ?? false)}
          onChange={(v) => onChange('noBrowser', !v ? true : undefined)}
        />
      )}

      <div className="border-t border-theme-border pt-4 mt-4">
        <h4 className="text-xs font-medium text-theme-text-secondary uppercase tracking-wider mb-3">Timeline</h4>

        <div className="space-y-4">
          <div>
            <label className="block text-sm font-medium text-theme-text-primary mb-1">
              Storage Backend
            </label>
            <select
              value={config.timelineStorage ?? 'memory'}
              onChange={(e) => onChange('timelineStorage', e.target.value === 'memory' ? undefined : e.target.value as 'sqlite')}
              className="w-full px-3 py-1.5 text-sm bg-theme-elevated border border-theme-border rounded-md text-theme-text-primary focus:outline-none focus:border-blue-500"
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
        </div>
      </div>

      <div className="border-t border-theme-border pt-4 mt-4">
        <h4 className="text-xs font-medium text-theme-text-secondary uppercase tracking-wider mb-3">Integrations</h4>

        <div className="space-y-4">
          <ConfigField
            label="Prometheus URL"
            help="Manual Prometheus/VictoriaMetrics URL (skips auto-discovery)"
            value={config.prometheusUrl ?? ''}
            effectiveValue={effectiveConfig?.prometheusUrl}
            placeholder="http://prometheus-server.monitoring:9090"
            onChange={(v) => onChange('prometheusUrl', v || undefined)}
          />

          <MCPSection
            mcpEnabled={config.mcp ?? true}
            onToggle={(v) => onChange('mcp', v)}
            isDesktop={isDesktop}
            portPinned={config.port != null && config.port > 0}
            onPinPort={(port) => onChange('port', port)}
          />
        </div>
      </div>
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
        label="MCP Server (AI tools)"
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
              <button
                onClick={handleCopy}
                className="shrink-0 p-1.5 text-theme-text-tertiary hover:text-theme-text-primary hover:bg-theme-elevated rounded-md transition-colors"
                title="Copy MCP URL"
              >
                {copied ? <Check className="w-3.5 h-3.5 text-green-500" /> : <Copy className="w-3.5 h-3.5" />}
              </button>
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
        className="w-full px-3 py-1.5 text-sm bg-theme-elevated border border-theme-border rounded-md text-theme-text-primary placeholder:text-theme-text-tertiary focus:outline-none focus:border-blue-500"
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
        className="w-full px-3 py-1.5 text-sm bg-theme-elevated border border-theme-border rounded-md text-theme-text-primary placeholder:text-theme-text-tertiary focus:outline-none focus:border-blue-500"
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
        className="w-full px-3 py-1.5 text-sm bg-theme-elevated border border-theme-border rounded-md text-theme-text-primary placeholder:text-theme-text-tertiary focus:outline-none focus:border-blue-500"
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
