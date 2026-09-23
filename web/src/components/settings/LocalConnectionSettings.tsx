import { useCallback, useEffect, useRef, useState } from 'react'
import { Badge, Disclosure, Input } from '@skyhook-io/k8s-ui'
import { ArrowLeft, Check } from 'lucide-react'
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
interface Usage {
  revision: string
  binding: string
  integration: IntegrationKind
  context: string
  source: string
  inFileName: string
  availability: 'available' | 'removed' | 'unavailable'
}
interface SavedConnection {
  id: string
  type: IntegrationKind
  name: string
  customName: string
  url: string
  headerKeys: string[]
  envHeaderKeys: string[]
  secretSet: boolean
  insecureTls: boolean
  uses: Usage[]
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
  connection?: SavedConnection
  previousIdentity?: TargetIdentity
  error?: string
  legacy?: {
    url: string
    headerKeys: string[]
    secretSet: boolean
    revision: string
    error?: string
  }
}
export type IntegrationProfiles = Record<IntegrationKind, IntegrationProfile>
interface ConnectionResponse {
  profiles: IntegrationProfiles
  connections: SavedConnection[]
  unlinkedAssignments: Usage[]
  revision: string
  connected: boolean
  checked: boolean
  error?: string
}
interface Update {
  action: string
  connectionId?: string
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
type Task = 'main' | 'copy' | 'cleanup' | 'confirm' | 'reconfirm' | 'replace'
const names: Record<IntegrationKind, string> = {
  metrics: 'Metrics',
  argocd: 'Argo CD',
  cost: 'Cost'
}

export function LocalConnectionSettings({
  kind,
  profiles,
  cliSession,
  onChange,
  onDirtyChange,
  onBusyChange
}: {
  kind: IntegrationKind
  profiles: IntegrationProfiles
  cliSession?: { server: string; user: string; insecure?: boolean }
  onChange: (profiles: IntegrationProfiles) => void
  onDirtyChange: (dirty: boolean) => void
  onBusyChange?: (busy: boolean) => void
}) {
  const [snapshot, setSnapshot] = useState(profiles)
  const profile = snapshot[kind]
  const [catalog, setCatalog] = useState<SavedConnection[]>([])
  const [unlinked, setUnlinked] = useState<Usage[]>([])
  const [task, setTask] = useState<Task>('main')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
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
  const [selected, setSelected] = useState<
    (SavedConnection & { source: Usage }) | null
  >(null)
  const [pending, setPending] = useState<Update | null>(null)
  const [confirmBack, setConfirmBack] = useState(false)
  const [accepted, setAccepted] = useState<IntegrationKind[]>([])
  const acceptedChanges = accepted.filter(
    (k) => snapshot[k].state === 'target_changed'
  )
  const request = useRef<AbortController | null>(null)
  const heading = useRef<HTMLHeadingElement>(null)
  const region = useRef<HTMLFieldSetElement>(null)
  const trigger = useRef<HTMLElement | null>(null)
  const mounted = useRef(true)
  const copySources = catalog
    .filter((c) => c.type === kind)
    .flatMap((c) =>
      c.uses
        .filter((use) => use.binding !== profile.target.binding)
        .map((source) => ({ ...c, source }))
    )
  const storedContexts = [
    ...catalog.filter((c) => c.type === kind).flatMap((c) => c.uses),
    ...unlinked.filter((u) => u.integration === kind)
  ]
  const removalContext = pending?.binding
    ? storedContexts.find((use) => use.binding === pending.binding)?.context
    : profile.target.context
  const dirty =
    url !== profile.url ||
    insecureTls !== profile.insecureTls ||
    mode !== profile.mode ||
    clusterId !== profile.clusterId ||
    credentialDirty ||
    !!pending
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
  const resetDraft = (next: IntegrationProfile) => {
    setUrl(next.url)
    setInsecureTls(next.insecureTls)
    setMode(next.mode as CostConnectionDraft['mode'])
    setClusterId(next.clusterId)
    setCredentialDirty(false)
  }
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
    setConfirmBack(false)
    setTask('main')
    setPending(null)
    setSelected(null)
    setError('')
    resetDraft(profile)
    requestAnimationFrame(() => {
      if (trigger.current?.isConnected) trigger.current.focus()
      else region.current?.focus()
    })
  }
  const back = () => {
    if (dirty && ['replace', 'copy'].includes(task)) {
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
                legacyRevision: profile.legacy?.revision
              })
            }
          : {})
      })
      const data = (await response.json()) as ConnectionResponse
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
    setUnlinked(data.unlinkedAssignments)
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
      setPending(null)
      setSelected(null)
      if (next === 'copy' && data.profiles[kind].state === 'target_changed')
        setClusterId('')
      openTask(next)
    } catch (e) {
      if (mounted.current) setError(e instanceof Error ? e.message : String(e))
    }
  }
  const confirm = (update: Update) => {
    setPending(update)
    openTask('confirm')
  }
  const apply = async (draft: Omit<Update, 'action'>) => {
    const action = task === 'replace' ? 'replace' : 'save'
    const update: Update = { ...draft, action }
    if (kind === 'cost' && draft.mode === 'prometheus') {
      update.action = 'auto'
      delete update.url
      delete update.secret
      delete update.clusterId
    }
    const removing =
      !!profile.connection &&
      (draft.url?.trim() === '' ||
        update.action === 'auto' ||
        action === 'replace')
    if (removing) {
      confirm(update)
      throw new Error('Confirm replacing this cluster’s saved connection.')
    }
    return save(update)
  }
  const setSecretDirty = useCallback(
    (value: boolean) => setCredentialDirty(value),
    []
  )
  const editor =
    task === 'replace' ||
    (task === 'main' &&
      profile.state !== 'target_changed' &&
      profile.state !== 'error' &&
      profile.state !== 'launch')
  const replacing = task === 'replace'
  const title =
    task === 'copy'
      ? 'Copy from another cluster'
      : task === 'cleanup'
        ? 'Stored cluster settings'
        : task === 'confirm'
          ? 'Remove saved settings?'
          : task === 'reconfirm'
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
  const feedback =
    message &&
    task === 'main' &&
    messageRevision === profile.revision &&
    (messageWarning || !dirty) ? (
      <p
        role="status"
        className={`flex items-start gap-1.5 text-xs ${messageWarning ? 'text-warning-text' : 'text-theme-text-secondary'}`}
      >
        {!messageWarning && (
          <Check
            aria-hidden="true"
            className="mt-0.5 h-3 w-3 shrink-0 text-[var(--color-success-dark)] dark:text-[var(--color-success-light)]"
          />
        )}
        {message}
      </p>
    ) : null
  return (
    <fieldset
      ref={region}
      tabIndex={-1}
      aria-label={`${names[kind]} connection settings`}
      disabled={busy}
      className="min-w-0 space-y-4 outline-none"
      onKeyDown={(event) => {
        if (event.key === 'Escape' && task !== 'main') {
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
      <p className="min-w-0 break-words text-xs text-theme-text-secondary">
        Cluster:{' '}
        <span className="font-medium text-theme-text-primary">
          {profile.target.context || 'Not selected'}
        </span>
      </p>
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
          ) : profile.state === 'target_changed' ||
            profile.state === 'error' ? (
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
              {profile.connection && (
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
          {profile.legacy && (
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
                  className="text-xs text-accent-text"
                  onClick={() => void act({ action: 'adopt' })}
                >
                  Use for this cluster
                </button>
                <button
                  type="button"
                  className="text-xs text-theme-text-secondary"
                  onClick={() => void act({ action: 'dismiss_legacy' })}
                >
                  Stop offering these older settings
                </button>
              </div>
            </div>
          )}
        </>
      )}
      {editor &&
        (kind === 'metrics' ? (
          <PrometheusConnectionForm
            key={`${profile.revision}:${task}`}
            local
            value={url}
            onChange={setUrl}
            configuredHeaderKeys={replacing ? [] : profile.headerKeys}
            environmentHeaderKeys={replacing ? [] : profile.envHeaderKeys}
            serverManaged={false}
            headersManaged={false}
            urlFromFlag={false}
            onDirtyChange={setSecretDirty}
            applyLabel="Apply now"
            scopeDescription="Applies only to this cluster. No restart needed."
            onApply={async () => {
              throw new Error('Header operations required')
            }}
            onApplyOperations={async (url, headers) => {
              const result = await apply({ url, headers })
              return { connected: result.connected, error: result.error }
            }}
          />
        ) : kind === 'argocd' ? (
          <ArgoCDConnectionForm
            key={`${profile.revision}:${task}`}
            value={{ url, insecureTls }}
            secretSet={!replacing && profile.secretSet}
            cliSession={cliSession}
            onChange={(draft: ArgoConnectionDraft) => {
              setUrl(draft.url)
              setInsecureTls(draft.insecureTls)
            }}
            onDirtyChange={setSecretDirty}
            onApply={async (draft) => {
              await apply(draft)
            }}
            applyLabel="Test & apply"
          />
        ) : (
          <CostConnectionForm
            key={`${profile.revision}:${task}`}
            value={{ url, mode, clusterId }}
            secretSet={!replacing && profile.secretSet}
            onChange={(draft) => {
              setUrl(draft.url)
              setMode(draft.mode)
              setClusterId(draft.clusterId)
            }}
            onDirtyChange={setSecretDirty}
            onApply={async (draft) => {
              await apply(draft)
            }}
            applyLabel="Test & apply"
          />
        ))}
      {feedback}
      {task === 'copy' && (
        <div className="space-y-3">
          <p className="text-sm text-theme-text-secondary">
            Copy a saved endpoint and its credentials to{' '}
            {profile.target.context}. Later edits stay independent. Credentials
            never appear in your browser. Only copy an endpoint that serves this
            cluster too; a connection check does not verify its data coverage.
          </p>
          {copySources.map((connection) => (
            <button
              type="button"
              key={connection.source.binding}
              disabled={!!connection.error}
              onClick={() => setSelected(connection)}
              className={`block w-full rounded-lg border p-3 text-left ${selected?.source.binding === connection.source.binding ? 'border-accent bg-accent-muted' : 'border-theme-border hover:bg-theme-hover'}`}
            >
              <span className="block text-sm font-medium">
                {connection.source.context}
              </span>
              <span className="block text-xs text-theme-text-secondary break-all">
                {connection.url}
              </span>
              <span className="block text-xs text-theme-text-tertiary break-all">
                {connection.source.source} · {connection.source.inFileName}
                {connection.source.availability !== 'available'
                  ? ' · Not loaded in this session'
                  : ''}
              </span>
              {connection.error && (
                <span className="text-xs text-warning-text">
                  {connection.error}
                </span>
              )}
            </button>
          ))}
          {!copySources.length && (
            <p className="text-sm text-theme-text-tertiary">
              No other clusters have a saved endpoint for this integration.
              Auto-discovery settings cannot be copied.
            </p>
          )}
          {selected && (
            <div className="card-inner-lg space-y-3">
              <p className="text-xs text-theme-text-secondary">
                {kind === 'metrics'
                  ? selected.headerKeys.length
                    ? `Includes headers: ${selected.headerKeys.join(', ')}.`
                    : 'No authentication headers.'
                  : selected.secretSet
                    ? 'Includes the saved credential.'
                    : 'No saved credential.'}
                {kind === 'argocd' && selected.insecureTls
                  ? ' TLS verification is disabled.'
                  : ''}
              </p>
              {selected.envHeaderKeys.length > 0 && (
                <p className="text-xs text-theme-text-secondary">
                  Environment-backed headers keep their environment references;
                  changes to those variables can affect both clusters.
                </p>
              )}
              {kind === 'cost' && (
                <label className="block text-sm space-y-1">
                  This cluster’s Kubecost cluster ID
                  <Input
                    value={clusterId}
                    onChange={(e) => setClusterId(e.target.value)}
                    placeholder="Auto-detect CLUSTER_ID"
                  />
                  <span className="block text-xs text-theme-text-tertiary">
                    The source cluster’s mapping is not copied.
                  </span>
                </label>
              )}
              {(profile.connection ||
                profile.secretSet ||
                profile.clusterId) && (
                <p className="text-xs text-theme-text-secondary">
                  Replaces this cluster’s saved connection and credentials. The
                  source cluster is unchanged.
                </p>
              )}
              <button
                type="button"
                className="btn-brand px-3 py-2 text-xs"
                onClick={() =>
                  void act({
                    action: 'copy',
                    binding: selected.source.binding,
                    sourceRevision: selected.source.revision,
                    connectionId: selected.id,
                    confirmRemoval: !!(
                      profile.connection ||
                      profile.secretSet ||
                      profile.clusterId
                    ),
                    ...(kind === 'cost' ? { clusterId } : {})
                  })
                }
              >
                Copy & apply
              </button>
            </div>
          )}
        </div>
      )}
      {task === 'cleanup' && (
        <div className="space-y-4">
          <p className="text-xs text-theme-text-secondary">
            Removing a kubeconfig entry does not delete its saved settings.
            Forget settings here when you no longer need them.
          </p>
          <ConnectionUses
            uses={storedContexts}
            currentBinding={profile.target.binding}
            onForget={(use) =>
              confirm({
                action: 'forget',
                binding: use.binding,
                sourceRevision: use.revision
              })
            }
          />
          {!storedContexts.length && (
            <p className="text-sm text-theme-text-tertiary">
              No stored cluster settings.
            </p>
          )}
        </div>
      )}
      {task === 'confirm' && pending && (
        <div className="space-y-4">
          <p className="text-sm text-theme-text-secondary">
            {['replace', 'save'].includes(pending.action)
              ? `Replace the saved connection for ${removalContext || 'this cluster'}?`
              : `Remove the saved settings and credentials for ${removalContext || 'this cluster'}?`}{' '}
            Other clusters are unchanged.
          </p>
          <p className="text-xs text-theme-text-tertiary">
            Nothing is deleted from Kubernetes or the backend.
          </p>
          <div className="flex gap-3">
            <button
              type="button"
              className="btn-brand px-3 py-2 text-xs"
              onClick={() => void act({ ...pending, confirmRemoval: true })}
            >
              {['replace', 'save'].includes(pending.action)
                ? 'Replace connection'
                : 'Remove saved settings'}
            </button>
            <button
              type="button"
              className="text-xs text-theme-text-secondary"
              onClick={back}
            >
              Cancel
            </button>
          </div>
        </div>
      )}
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
      {task === 'main' && profile.state !== 'launch' && (
        <div className="flex flex-wrap gap-4 border-t border-theme-border pt-4 text-xs">
          <button
            type="button"
            disabled={dirty}
            onClick={() => void loadTask('copy')}
            className="text-accent-text hover:underline disabled:opacity-50"
          >
            Copy from another cluster…
          </button>
          {profile.state === 'saved' && (
            <button
              type="button"
              disabled={dirty}
              onClick={() => confirm({ action: 'auto' })}
              className="text-theme-text-secondary hover:underline disabled:opacity-50"
            >
              Use auto-discovery
            </button>
          )}
        </div>
      )}
      {error && (
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
      {task === 'main' && (
        <Disclosure
          summary="Storage and cluster identity"
          className="border-t border-theme-border pt-4 text-xs text-theme-text-tertiary"
          summaryClassName="text-sm font-medium text-theme-text-primary"
        >
          <div className="space-y-2 pt-2">
            <p>
              Stored in ~/.radar/clusters.json, shared by CLI and Desktop.
              Credentials are permission-protected plaintext, not encrypted.
              Changes made by another process apply on the next integration
              operation.
            </p>
            <p className="break-all">
              Source: {profile.target.source} · {profile.target.inFileName}
            </p>
            <p>
              Renamed or moved a kubeconfig entry? Copy from the old cluster
              entry, then remove its stored settings. Missing contexts are never
              removed automatically.
            </p>
            <button
              type="button"
              disabled={dirty}
              className="text-accent-text hover:underline disabled:opacity-50"
              onClick={() => void loadTask('cleanup')}
            >
              Manage stored cluster settings
            </button>
          </div>
        </Disclosure>
      )}
    </fieldset>
  )
}

function ConnectionUses({
  uses,
  onForget,
  currentBinding
}: {
  uses: Usage[]
  onForget: (use: Usage) => void
  currentBinding: string
}) {
  return (
    <div className="space-y-3 text-xs text-theme-text-secondary">
      {uses.map((use) => (
        <div key={use.binding} className="flex justify-between gap-3">
          <div className="min-w-0">
            <span className="font-medium break-words">{use.context}</span>
            {use.binding === currentBinding && <span> (current)</span>}
            {use.availability === 'removed' && (
              <span> · removed from kubeconfig</span>
            )}
            {use.availability === 'unavailable' && (
              <span> · not loaded in this session</span>
            )}
            <p className="text-theme-text-tertiary break-all">
              {use.source} · {use.inFileName}
            </p>
          </div>
          <button
            type="button"
            className="text-accent-text self-start shrink-0 hover:underline"
            onClick={() => onForget(use)}
          >
            Forget settings
          </button>
        </div>
      ))}
    </div>
  )
}
