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
  affected: number
  error?: string
}
interface Update {
  action: string
  connectionId?: string
  binding?: string
  name?: string
  url?: string
  headers?: HeaderOperation[]
  secret?: SecretEdit
  insecureTls?: boolean
  mode?: string
  clusterId?: string
  keepUnused?: boolean
  confirmRemoval?: boolean
  kinds?: IntegrationKind[]
  useCliToken?: boolean
}
type Task =
  | 'main'
  | 'use'
  | 'manage'
  | 'rename'
  | 'confirm'
  | 'reconfirm'
  | 'shared'
  | 'fork'
  | 'replace'
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
  const [selected, setSelected] = useState<SavedConnection | null>(null)
  const [rename, setRename] = useState('')
  const [pending, setPending] = useState<Update | null>(null)
  const [keepUnused, setKeepUnused] = useState(false)
  const [confirmBack, setConfirmBack] = useState(false)
  const [accepted, setAccepted] = useState<IntegrationKind[]>([])
  const acceptedChanges = accepted.filter(k => snapshot[k].state === 'target_changed')
  const request = useRef<AbortController | null>(null)
  const heading = useRef<HTMLHeadingElement>(null)
  const region = useRef<HTMLFieldSetElement>(null)
  const trigger = useRef<HTMLElement | null>(null)
  const mounted = useRef(true)
  const shared = (profile.connection?.uses.length ?? 0) > 1
  const sharedSummary = shared && profile.state === 'saved'
  const removalConnection =
    pending?.action === 'delete'
      ? catalog.find((connection) => connection.id === pending.connectionId)
      : pending?.action === 'forget'
        ? catalog.find((connection) =>
            connection.uses.some((use) => use.binding === pending.binding)
          )
        : profile.connection
  const removalContext = pending?.binding
    ? [...catalog.flatMap((connection) => connection.uses), ...unlinked].find(
        (use) => use.binding === pending.binding
      )?.context
    : profile.target.context
  const dirty =
    url !== profile.url ||
    insecureTls !== profile.insecureTls ||
    mode !== profile.mode ||
    clusterId !== profile.clusterId ||
    credentialDirty ||
    !!pending ||
    (task === 'rename' && rename !== selected?.customName)
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
    } else if ((Object.keys(profiles) as IntegrationKind[]).some(k => k !== kind && profiles[k] !== snapshot[k])) {
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
    if (dirty && ['shared', 'fork', 'replace', 'rename'].includes(task)) {
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
                ...(update.action === 'reconfirm' ? { revisions: Object.fromEntries(update.kinds!.map(k => [k, snapshot[k].revision])) } : {}),
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
      if (next === 'use' && data.profiles[kind].state === 'target_changed')
        setClusterId('')
      openTask(next)
    } catch (e) {
      if (mounted.current) setError(e instanceof Error ? e.message : String(e))
    }
  }
  const confirm = (update: Update) => {
    setPending(update)
    setKeepUnused(false)
    openTask('confirm')
  }
  const apply = async (draft: Omit<Update, 'action'>) => {
    const action =
      task === 'shared'
        ? 'update_shared'
        : task === 'fork'
          ? 'fork'
          : task === 'replace'
            ? 'replace'
            : 'save'
    const update: Update = { ...draft, action }
    if (kind === 'cost' && draft.mode === 'prometheus') {
      update.action = 'auto'
      delete update.url
      delete update.secret
      delete update.clusterId
    }
    const removing =
      !!profile.connection &&
      ((draft.url?.trim() === '' && action !== 'update_shared') ||
        update.action === 'auto' ||
        action === 'replace')
    if (removing && profile.connection!.uses.length === 1) {
      confirm(update)
      throw new Error('Confirm what to do with the previous connection.')
    }
    return save(update)
  }
  const setSecretDirty = useCallback(
    (value: boolean) => setCredentialDirty(value),
    []
  )
  const editor =
    task === 'shared' ||
    task === 'fork' ||
    task === 'replace' ||
    (task === 'main' &&
      !shared &&
      profile.state !== 'target_changed' &&
      profile.state !== 'error' &&
      profile.state !== 'launch')
  const replacing = task === 'replace'
  const title =
    task === 'use'
      ? 'Use saved connection'
      : task === 'manage'
        ? 'Manage saved connections'
        : task === 'rename'
          ? 'Rename connection'
          : task === 'confirm'
            ? 'Remove saved settings?'
            : task === 'reconfirm'
              ? 'Review changed cluster connection'
              : task === 'shared'
                ? 'Edit shared connection'
                : task === 'fork'
                  ? 'Customize for this cluster'
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
          ) : sharedSummary ? (
            <div className="space-y-4">
              <div className="flex flex-wrap items-center justify-between gap-3">
                <h4 className="text-sm font-medium text-theme-text-primary">
                  {profile.connection!.name}
                </h4>
                <button
                  type="button"
                  className="btn-brand px-3 py-2 text-xs"
                  onClick={() => openTask('shared')}
                >
                  Edit shared connection
                </button>
              </div>
              <dl className="grid grid-cols-[auto_minmax(0,1fr)] items-baseline gap-x-4 gap-y-2 text-xs">
                <dt className="text-theme-text-tertiary">Endpoint</dt>
                <dd className="break-all text-theme-text-primary">
                  {profile.url}
                </dd>
                <dt className="text-theme-text-tertiary">Authentication</dt>
                <dd className="min-w-0 break-words text-theme-text-secondary">
                  {kind === 'metrics'
                    ? profile.headerKeys.length
                      ? profile.headerKeys
                          .map((key) =>
                            profile.envHeaderKeys.includes(key)
                              ? `${key} (environment)`
                              : key
                          )
                          .join(', ')
                      : 'No headers configured'
                    : kind === 'argocd'
                      ? profile.secretSet
                        ? 'Token configured'
                        : 'No token configured'
                      : profile.secretSet
                        ? 'API key configured'
                        : 'No API key configured'}
                </dd>
                {kind === 'argocd' && (
                  <>
                    <dt className="text-theme-text-tertiary">
                      TLS verification
                    </dt>
                    <dd
                      className={
                        profile.insecureTls
                          ? 'text-warning-text'
                          : 'text-theme-text-secondary'
                      }
                    >
                      {profile.insecureTls ? 'Off' : 'On'}
                    </dd>
                  </>
                )}
              </dl>
              {feedback}
              <div className="space-y-2">
                <p className="text-xs font-medium text-theme-text-secondary">
                  Shared by {profile.connection!.uses.length} contexts
                </p>
                <ConnectionUses
                  uses={profile.connection!.uses}
                  currentBinding={profile.target.binding}
                  compact
                />
              </div>
              <div>
                <button
                  type="button"
                  className="break-words text-left text-xs text-accent-text hover:underline"
                  onClick={() => openTask('fork')}
                >
                  Customize for {profile.target.context}
                </button>
              </div>
              {kind === 'cost' && (
                <Disclosure summary="This context's cluster mapping">
                  <div className="space-y-2 pt-2">
                    <label className="block text-sm space-y-1">
                      Kubecost cluster ID
                      <Input
                        className="block w-full min-w-0 px-3 py-2 text-sm bg-theme-elevated border border-theme-border rounded-md text-theme-text-primary placeholder:text-theme-text-tertiary focus:outline-none focus:border-skyhook-500"
                        value={clusterId}
                        onChange={(e) => setClusterId(e.target.value)}
                        placeholder="Auto-detect CLUSTER_ID"
                      />
                    </label>
                    <p className="text-xs text-theme-text-tertiary">
                      This mapping applies only to {profile.target.context}, not
                      the other contexts using this backend.
                    </p>
                    <button
                      type="button"
                      disabled={clusterId === profile.clusterId}
                      className="text-xs text-accent-text disabled:opacity-50"
                      onClick={() => void act({ action: 'mapping', clusterId })}
                    >
                      Test & apply mapping
                    </button>
                  </div>
                </Disclosure>
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
      {task === 'shared' && profile.connection && (
        <div className="card-inner-lg space-y-2">
          <p className="text-sm font-medium">
            Changes apply to {profile.connection.uses.length} contexts
          </p>
          <ConnectionUses uses={profile.connection.uses} />
          <p className="text-xs text-theme-text-tertiary">
            A connection check verifies this backend, not data availability for
            every context.
          </p>
        </div>
      )}
      {task === 'fork' && (
        <p className="text-sm text-theme-text-secondary">
          Creates an independent connection for {profile.target.context}.
          Unchanged credentials are retained without exposing them to your
          browser. Other contexts are unaffected.
        </p>
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
            applyLabel={
              task === 'shared'
                ? `Update ${profile.connection!.uses.length} contexts`
                : 'Apply now'
            }
            scopeDescription={
              task === 'shared'
                ? 'Updates this saved connection everywhere it is assigned.'
                : 'Applies only to this context. No restart needed.'
            }
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
            applyLabel={
              task === 'shared'
                ? `Update ${profile.connection!.uses.length} contexts`
                : 'Test & apply'
            }
          />
        ) : (
          <CostConnectionForm
            key={`${profile.revision}:${task}`}
            value={{ url, mode, clusterId }}
            secretSet={!replacing && profile.secretSet}
            shared={task === 'shared'}
            onChange={(draft) => {
              setUrl(draft.url)
              setMode(draft.mode)
              setClusterId(draft.clusterId)
            }}
            onDirtyChange={setSecretDirty}
            onApply={async (draft) => {
              await apply(draft)
            }}
            applyLabel={
              task === 'shared'
                ? `Update ${profile.connection!.uses.length} contexts`
                : 'Test & apply'
            }
          />
        ))}
      {!sharedSummary && feedback}
      {task === 'use' && (
        <div className="space-y-3">
          <p className="text-sm text-theme-text-secondary">
            Link this context to a saved backend. Later shared edits also apply
            here. Credentials are not shown or copied to your browser.
          </p>
          {catalog
            .filter((c) => c.type === kind)
            .map((connection) => (
              <button
                type="button"
                key={connection.id}
                disabled={!!connection.error}
                onClick={() => setSelected(connection)}
                className={`block w-full rounded-lg border p-3 text-left ${selected?.id === connection.id ? 'border-accent bg-accent-muted' : 'border-theme-border hover:bg-theme-hover'}`}
              >
                <span className="block text-sm font-medium">
                  {connection.name}
                </span>
                <span className="block text-xs text-theme-text-secondary break-all">
                  {connection.url}
                </span>
                <span className="block text-xs text-theme-text-tertiary">
                  {connection.uses.length
                    ? `Used by ${connection.uses
                        .slice(0, 2)
                        .map((use) => use.context)
                        .join(
                          ', '
                        )}${connection.uses.length > 2 ? ` +${connection.uses.length - 2} more` : ''}`
                    : `Unused · ${connection.id.slice(-6)}`}
                  {connection.error ? ` · ${connection.error}` : ''}
                </span>
              </button>
            ))}
          {!catalog.some((c) => c.type === kind) && (
            <p className="text-sm text-theme-text-tertiary">
              No saved connections yet. Configure one in the regular form first.
            </p>
          )}
          {selected && (
            <>
              <ConnectionUses uses={selected.uses} />
              {kind === 'cost' && (
                <label className="block text-sm space-y-1">
                  This context's Kubecost cluster ID
                  <Input
                    className="block w-full min-w-0 px-3 py-2 text-sm bg-theme-elevated border border-theme-border rounded-md text-theme-text-primary placeholder:text-theme-text-tertiary focus:outline-none focus:border-skyhook-500"
                    value={clusterId}
                    onChange={(e) => setClusterId(e.target.value)}
                    placeholder="Auto-detect CLUSTER_ID"
                  />
                  <span className="block text-xs text-theme-text-tertiary">
                    Mappings are never copied from another context.
                  </span>
                </label>
              )}
              <button
                type="button"
                className="btn-brand px-3 py-2 text-xs"
                onClick={() => {
                  const update = {
                    action: 'use',
                    connectionId: selected.id,
                    ...(kind === 'cost' ? { clusterId } : {})
                  }
                  if (
                    profile.connection &&
                    profile.connection.id !== selected.id &&
                    profile.connection.uses.length === 1
                  )
                    confirm(update)
                  else void act(update)
                }}
              >
                Use for {profile.target.context}
              </button>
            </>
          )}
        </div>
      )}
      {task === 'manage' && (
        <div className="space-y-4">
          {catalog
            .filter((c) => c.type === kind)
            .map((connection) => (
              <div
                key={connection.id}
                className="space-y-2 border-b border-theme-border pb-4"
              >
                <div className="flex justify-between gap-3">
                  <span className="text-sm font-medium">{connection.name}</span>
                  <button
                    type="button"
                    className="text-xs text-accent-text"
                    onClick={() => {
                      setSelected(connection)
                      setRename(connection.customName)
                      openTask('rename')
                    }}
                  >
                    Rename
                  </button>
                </div>
                <p className="text-xs text-theme-text-secondary break-all">
                  {connection.url}
                </p>
                <ConnectionUses
                  uses={connection.uses}
                  onForget={(use) =>
                    confirm({
                      action: 'forget',
                      binding: use.binding,
                      connectionId: connection.id
                    })
                  }
                />
                {!connection.uses.length && (
                  <button
                    type="button"
                    className="text-xs text-warning-text"
                    onClick={() =>
                      confirm({ action: 'delete', connectionId: connection.id })
                    }
                  >
                    Delete unused connection and credentials
                  </button>
                )}
              </div>
            ))}
          {unlinked.some((use) => use.integration === kind) && (
            <section className="space-y-2">
              <h4 className="text-sm font-medium">Context-specific settings</h4>
              <p className="text-xs text-theme-text-tertiary">
                Discovery credentials, source choices and mappings that are not
                linked to a saved backend.
              </p>
              <ConnectionUses
                uses={unlinked.filter((use) => use.integration === kind)}
                onForget={(use) =>
                  confirm({ action: 'forget', binding: use.binding })
                }
              />
            </section>
          )}
          {!catalog.some((c) => c.type === kind) &&
            !unlinked.some((use) => use.integration === kind) && (
              <p className="text-sm text-theme-text-tertiary">
                No saved connections.
              </p>
            )}
        </div>
      )}
      {task === 'rename' && selected && (
        <div className="space-y-3">
          <label className="block text-sm space-y-1">
            Connection name (optional)
            <Input
              className="block w-full min-w-0 px-3 py-2 text-sm bg-theme-elevated border border-theme-border rounded-md text-theme-text-primary placeholder:text-theme-text-tertiary focus:outline-none focus:border-skyhook-500"
              autoFocus
              value={rename}
              onChange={(e) => setRename(e.target.value)}
              placeholder={selected.name}
            />
          </label>
          <p className="text-xs text-theme-text-tertiary">
            Leave empty to use the backend name. This changes its label in every
            assigned context.
          </p>
          <button
            type="button"
            className="btn-brand px-3 py-2 text-xs"
            onClick={() =>
              void act({
                action: 'rename',
                connectionId: selected.id,
                name: rename
              })
            }
          >
            Save name
          </button>
        </div>
      )}
      {task === 'confirm' && pending && (
        <div className="space-y-4">
          <p className="text-sm text-theme-text-secondary">
            {pending.action === 'delete'
              ? `Delete ${removalConnection?.name || 'this unused connection'} and its stored credentials?`
              : ['use', 'replace', 'save'].includes(pending.action)
                ? `Replace the connection for ${removalContext || 'this context'}? Its previous connection and stored credentials will be deleted if no other context uses them.`
                : `Remove the saved settings for ${removalContext || 'this context'}? ${removalConnection ? 'If this is the last assignment, the unused connection and its stored credentials will also be deleted.' : 'Its discovery credentials and mapping will also be removed.'}`}
          </p>
          {pending.action !== 'delete' && removalConnection && (
            <label className="flex items-center gap-2 text-sm">
              <input
                type="checkbox"
                checked={keepUnused}
                onChange={(e) => setKeepUnused(e.target.checked)}
              />
              Keep the unused connection for reuse
            </label>
          )}
          <p className="text-xs text-theme-text-tertiary">
            Nothing is deleted from Kubernetes or the backend.
          </p>
          <div className="flex gap-3">
            <button
              type="button"
              className="btn-brand px-3 py-2 text-xs"
              onClick={() =>
                void act({ ...pending, confirmRemoval: true, keepUnused })
              }
            >
              {pending.action === 'delete'
                ? 'Delete connection'
                : ['use', 'replace', 'save'].includes(pending.action)
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
            onClick={() => void act({ action: 'reconfirm', kinds: acceptedChanges })}
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
            onClick={() => void loadTask('use')}
            className="text-accent-text hover:underline disabled:opacity-50"
          >
            Use saved connection…
          </button>
          <button
            type="button"
            disabled={dirty}
            onClick={() => void loadTask('manage')}
            className="text-accent-text hover:underline disabled:opacity-50"
          >
            Manage saved connections
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
              Renamed or moved a kubeconfig entry? Reuse its saved connection
              here, then forget the old assignment under Manage saved
              connections. Missing contexts are never removed automatically.
            </p>
          </div>
        </Disclosure>
      )}
    </fieldset>
  )
}

function ConnectionUses({
  uses,
  onForget,
  currentBinding,
  compact = false,
  showUnavailable = true
}: {
  uses: Usage[]
  onForget?: (use: Usage) => void
  currentBinding?: string
  compact?: boolean
  showUnavailable?: boolean
}) {
  const unavailable = uses.filter(
    (use) => use.availability !== 'available'
  ).length
  if (compact) {
    const ordered =
      uses.length > 4
        ? [
            ...uses.filter((use) => use.binding === currentBinding),
            ...uses.filter((use) => use.binding !== currentBinding)
          ]
        : uses
    return (
      <div className="space-y-2 text-xs text-theme-text-secondary">
        <ul className="flex flex-wrap gap-x-4 gap-y-2">
          {ordered.slice(0, 4).map((use) => (
            <li key={use.binding} className="min-w-0 break-words">
              <span
                className={
                  use.binding === currentBinding
                    ? 'font-medium text-theme-text-primary'
                    : undefined
                }
              >
                {use.context}
                {use.binding === currentBinding ? ' (current)' : ''}
              </span>
              {uses.some(
                (other) =>
                  other.binding !== use.binding && other.context === use.context
              ) && (
                <span className="block break-all text-theme-text-tertiary">
                  {use.source} · {use.inFileName}
                </span>
              )}
            </li>
          ))}
          {uses.length > 4 && (
            <li className="text-theme-text-tertiary">
              +{uses.length - 4} more
            </li>
          )}
        </ul>
        {showUnavailable && unavailable > 0 && (
          <p className="text-warning-text">
            {unavailable} {unavailable === 1 ? 'context is' : 'contexts are'}{' '}
            not available in this session.
          </p>
        )}
        <Disclosure
          summary={
            uses.length > 4
              ? 'All contexts and source details'
              : 'Source details'
          }
          summaryClassName="text-xs text-theme-text-tertiary"
        >
          <div className="pt-2">
            <ConnectionUses
              uses={uses}
              currentBinding={currentBinding}
              showUnavailable={false}
            />
          </div>
        </Disclosure>
      </div>
    )
  }
  return (
    <div className="space-y-1 text-xs text-theme-text-secondary">
      {showUnavailable && unavailable > 0 && (
        <p>
          {unavailable} {unavailable === 1 ? 'context is' : 'contexts are'} not
          available in this session.
        </p>
      )}
      {uses.map((use) => (
        <div key={use.binding} className="flex justify-between gap-3">
          <div className="min-w-0">
            <span className="font-medium break-words">{use.context}</span>
            {use.binding === currentBinding && <span> (current)</span>}
            {use.availability === 'removed' && (
              <span> · removed from kubeconfig</span>
            )}
            <p className="text-theme-text-tertiary break-all">
              {use.source} · {use.inFileName}
            </p>
          </div>
          {onForget && (
            <button
              type="button"
              className="text-accent-text self-start shrink-0 hover:underline"
              onClick={() => onForget(use)}
            >
              Forget assignment
            </button>
          )}
        </div>
      ))}
    </div>
  )
}
