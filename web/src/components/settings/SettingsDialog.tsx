import { useState, useEffect, useRef, useCallback } from 'react'
import { createPortal } from 'react-dom'
import { Settings, X, RotateCcw, Loader2 } from 'lucide-react'
import { clsx } from 'clsx'
import { useAnimatedUnmount } from '../../hooks/useAnimatedUnmount'
import { TRANSITION_BACKDROP, TRANSITION_PANEL } from '../../utils/animation'

interface Config {
  kubeconfig?: string
  kubeconfigDirs?: string[]
  namespace?: string
  port?: number
  noBrowser?: boolean
  timelineStorage?: string
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

type Tab = 'startup' | 'preferences'

interface SettingsDialogProps {
  open: boolean
  onClose: () => void
}

export function SettingsDialog({ open, onClose }: SettingsDialogProps) {
  const dialogRef = useRef<HTMLDivElement>(null)
  const { shouldRender, isOpen } = useAnimatedUnmount(open, 200)
  const [activeTab, setActiveTab] = useState<Tab>('startup')
  const [configData, setConfigData] = useState<ConfigResponse | null>(null)
  const [editedConfig, setEditedConfig] = useState<Config>({})
  const [saving, setSaving] = useState(false)
  const [saveMessage, setSaveMessage] = useState<string | null>(null)
  const [configDirty, setConfigDirty] = useState(false)

  // Preferences state
  const [logsWrap, setLogsWrap] = useState<boolean | null>(null)
  const [logsTimestamps, setLogsTimestamps] = useState<boolean | null>(null)

  // Load config and settings on open
  useEffect(() => {
    if (!open) return
    setSaveMessage(null)
    setConfigDirty(false)

    fetch('/api/config')
      .then((res) => res.ok ? res.json() : null)
      .then((data: ConfigResponse | null) => {
        if (data) {
          setConfigData(data)
          setEditedConfig(data.file)
        }
      })
      .catch((err) => console.warn('[settings] Failed to load config:', err))

    fetch('/api/settings')
      .then((res) => res.ok ? res.json() : null)
      .then((data) => {
        if (data) {
          setLogsWrap(data.logsWrap ?? null)
          setLogsTimestamps(data.logsTimestamps ?? null)
        }
      })
      .catch((err) => console.warn('[settings] Failed to load settings:', err))
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
      const res = await fetch('/api/config', {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
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

  const resetConfig = useCallback(async () => {
    setSaving(true)
    try {
      const res = await fetch('/api/config', {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({}),
      })
      if (res.ok) {
        setEditedConfig({})
        setConfigDirty(false)
        setSaveMessage('Config reset to defaults. Changes take effect on next launch.')
      } else {
        const data = await res.json().catch(() => null)
        setSaveMessage(`Error: ${data?.error || res.statusText}`)
      }
    } catch (err) {
      setSaveMessage(`Error: ${err}`)
    } finally {
      setSaving(false)
    }
  }, [])

  const updatePreference = useCallback((field: string, value: boolean) => {
    if (field === 'logsWrap') setLogsWrap(value)
    if (field === 'logsTimestamps') setLogsTimestamps(value)

    // Also update localStorage for immediate effect
    try {
      if (field === 'logsWrap') localStorage.setItem('radar-logs-wrap', String(value))
      if (field === 'logsTimestamps') localStorage.setItem('radar-logs-timestamps', String(value))
    } catch {}

    fetch('/api/settings', {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ [field]: value }),
    }).catch((err) => console.warn('[settings] Failed to save preference:', err))
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
          'relative bg-theme-surface border border-theme-border shadow-2xl w-full outline-none flex flex-col',
          'max-sm:inset-0 max-sm:absolute max-sm:rounded-none max-sm:max-h-full max-sm:border-0',
          'sm:rounded-lg sm:max-w-xl sm:mx-4 sm:max-h-[85vh]',
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

        {/* Tabs */}
        <div className="flex border-b border-theme-border shrink-0">
          <button
            onClick={() => setActiveTab('startup')}
            className={clsx(
              'px-4 py-2 text-sm font-medium transition-colors',
              activeTab === 'startup'
                ? 'text-theme-text-primary border-b-2 border-blue-500'
                : 'text-theme-text-secondary hover:text-theme-text-primary'
            )}
          >
            Configuration
          </button>
          <button
            onClick={() => setActiveTab('preferences')}
            className={clsx(
              'px-4 py-2 text-sm font-medium transition-colors',
              activeTab === 'preferences'
                ? 'text-theme-text-primary border-b-2 border-blue-500'
                : 'text-theme-text-secondary hover:text-theme-text-primary'
            )}
          >
            Preferences
          </button>
        </div>

        {/* Content */}
        <div className="overflow-y-auto p-4 flex-1">
          {activeTab === 'startup' && (
            <StartupConfigTab
              config={editedConfig}
              effectiveConfig={configData?.effective}
              isDesktop={isDesktop}
              onChange={updateConfigField}
            />
          )}
          {activeTab === 'preferences' && (
            <PreferencesTab
              logsWrap={logsWrap}
              logsTimestamps={logsTimestamps}
              onChange={updatePreference}
            />
          )}
        </div>

        {/* Footer */}
        {activeTab === 'startup' && (
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
              className="flex items-center gap-1.5 px-4 py-1.5 text-sm font-medium bg-blue-600 hover:bg-blue-700 text-white rounded-md transition-colors disabled:opacity-50"
            >
              {saving && <Loader2 className="w-3.5 h-3.5 animate-spin" />}
              Save
            </button>
          </div>
        )}
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

      <ConfigField
        label="Kubeconfig Directories"
        help="Comma-separated directories containing kubeconfig files"
        value={config.kubeconfigDirs?.join(', ') ?? ''}
        effectiveValue={effectiveConfig?.kubeconfigDirs?.join(', ')}
        placeholder="/path/to/dir1, /path/to/dir2"
        onChange={(v) => onChange('kubeconfigDirs', v ? v.split(',').map(s => s.trim()).filter(Boolean) : undefined)}
      />

      <ConfigField
        label="Default Namespace"
        help="Initial namespace filter on startup"
        value={config.namespace ?? ''}
        effectiveValue={effectiveConfig?.namespace}
        placeholder="All namespaces"
        onChange={(v) => onChange('namespace', v || undefined)}
      />

      {!isDesktop && (
        <ConfigNumberField
          label="Port"
          help="Server port"
          value={config.port}
          effectiveValue={effectiveConfig?.port}
          placeholder="9280"
          onChange={(v) => onChange('port', v)}
        />
      )}

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
              onChange={(e) => onChange('timelineStorage', e.target.value === 'memory' ? undefined : e.target.value)}
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

          {!isDesktop && (
            <ConfigToggle
              label="MCP Server (AI tools)"
              value={config.mcp ?? true}
              onChange={(v) => onChange('mcp', v)}
            />
          )}
        </div>
      </div>
    </div>
  )
}

// -- Preferences Tab ----------------------------------------------------------

function PreferencesTab({
  logsWrap,
  logsTimestamps,
  onChange,
}: {
  logsWrap: boolean | null
  logsTimestamps: boolean | null
  onChange: (field: string, value: boolean) => void
}) {
  return (
    <div className="space-y-4">
      <p className="text-xs text-theme-text-tertiary">
        These preferences apply immediately.
      </p>

      <div className="space-y-3">
        <h4 className="text-xs font-medium text-theme-text-secondary uppercase tracking-wider">Log Viewer</h4>

        <ConfigToggle
          label="Word wrap"
          value={logsWrap ?? true}
          onChange={(v) => onChange('logsWrap', v)}
        />

        <ConfigToggle
          label="Show timestamps"
          value={logsTimestamps ?? true}
          onChange={(v) => onChange('logsTimestamps', v)}
        />
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
        className="w-full px-3 py-1.5 text-sm bg-theme-elevated border border-theme-border rounded-md text-theme-text-primary placeholder:text-theme-text-tertiary focus:outline-none focus:border-blue-500"
      />
      <EffectiveHint current={value || undefined} effective={effectiveValue} />
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
          value ? 'bg-blue-600' : 'bg-theme-elevated border border-theme-border'
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
    <p className="text-xs text-amber-400/80 mt-0.5">
      Currently running: {effectiveStr} (from CLI flag)
    </p>
  )
}
