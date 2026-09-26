import { useCallback, useEffect, useRef, useState, type ReactNode } from 'react'
import { Badge, Collapse, ConfirmDialog, SelectMenu } from '@skyhook-io/k8s-ui'
import { ArrowLeft, Info } from 'lucide-react'
import { Tooltip } from '../ui/Tooltip'
import type { ConnectionFeedback } from './ConnectionFormActions'
import { trapConnectionConfirmationFocus } from './connection-confirmation'
import {
  apiUrl,
  getApiBase,
  getAuthHeaders,
  getCredentialsMode
} from '../../api/config'
import { PrometheusConnectionForm } from './PrometheusConnectionForm'
import {
  ArgoCDConnectionForm,
  CostConnectionForm,
  type ArgoConnectionDraft,
  type CostConnectionDraft,
  type SecretEdit
} from './IntegrationConnectionForms'
import type { HeaderOperation } from './ConnectionHeadersEditor'

export type IntegrationKind = 'metrics' | 'argocd' | 'cost'
interface TargetIdentity {
  server: string
  user?: string
  tlsName?: string
  trust?: string
  proxy?: string
  insecureTls?: boolean
}
interface Target {
  binding: string
  context: string
  source?: string
  inFileName?: string
  fingerprint: string
  clientGeneration: number
  operationGeneration: number
  identity: TargetIdentity
}
export interface StoredConnection {
  revision: string
  binding: string
  integration: IntegrationKind
  context: string
  source: string
  inFileName: string
  availability: 'available' | 'removed' | 'unavailable'
  url: string
  headerKeys: string[]
  envHeaderKeys: string[]
  secretSet: boolean
  insecureTls: boolean
  error?: string
}
export interface IntegrationProfile {
  target: Target
  revision: string
  state: 'auto' | 'saved' | 'launch' | 'target_changed' | 'error'
  mode: string
  url: string
  headerKeys: string[]
  envHeaderKeys: string[]
  headersManaged: boolean
  secretSet: boolean
  insecureTls: boolean
  clusterId: string
  previousIdentity?: TargetIdentity
  error?: string
  legacy?: {
    url: string
    headerKeys: string[]
    envHeaderKeys: string[]
    insecureTls: boolean
    mode: string
    clusterId: string
    secretSet: boolean
    revision: string
    error?: string
  }
}
export type IntegrationProfiles = Record<IntegrationKind, IntegrationProfile>
export interface ConnectionResponse {
  profiles: IntegrationProfiles
  connections: StoredConnection[]
  connected: boolean
  checked: boolean
  error?: string
}
interface Update {
  action: string
  legacyRevision?: string
  binding?: string
  sourceRevision?: string
  url?: string
  headers?: HeaderOperation[]
  secret?: SecretEdit
  insecureTls?: boolean
  mode?: string
  clusterId?: string
  confirmRemoval?: boolean
  kinds?: IntegrationKind[]
  useCliToken?: boolean
}
type Task = 'main' | 'reconfirm' | 'replace'
const names: Record<IntegrationKind, string> = {
  metrics: 'Metrics',
  argocd: 'Argo CD',
  cost: 'Cost'
}
export { names as integrationNames }

export function LocalConnectionSettings({
  kind,
  profiles,
  cliSession,
  onChange,
  onDirtyChange,
  onBusyChange,
  status
}: {
  kind: IntegrationKind
  profiles: IntegrationProfiles
  cliSession?: { server: string; user: string; insecure?: boolean }
  onChange: (profiles: IntegrationProfiles) => void
  onDirtyChange: (dirty: boolean) => void
  onBusyChange?: (busy: boolean) => void
  status?: ReactNode
}) {
  const [snapshot, setSnapshot] = useState(profiles)
  const profile = snapshot[kind]
  const [catalog, setCatalog] = useState<StoredConnection[]>([])
  const [task, setTask] = useState<Task>('main')
  const [busy, setBusy] = useState(false)
  const wasBusy = useRef(false)
  const restoreTriggerFocus = useRef(false)
  const [error, setError] = useState('')
  const [catalogError, setCatalogError] = useState(false)
  const [catalogRetry, setCatalogRetry] = useState(0)
  const [message, setMessage] = useState('')
  const [messageWarning, setMessageWarning] = useState(false)
  const [messageRevision, setMessageRevision] = useState('')
  const [url, setUrl] = useState(profile.url)
  const [insecureTls, setInsecureTls] = useState(profile.insecureTls)
  const [mode, setMode] = useState<CostConnectionDraft['mode']>(
    profile.mode as CostConnectionDraft['mode']
  )
  const [clusterId, setClusterId] = useState(profile.clusterId)
  const [credentialDirty, setCredentialDirty] = useState(false)
  const [discoveryDraft, setDiscoveryDraft] = useState(false)
  const [legacyDraft, setLegacyDraft] = useState<IntegrationProfile['legacy']>()
  const [draftGeneration, setDraftGeneration] = useState(0)
  const [selected, setSelected] = useState<
    StoredConnection | null
  >(null)
  const [pending, setPending] = useState<Update | null>(null)
  const confirmationCopy = useRef({ title: '', message: '', label: '' })
  const [confirmBack, setConfirmBack] = useState(false)
  const [accepted, setAccepted] = useState<IntegrationKind[]>([])
  const acceptedChanges = accepted.filter(
    (k) => snapshot[k].state === 'target_changed'
  )
  const request = useRef<AbortController | null>(null)
  const catalogRequest = useRef<AbortController | null>(null)
  const heading = useRef<HTMLHeadingElement>(null)
  const confirmationError = useRef<HTMLParagraphElement>(null)
  const region = useRef<HTMLFieldSetElement>(null)
  const editorRegion = useRef<HTMLFieldSetElement>(null)
  const trigger = useRef<HTMLElement | null>(null)
  const mounted = useRef(true)
  const copySources = catalog.filter(c => c.integration === kind && !c.error && c.url && c.binding !== profile.target.binding)
  const removalContext = profile.target.context
  const dirty =
    !!legacyDraft ||
    !!selected ||
    discoveryDraft ||
    url !== profile.url ||
    insecureTls !== profile.insecureTls ||
    mode !== profile.mode ||
    clusterId !== profile.clusterId ||
    credentialDirty
  const hasSavedConfiguration = !!(
    profile.url || profile.secretSet ||
    profile.headerKeys.length || profile.insecureTls || profile.clusterId
  )
  const automaticDraft = discoveryDraft && !url.trim() && mode === 'auto' &&
    !credentialDirty && !insecureTls && !clusterId
  useEffect(() => {
    const controller = new AbortController()
    catalogRequest.current = controller
    const base = getApiBase()
    void fetch(apiUrl('/integrations/connections'), {
      signal: controller.signal,
      credentials: getCredentialsMode(),
      headers: getAuthHeaders()
    })
      .then(async (response) => {
        const data = (await response.json().catch(() => ({}))) as ConnectionResponse
        if (!response.ok)
          throw new Error(data.error || 'Could not load saved connections.')
        if (!controller.signal.aborted && getApiBase() === base) {
          setCatalog(data.connections)
          setCatalogError(false)
        }
      })
      .catch(() => {
        if (!controller.signal.aborted && getApiBase() === base)
          setCatalogError(true)
      })
    return () => controller.abort()
  }, [kind, profile.target.binding, profiles, draftGeneration, catalogRetry])
  useEffect(() => onDirtyChange(dirty), [dirty, onDirtyChange])
  useEffect(() => () => onBusyChange?.(false), [onBusyChange])
  useEffect(() => {
    mounted.current = true
    return () => {
      mounted.current = false
      request.current?.abort()
    }
  }, [])
  const openTask = (next: Task) => {
    trigger.current = document.activeElement as HTMLElement
    setTask(next)
    setError('')
    setMessage('')
    setConfirmBack(false)
  }
  useEffect(() => {
    if (task !== 'main') heading.current?.focus()
  }, [task])
  useEffect(() => {
    if (pending && error && !busy) confirmationError.current?.focus()
  }, [pending, error, busy])
  const resetDraft = (next: IntegrationProfile) => {
    setLegacyDraft(undefined)
    setSelected(null)
    setDiscoveryDraft(false)
    setUrl(next.url)
    setInsecureTls(next.insecureTls)
    setMode(next.mode as CostConnectionDraft['mode'])
    setClusterId(next.clusterId)
    setCredentialDirty(false)
  }
  useEffect(() => {
    const finished = wasBusy.current && !busy
    wasBusy.current = busy
    if (busy || pending) return
    if (restoreTriggerFocus.current) {
      restoreTriggerFocus.current = false
      if (trigger.current?.isConnected) trigger.current.focus()
      else region.current?.focus()
    } else if (finished && document.activeElement === document.body) {
      region.current?.focus()
    }
  }, [busy, pending, task])
  useEffect(() => {
    if (
      !dirty &&
      task === 'main' &&
      profiles[kind].revision !== snapshot[kind].revision
    ) {
      setSnapshot(profiles)
      resetDraft(profiles[kind])
    } else if (
      (Object.keys(profiles) as IntegrationKind[]).some(
        (k) => k !== kind && profiles[k] !== snapshot[k]
      )
    ) {
      setSnapshot({ ...profiles, [kind]: snapshot[kind] })
    }
  }, [profiles, snapshot, kind, dirty, task])
  const leaveTask = () => {
    restoreTriggerFocus.current = true
    setConfirmBack(false)
    setTask('main')
    setPending(null)
    setSelected(null)
    setError('')
    resetDraft(profile)
  }
  const back = () => {
    if (dirty && task === 'replace') {
      setConfirmBack(true)
      return
    }
    leaveTask()
  }
  const fetchConnections = async (
    update?: Update
  ): Promise<ConnectionResponse> => {
    if (request.current)
      throw new Error('A settings request is already in progress.')
    catalogRequest.current?.abort()
    const controller = new AbortController()
    request.current = controller
    const base = getApiBase()
    setBusy(true)
    onBusyChange?.(true)
    try {
      const response = await fetch(apiUrl('/integrations/connections'), {
        method: update ? 'PUT' : 'GET',
        signal: controller.signal,
        credentials: getCredentialsMode(),
        headers: { 'Content-Type': 'application/json', ...getAuthHeaders() },
        ...(update
          ? {
              body: JSON.stringify({
                ...update,
                target: profile.target,
                kind,
                revision: profile.revision,
                ...(update.action === 'reconfirm'
                  ? {
                      revisions: Object.fromEntries(
                        update.kinds!.map((k) => [k, snapshot[k].revision])
                      )
                    }
                  : {}),
                legacyRevision: update.legacyRevision ?? profile.legacy?.revision
              })
            }
          : {})
      })
      const data = (await response.json().catch(() => ({}))) as ConnectionResponse
      if (
        controller.signal.aborted ||
        getApiBase() !== base ||
        !mounted.current
      )
        throw new Error('Cluster changed; reload settings.')
      if (!response.ok)
        throw new Error(
          data.error || `Settings request failed (${response.status})`
        )
      return data
    } finally {
      if (request.current === controller) request.current = null
      if (mounted.current) {
        setBusy(false)
        onBusyChange?.(false)
      }
    }
  }
  const receive = (data: ConnectionResponse) => {
    setSnapshot(data.profiles)
    setCatalog(data.connections)
    onChange(data.profiles)
    resetDraft(data.profiles[kind])
  }
  const save = async (update: Update) => {
    const data = await fetchConnections(update)
    receive(data)
    setConfirmBack(false)
    setTask('main')
    setPending(null)
    setSelected(null)
    setMessageRevision(data.profiles[kind].revision)
    setMessageWarning(!!data.error)
    setMessage(
      data.error ? data.error : data.checked ? 'Connection checked' : 'Saved'
    )
    return data
  }
  const act = async (update: Update) => {
    setError('')
    try {
      await save(update)
    } catch (e) {
      if (mounted.current) setError(e instanceof Error ? e.message : String(e))
    }
  }
  const loadTask = async (next: Task) => {
    setError('')
    try {
      const data = await fetchConnections()
      receive(data)
      setDraftGeneration(generation => generation + 1)
      setPending(null)
      setSelected(null)
      openTask(next)
    } catch (e) {
      if (mounted.current) setError(e instanceof Error ? e.message : String(e))
    }
  }
  const confirm = (update: Update) => {
    trigger.current = document.activeElement as HTMLElement
    setError('')
    setPending(update)
  }
  const cancelConfirmation = () => {
    restoreTriggerFocus.current = true
    setPending(null)
  }
  const confirmPending = async () => {
    if (!pending) return
    setError('')
    try {
      restoreTriggerFocus.current = true
      await save({ ...pending, confirmRemoval: true })
    } catch (e) {
      restoreTriggerFocus.current = false
      if (mounted.current) setError(e instanceof Error ? e.message : String(e))
    }
  }
  const discard = () => {
    resetDraft(profile)
    setDraftGeneration((generation) => generation + 1)
    setError('')
    setMessage('')
    onDirtyChange(false)
  }
  const apply = async (draft: Omit<Update, 'action'>) => {
    const action = selected ? 'copy' : legacyDraft ? 'adopt' : task === 'replace' || discoveryDraft ? 'replace' : 'save'
    const credentialSource = selected ?? legacyDraft ?? profile
    if ((action === 'save' || action === 'copy' || action === 'adopt') && credentialSource.url && draft.url?.trim() === '' && !(kind === 'cost' && draft.mode === 'prometheus')) {
      const keptHeaders = credentialSource.headerKeys.some((key) => !draft.headers?.some((header) =>
        header.key.toLowerCase() === key.toLowerCase() && header.action === 'clear'))
      const keptSecret = credentialSource.secretSet && (!draft.secret || draft.secret.action === 'keep') && !draft.useCliToken
      if (keptHeaders || keptSecret) {
        throw new Error('Remove or replace the saved credentials, or choose Use auto-discovery to clear this connection.')
      }
    }
    const update: Update = automaticDraft && !draft.useCliToken
      ? { action: 'auto' }
      : { ...draft, action, ...(legacyDraft ? { legacyRevision: legacyDraft.revision } : {}), ...(selected ? {
        binding: selected.binding,
        sourceRevision: selected.revision,
      } : {}) }
    if (kind === 'cost' && draft.mode === 'prometheus') {
      update.action = 'auto'
      delete update.url
      delete update.secret
      delete update.clusterId
    }
    const removing =
      (!!profile.url && draft.url?.trim() === '') ||
      (hasSavedConfiguration && (action === 'replace' || action === 'copy')) ||
      (update.action === 'auto' && hasSavedConfiguration)
    if (removing) {
      confirm(update)
      return null
    }
    return save(update)
  }
  const setSecretDirty = useCallback(
    (value: boolean) => setCredentialDirty(value),
    []
  )
  const editor =
    task === 'replace' ||
    (task === 'main' && (!!selected || (
      profile.state !== 'target_changed' &&
      profile.state !== 'error' &&
      profile.state !== 'launch')))
  const replacing = task === 'replace' || discoveryDraft
  const title =
    task === 'reconfirm'
        ? 'Review changed cluster connection'
        : 'Use a different connection'
  const beginReplace = () => {
    resetDraft({
      ...profile,
      url: '',
      insecureTls: false,
      mode: 'auto',
      clusterId: ''
    })
    openTask('replace')
  }
  const autoDiscoveryAction =
    task === 'main' && ['auto', 'saved'].includes(profile.state) ? (
      <button
        type="button"
        disabled={!!pending || automaticDraft || (!dirty && !hasSavedConfiguration && profile.mode === 'auto')}
        onClick={() => {
          resetDraft({ ...profile, url: '', mode: 'auto', insecureTls: false, clusterId: '' })
          setDiscoveryDraft(true)
          setDraftGeneration((generation) => generation + 1)
          setError('')
          requestAnimationFrame(() => region.current?.querySelector<HTMLInputElement | HTMLSelectElement>('input:not(:disabled), select:not(:disabled)')?.focus())
        }}
        className="text-xs text-accent-text hover:underline disabled:opacity-50 shrink-0"
      >
        Use auto-discovery
      </button>
    ) : undefined
  const copyAction = task === 'main' && profile.state !== 'launch' && catalogError ? (
    <span role="status" className="text-xs text-theme-text-secondary">
      Could not load other clusters.{' '}
      <button type="button" className="text-accent-text hover:underline" onClick={() => setCatalogRetry(retry => retry + 1)}>
        Retry
      </button>
    </span>
  ) : task === 'main' && profile.state !== 'launch' && copySources.length > 0 ? (
    <SelectMenu
      variant="text"
      value=""
      placeholder="Copy from another cluster…"
      ariaLabel="Copy from another cluster…"
      searchPlaceholder="Search clusters or URLs"
      disabled={!!pending}
      options={copySources.map(connection => ({
        value: connection.binding,
        label: `${connection.context}${copySources.filter(other => other.context === connection.context).length > 1 ? ` · ${connection.source} · ${connection.inFileName}` : ''}`,
        description: connection.url,
      }))}
      onChange={binding => {
        const source = copySources.find(connection => connection.binding === binding)
        if (!source) return
        setSelected(source)
        setLegacyDraft(undefined)
        setUrl(source.url)
        setInsecureTls(source.insecureTls)
        if (kind === 'cost') {
          setMode('kubecost')
          setClusterId(profile.state === 'target_changed' ? '' : profile.clusterId)
        }
        setDiscoveryDraft(false)
        setCredentialDirty(false)
        setDraftGeneration(generation => generation + 1)
        setMessage('')
        setError('')
        requestAnimationFrame(() => editorRegion.current?.querySelector<HTMLInputElement>('input:not(:disabled)')?.focus())
      }}
    />
  ) : undefined
  const connectionActions = (autoDiscoveryAction || copyAction) && (
    <div className="flex flex-wrap items-center gap-x-4 gap-y-2 pt-2">
      {autoDiscoveryAction}
      {copyAction}
    </div>
  )
  const resetToDiscovery =
    pending?.action === 'auto' && pending.mode !== 'prometheus'
  const useMetricsForCost =
    pending?.action === 'auto' && pending.mode === 'prometheus'
  const confirmationTitle = resetToDiscovery
    ? 'Use auto-discovery?'
    : useMetricsForCost
      ? 'Use metrics for cost data?'
      : 'Replace saved connection?'
  const confirmationLabel = resetToDiscovery
    ? 'Save & use auto-discovery'
    : useMetricsForCost
      ? 'Use metrics connection'
      : 'Replace connection'
  const confirmationMessage = resetToDiscovery
    ? `Remove the saved ${names[kind]} connection and credentials for ${removalContext || 'this cluster'} and discover a backend automatically.${kind === 'cost' ? ' The saved Kubecost cluster mapping is also removed.' : ''} Other clusters are unchanged.`
    : useMetricsForCost
      ? `Remove the saved Kubecost connection, credentials and cluster mapping for ${removalContext || 'this cluster'} and use its metrics connection for cost data. Other clusters are unchanged.`
      : `Replace the saved ${names[kind]} connection for ${removalContext || 'this cluster'} with your changes? Other clusters are unchanged.`
  if (pending)
    confirmationCopy.current = {
      title: confirmationTitle,
      message: confirmationMessage,
      label: confirmationLabel
    }
  const showFeedback = discoveryDraft ? profile.secretSet || profile.headerKeys.length > 0 : !!(
    message &&
    task === 'main' &&
    messageRevision === profile.revision &&
    (messageWarning || !dirty)
  )
  const feedback: ConnectionFeedback | undefined = showFeedback ? {
    tone: discoveryDraft ? 'info' : messageWarning ? 'warning' : 'success',
    message: discoveryDraft
          ? 'Saving clears the saved credentials for this cluster.'
          : message,
  } : undefined
  return (
    <fieldset
      ref={region}
      tabIndex={-1}
      aria-label={`${names[kind]} connection settings`}
      disabled={busy}
      className="min-w-0 space-y-4 outline-none"
      onKeyDownCapture={(event) => {
        if (pending) trapConnectionConfirmationFocus(event)
      }}
      onKeyDown={(event) => {
        if (event.key === 'Escape' && task !== 'main' && !pending) {
          event.preventDefault()
          event.stopPropagation()
          if (!busy) {
            if (confirmBack) {
              setConfirmBack(false)
              heading.current?.focus()
            } else back()
          }
        }
      }}
    >
      {task !== 'main' && (
        <div className="space-y-3 border-b border-theme-border pb-3">
          <button
            type="button"
            onClick={back}
            className="flex items-center gap-1 text-xs text-accent-text"
          >
            <ArrowLeft className="h-3 w-3" />
            Back to {names[kind].toLowerCase()}
          </button>
          <h4
            ref={heading}
            tabIndex={-1}
            className="text-base font-semibold text-theme-text-primary outline-none"
          >
            {title}
          </h4>
        </div>
      )}
      {confirmBack && (
        <div role="alert" className="card-inner-lg space-y-3">
          <p className="text-sm">Discard your unapplied connection changes?</p>
          <div className="flex gap-3 text-xs">
            <button
              type="button"
              autoFocus
              className="btn-brand px-3 py-2"
              onClick={() => {
                setConfirmBack(false)
                heading.current?.focus()
              }}
            >
              Keep editing
            </button>
            <button
              type="button"
              className="text-theme-text-secondary hover:underline"
              onClick={leaveTask}
            >
              Discard draft
            </button>
          </div>
        </div>
      )}
      <div className="flex flex-wrap items-center gap-x-3 gap-y-1">
        <p className="min-w-0 break-words text-xs text-theme-text-secondary">
          Cluster:{' '}
          <span className="font-medium text-theme-text-primary">
            {profile.target.context || 'Not selected'}
          </span>
        </p>
        {profile.target.source && (
          <Tooltip content={<span className="block max-w-sm break-words">Settings belong to this kubeconfig entry: {profile.target.source} · {profile.target.inFileName}. Renaming or moving it creates a separate entry.</span>}>
            <button type="button" aria-label="Cluster settings identity" className="text-theme-text-tertiary hover:text-theme-text-primary">
              <Info className="h-3.5 w-3.5" />
            </button>
          </Tooltip>
        )}
      {task === 'main' && status}
      </div>
      {!editor && connectionActions}
      {selected && <div className="text-xs text-theme-text-secondary space-y-1">
        <p>Copied from <span className="font-medium">{selected.context}</span> · Unsaved</p>
        <p>{selected.headerKeys.length > 0 || selected.secretSet ? 'Saved credentials will be copied when you save. ' : ''}Changes stay independent. Check that this backend serves this cluster.</p>
        {selected.envHeaderKeys.length > 0 && <p>Environment-backed headers keep their references; changing those variables affects both clusters.</p>}
      </div>}
      {task === 'main' && (
        <>
          {profile.state === 'launch' ? (
            <div className="card-inner-lg space-y-2">
              <Badge tone="note">Set for this launch</Badge>
              <p className="text-sm break-all">
                {profile.url || 'Auto-discovery'}
              </p>
              <p className="text-xs text-theme-text-secondary">
                This override stays with this context. Restart without its
                startup flags or environment configuration to edit saved
                settings.
              </p>
              {profile.error && (
                <p role="alert" className="text-xs text-warning-text">
                  {profile.error}
                </p>
              )}
            </div>
          ) : !selected && (profile.state === 'target_changed' ||
            profile.state === 'error') ? (
            <div className="card-inner-lg space-y-3">
              <Badge severity="warning">
                {profile.state === 'target_changed'
                  ? 'Cluster connection changed'
                  : 'Settings unavailable'}
              </Badge>
              <p className="text-sm text-theme-text-secondary">
                {profile.error}
              </p>
              {profile.state === 'target_changed' && (
                <button
                  type="button"
                  className="btn-brand px-3 py-2 text-xs"
                  onClick={() => {
                    setAccepted([kind])
                    openTask('reconfirm')
                  }}
                >
                  Review changes
                </button>
              )}
              {profile.state === 'error' && !error && (
                <button
                  type="button"
                  className="text-xs text-accent-text hover:underline"
                  onClick={() => void loadTask('main')}
                >
                  Reload latest settings
                </button>
              )}
              {profile.url && (
                <button
                  type="button"
                  className="block text-xs text-accent-text"
                  onClick={beginReplace}
                >
                  Use a different connection
                </button>
              )}
            </div>
          ) : null}
          {profile.state === 'auto' && profile.legacy && !legacyDraft && (
            <div className="card-inner-lg space-y-2">
              <h4 className="text-sm font-medium">
                Previously saved connection
              </h4>
              <p className="text-xs text-theme-text-secondary">
                Use these older settings here only if this backend serves{' '}
                {profile.target.context}.
              </p>
              <p className="text-xs break-all">
                {profile.legacy.url || 'Cluster discovery'}
              </p>
              {profile.legacy.error && (
                <p className="text-xs text-warning-text">
                  {profile.legacy.error}
                </p>
              )}
              <div className="flex flex-wrap gap-3">
                <button
                  type="button"
                  disabled={!!profile.legacy.error}
                  className="text-xs text-accent-text disabled:opacity-50 disabled:cursor-not-allowed"
                  onClick={() => {
                    const legacy = profile.legacy!
                    setSelected(null)
                    setLegacyDraft(legacy)
                    setUrl(legacy.url)
                    setInsecureTls(legacy.insecureTls)
                    setMode(legacy.mode as CostConnectionDraft['mode'])
                    setClusterId(legacy.clusterId)
                    setDiscoveryDraft(false)
                    setCredentialDirty(false)
                    setDraftGeneration(generation => generation + 1)
                    setMessage('')
                    setError('')
                    requestAnimationFrame(() => editorRegion.current?.querySelector<HTMLInputElement>('input:not(:disabled)')?.focus())
                  }}
                >
                  Use previous settings
                </button>
                <button
                  type="button"
                  className="text-xs text-theme-text-secondary"
                  onClick={() => void act({ action: 'dismiss_legacy' })}
                >
                  Dismiss for this cluster
                </button>
              </div>
            </div>
          )}
        </>
      )}
      <fieldset ref={editorRegion} disabled={!!pending} className="min-w-0">
        {editor &&
          (kind === 'metrics' ? (
            <PrometheusConnectionForm
              key={`${profile.revision}:${task}:${draftGeneration}`}
              local
              value={url}
              onChange={setUrl}
              configuredHeaderKeys={selected ? selected.headerKeys : legacyDraft ? legacyDraft.headerKeys : replacing ? [] : profile.headerKeys}
              environmentHeaderKeys={selected ? selected.envHeaderKeys : legacyDraft ? legacyDraft.envHeaderKeys : replacing ? [] : profile.envHeaderKeys}
              serverManaged={false}
              headersManaged={false}
              urlFromFlag={false}
              onDirtyChange={setSecretDirty}
              dirty={dirty}
              onDiscard={discard}
              feedback={feedback}
              connectionAction={connectionActions}
              onApply={async () => {
                throw new Error('Header operations required')
              }}
              onApplyOperations={async (url, headers) => {
                const result = await apply({ url, headers })
                return result
                  ? { connected: result.connected, error: result.error }
                  : null
              }}
            />
          ) : kind === 'argocd' ? (
            <ArgoCDConnectionForm
              key={`${profile.revision}:${task}:${draftGeneration}`}
              dirty={dirty}
              onDiscard={discard}
              feedback={feedback}
              connectionAction={connectionActions}
              value={{ url, insecureTls }}
              secretSet={selected ? selected.secretSet : legacyDraft ? legacyDraft.secretSet : !replacing && profile.secretSet}
              cliSession={cliSession}
              onChange={(draft: ArgoConnectionDraft) => {
                setUrl(draft.url)
                setInsecureTls(draft.insecureTls)
              }}
              onDirtyChange={setSecretDirty}
              onApply={async (draft) => {
                return !!(await apply(draft))
              }}
            />
          ) : (
            <CostConnectionForm
              key={`${profile.revision}:${task}:${draftGeneration}`}
              dirty={dirty}
              onDiscard={discard}
              feedback={feedback}
              connectionAction={connectionActions}
              value={{ url, mode, clusterId }}
              secretSet={selected ? selected.secretSet : legacyDraft ? legacyDraft.secretSet : !replacing && profile.secretSet}
              onChange={(draft) => {
                setUrl(draft.url)
                setMode(draft.mode)
                setClusterId(draft.clusterId)
              }}
              onDirtyChange={setSecretDirty}
              onApply={async (draft) => {
                return !!(await apply(draft))
              }}
            />
          ))}
      </fieldset>
      {!editor && <Collapse open={showFeedback} className="!mt-0">
        <p role="status" className={`pt-2 text-xs ${messageWarning ? 'text-warning-text' : 'text-theme-text-secondary'}`}>{feedback?.message}</p>
      </Collapse>}
      <ConfirmDialog
        open={!!pending}
        onClose={cancelConfirmation}
        onConfirm={() => void confirmPending()}
        title={confirmationCopy.current.title}
        message={confirmationCopy.current.message}
        confirmLabel={confirmationCopy.current.label}
        variant="warning"
        showWarning={false}
        isLoading={busy}
      >
        {error && pending ? (
          <p
            ref={confirmationError}
            tabIndex={-1}
            role="alert"
            className="text-sm text-warning-text outline-none"
          >
            {error}
          </p>
        ) : undefined}
      </ConfirmDialog>
      {task === 'reconfirm' && (
        <div className="space-y-4">
          <p className="text-sm text-theme-text-secondary">
            This kubeconfig entry now identifies a different server, trust
            configuration, proxy or user reference. Choose which saved
            integrations may be used with it.
          </p>
          <dl className="grid grid-cols-[auto_1fr] gap-x-3 gap-y-2 text-xs">
            <dt className="text-theme-text-tertiary">Previous server</dt>
            <dd className="break-all">
              {profile.previousIdentity?.server || 'Not recorded'}
            </dd>
            <dt className="text-theme-text-tertiary">Current server</dt>
            <dd className="break-all">{profile.target.identity.server}</dd>
            <dt className="text-theme-text-tertiary">User reference</dt>
            <dd>
              {profile.previousIdentity?.user || '—'} →{' '}
              {profile.target.identity.user || '—'}
            </dd>
            <dt className="text-theme-text-tertiary">Trust / proxy</dt>
            <dd>
              {profile.previousIdentity?.trust !== profile.target.identity.trust
                ? 'CA trust changed. '
                : ''}
              {profile.previousIdentity?.proxy !== profile.target.identity.proxy
                ? 'Proxy changed. '
                : ''}
              {profile.previousIdentity?.tlsName !==
                profile.target.identity.tlsName ||
              profile.previousIdentity?.insecureTls !==
                profile.target.identity.insecureTls
                ? 'TLS settings changed.'
                : ''}
            </dd>
          </dl>
          {(Object.keys(snapshot) as IntegrationKind[])
            .filter((k) => snapshot[k].state === 'target_changed')
            .map((k) => (
              <label key={k} className="flex items-center gap-2 text-sm">
                <input
                  type="checkbox"
                  checked={accepted.includes(k)}
                  onChange={(e) =>
                    setAccepted(
                      e.target.checked
                        ? [...accepted, k]
                        : accepted.filter((item) => item !== k)
                    )
                  }
                />
                {names[k]} · {snapshot[k].url || 'Cluster discovery'}
              </label>
            ))}
          <button
            type="button"
            disabled={!acceptedChanges.length}
            className="btn-brand px-3 py-2 text-xs"
            onClick={() =>
              void act({ action: 'reconfirm', kinds: acceptedChanges })
            }
          >
            Use selected connections for this target
          </button>
        </div>
      )}
      {error && !pending && (
        <p role="alert" className="text-sm text-warning-text">
          {error}{' '}
          <button
            type="button"
            onClick={() => void loadTask('main')}
            className="underline"
          >
            {dirty ? 'Discard draft & reload' : 'Reload latest settings'}
          </button>
        </p>
      )}
    </fieldset>
  )
}
