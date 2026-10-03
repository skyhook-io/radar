import { useCallback, useEffect, useRef, useState, type ReactNode } from 'react'
import { Badge, Collapse, ConfirmDialog, SelectMenu } from '@skyhook-io/k8s-ui'
import { ArrowLeft, Info } from 'lucide-react'
import { Tooltip } from '../ui/Tooltip'
import type { FormFeedback } from './FormSaveActions'
import {
  apiUrl,
  getApiBase,
  getAuthHeaders,
  getCredentialsMode
} from '../../api/config'
import { ApiError } from '../../api/client'
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
  mode: string
  url: string
  headerKeys: string[]
  envHeaderKeys: string[]
  secretSet: boolean
  insecureTls: boolean
  clusterId: string
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
    omitted: string[]
    noticeDismissed: boolean
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
const previousSource = '__previous__'
const savedCredential: Record<IntegrationKind, string> = {
  metrics: 'saved headers',
  argocd: 'a saved token',
  cost: 'a saved API key'
}
function describeSource(kind: IntegrationKind, source: { url: string; secretSet: boolean; headerKeys: string[]; mode?: string; clusterId?: string }) {
  if (source.url) return source.url
  if (kind === 'cost' && source.mode === 'prometheus') return 'OpenCost metrics'
  if (source.secretSet || source.headerKeys.length > 0) return `Auto-discovery with ${savedCredential[kind]}`
  if (source.clusterId) return 'Auto-discovery with a Kubecost cluster mapping'
  return 'Auto-discovery'
}
// Cost's reset also returns the source picker to Automatic, so it can't share
// the endpoint-only "auto-discovery" wording.
const automaticAction: Record<IntegrationKind, string> = {
  metrics: 'Use auto-discovery',
  argocd: 'Use auto-discovery',
  cost: 'Reset to Automatic'
}
function sameIdentity(a?: TargetIdentity, b?: TargetIdentity) {
  return !!a && !!b && a.server === b.server && a.user === b.user &&
    a.tlsName === b.tlsName && a.trust === b.trust && a.proxy === b.proxy &&
    !!a.insecureTls === !!b.insecureTls
}
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
  // The review page compares against one previous identity, so it can only
  // approve integrations that were paused from that same identity.
  const pausedKinds = (Object.keys(snapshot) as IntegrationKind[]).filter(
    (k) => snapshot[k].state === 'target_changed'
  )
  const reviewKinds = pausedKinds.filter(
    (k) => k === kind || sameIdentity(snapshot[k].previousIdentity, profile.previousIdentity)
  )
  const separateReviewKinds = pausedKinds.filter((k) => !reviewKinds.includes(k))
  const separateIdentityKnown = !!profile.previousIdentity &&
    separateReviewKinds.every((k) => !!snapshot[k].previousIdentity)
  const acceptedChanges = accepted.filter((k) => reviewKinds.includes(k))
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
          throw new Error(data.error || 'Could not load other clusters’ settings.')
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
        throw new ApiError(
          data.error || `Settings request failed (${response.status})`,
          response.status
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
      data.error ? data.error : data.checked ? 'Saved · Connected' : 'Saved'
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
  // The forms disable Save while applying, which blurs it before the dialog
  // can record it as the element to return focus to.
  const confirmOpener = useRef<HTMLElement | null>(null)
  const confirm = (update: Update) => {
    confirmOpener.current = document.activeElement as HTMLElement
    setError('')
    setPending(update)
  }
  const cancelConfirmation = () => {
    confirmOpener.current?.focus()
    setPending(null)
  }
  const confirmPending = async () => {
    if (!pending) return
    setError('')
    try {
      await save({ ...pending, confirmRemoval: true })
      // Saving remounts the form, so the dialog's opener no longer exists.
      region.current?.focus()
    } catch (e) {
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
  // A conflict needs the reload control, which the form's inline error lacks.
  const saveFromForm = async (update: Update) => {
    setError('')
    try {
      return await save(update)
    } catch (e) {
      if (!(e instanceof ApiError) || e.status !== 409) throw e
      if (mounted.current) setError(e.message)
      return null
    }
  }
  const apply = async (draft: Omit<Update, 'action'>) => {
    const action = selected ? 'copy' : legacyDraft ? 'adopt' : task === 'replace' || discoveryDraft ? 'replace' : 'save'
    const credentialSource = selected ?? legacyDraft ?? profile
    if ((action === 'save' || action === 'copy' || action === 'adopt') && credentialSource.url && draft.url?.trim() === '' && !(kind === 'cost' && draft.mode === 'prometheus')) {
      const keptHeaders = credentialSource.headerKeys.some((key) => !draft.headers?.some((header) =>
        header.key.toLowerCase() === key.toLowerCase() && header.action === 'clear'))
      const keptSecret = credentialSource.secretSet && (!draft.secret || draft.secret.action === 'keep') && !draft.useCliToken
      if (keptHeaders || keptSecret) {
        throw new Error(`Remove or replace the saved credentials, or choose ${automaticAction[kind]} to clear this connection.`)
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
    // The staged discovery notice already said what Save removes.
    if (automaticDraft && update.action === 'auto')
      return saveFromForm(hasSavedConfiguration ? { ...update, confirmRemoval: true } : update)
    const removing =
      (!!profile.url && draft.url?.trim() === '') ||
      (hasSavedConfiguration && (action === 'replace' || action === 'copy')) ||
      (update.action === 'auto' && hasSavedConfiguration)
    if (removing) {
      confirm(update)
      return null
    }
    return saveFromForm(update)
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
        ? 'Do these settings still apply to this cluster?'
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
    (task === 'main' && ['auto', 'saved'].includes(profile.state)) || task === 'replace' ? (
      <button
        type="button"
        disabled={!!pending || automaticDraft || (!dirty && !hasSavedConfiguration && profile.mode === 'auto')}
        onClick={() => {
          resetDraft({ ...profile, url: '', mode: 'auto', insecureTls: false, clusterId: '' })
          setDiscoveryDraft(true)
          setDraftGeneration((generation) => generation + 1)
          setError('')
          // Cost's first control is its source picker, a listbox trigger.
          requestAnimationFrame(() => region.current?.querySelector<HTMLElement>('input:not(:disabled), button[aria-haspopup="listbox"]:not(:disabled)')?.focus())
        }}
        className="text-xs text-accent-text hover:underline disabled:opacity-50 shrink-0"
      >
        {automaticAction[kind]}
      </button>
    ) : undefined
  const previous = task === 'main' && profile.state === 'auto' ? profile.legacy : undefined
  const loadPrevious = (legacy: NonNullable<IntegrationProfile['legacy']>) => {
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
  }
  const copySource = (source: StoredConnection) => {
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
  }
  const reuseOptions = task === 'main' && profile.state !== 'launch' ? [
    ...(previous ? [{
      value: previousSource,
      label: 'Previous global settings',
      description: previous.error
        ? `Can’t be used: ${previous.error.replace(/\.$/, '')}. Fix it in ~/.radar/config.json.`
        : describeSource(kind, previous),
      disabled: !!previous.error,
    }] : []),
    ...copySources.map(connection => ({
      value: connection.binding,
      label: `${connection.context}${copySources.filter(other => other.context === connection.context).length > 1 ? ` · ${connection.source} · ${connection.inFileName}` : ''}`,
      description: connection.url,
    })),
  ] : []
  const copyAction = task === 'main' && profile.state !== 'launch' && (reuseOptions.length > 0 || catalogError) ? (
    <>
      {reuseOptions.length > 0 && (
        <SelectMenu
          variant="text"
          value=""
          placeholder="Copy settings from…"
          ariaLabel="Copy settings from…"
          searchPlaceholder="Search clusters or URLs"
          disabled={!!pending}
          options={reuseOptions}
          onChange={value => {
            if (value === previousSource) {
              if (previous && !previous.error) loadPrevious(previous)
              return
            }
            const source = copySources.find(connection => connection.binding === value)
            if (source) copySource(source)
          }}
        />
      )}
      {catalogError && (
        <span role="status" className="text-xs text-theme-text-secondary">
          Could not load other clusters.{' '}
          <button type="button" className="text-accent-text hover:underline" onClick={() => setCatalogRetry(retry => retry + 1)}>
            Retry
          </button>
        </span>
      )}
    </>
  ) : undefined
  const connectionActions = (autoDiscoveryAction || copyAction) && (
    <div className="flex flex-wrap items-center gap-x-4 gap-y-2 pt-2">
      {autoDiscoveryAction}
      {copyAction}
    </div>
  )
  const previousIdentity = profile.previousIdentity
  const currentIdentity = profile.target.identity
  const trustChanges = previousIdentity ? [
    previousIdentity.trust !== currentIdentity.trust && 'CA trust changed',
    previousIdentity.proxy !== currentIdentity.proxy && 'Proxy changed',
    (previousIdentity.tlsName !== currentIdentity.tlsName ||
      previousIdentity.insecureTls !== currentIdentity.insecureTls) && 'TLS settings changed',
  ].filter((change): change is string => !!change) : []
  const removedByDiscovery = [
    profile.url && (kind === 'cost' ? 'Kubecost endpoint' : 'endpoint'),
    (profile.secretSet || profile.headerKeys.length > 0) &&
      (kind === 'metrics' ? 'headers' : kind === 'argocd' ? 'API token' : 'API key'),
    kind === 'cost' && profile.clusterId && 'Kubecost cluster mapping'
  ].filter((item): item is string => !!item)
  const removedList: string | undefined = removedByDiscovery.length > 1
    ? `${removedByDiscovery.slice(0, -1).join(', ')} and ${removedByDiscovery[removedByDiscovery.length - 1]}`
    : removedByDiscovery[0]
  const removedPhrase = kind === 'cost'
    ? removedList ?? 'cost settings'
    : `${names[kind]} ${removedList ?? 'settings'}`
  const discoveryNotice = kind === 'cost'
    ? `Saving resets this cluster’s cost source to Automatic and removes its saved ${removedPhrase}. Other clusters are unchanged.`
    : `Saving switches this cluster to auto-discovery and removes its saved ${removedPhrase}. Other clusters are unchanged.`
  const useMetricsForCost =
    pending?.action === 'auto' && pending.mode === 'prometheus'
  // Paused settings have no editor, so an automatic reset there can only come
  // from declining them.
  const decliningPaused =
    pending?.action === 'auto' && !useMetricsForCost && profile.state === 'target_changed'
  const confirmationTitle = useMetricsForCost
    ? 'Use metrics for cost data?'
    : decliningPaused
      ? kind === 'cost' ? 'Reset cost source to Automatic?' : 'Use auto-discovery instead?'
      : 'Replace saved connection?'
  const confirmationLabel = useMetricsForCost
    ? 'Use metrics connection'
    : decliningPaused
      ? automaticAction[kind]
      : 'Replace connection'
  const confirmationMessage = useMetricsForCost
      ? `Remove the saved Kubecost connection, credentials and cluster mapping for ${removalContext || 'this cluster'} and use its metrics connection for cost data. Other clusters are unchanged.`
      : decliningPaused
        ? `Remove the saved ${removedPhrase} for ${removalContext || 'this cluster'} and ${kind === 'cost' ? 'reset its cost source to Automatic' : 'use auto-discovery'}? Other clusters are unchanged.`
        : `Replace the saved ${names[kind]} connection for ${removalContext || 'this cluster'} with your changes? Other clusters are unchanged.`
  if (pending)
    confirmationCopy.current = {
      title: confirmationTitle,
      message: confirmationMessage,
      label: confirmationLabel
    }
  const showFeedback = discoveryDraft ? automaticDraft && hasSavedConfiguration : !!(
    message &&
    task === 'main' &&
    messageRevision === profile.revision &&
    (messageWarning || !dirty)
  )
  const feedback: FormFeedback | undefined = showFeedback ? {
    tone: discoveryDraft ? 'info' : messageWarning ? 'warning' : 'success',
    message: discoveryDraft ? discoveryNotice : message,
  } : undefined
  return (
    <fieldset
      ref={region}
      tabIndex={-1}
      aria-label={`${names[kind]} connection settings`}
      disabled={busy}
      className="min-w-0 space-y-4 outline-none"
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
            Back to {names[kind]}
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
      {legacyDraft && <div className="text-xs text-theme-text-secondary space-y-1">
        <p>Copied from <span className="font-medium">previous global settings</span> · Unsaved</p>
        <p>{legacyDraft.headerKeys.length > 0 || legacyDraft.secretSet ? 'Saved credentials will be copied when you save. ' : ''}These settings applied to every cluster before; {legacyDraft.url ? 'check that this backend serves' : 'check that they belong to'} {profile.target.context}.</p>
        {legacyDraft.omitted.length > 0 && <p>Not copied: {legacyDraft.omitted.join(' and ')}, which belonged to another cluster.</p>}
        {legacyDraft.envHeaderKeys.length > 0 && <p>Environment-backed headers keep their references.</p>}
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
                  ? 'Cluster changed'
                  : 'Settings unavailable'}
              </Badge>
              <p className="text-sm text-theme-text-secondary">
                {profile.state === 'target_changed'
                  ? `The server, certificate authority, proxy or user for this context changed, so its saved ${names[kind]} settings are paused. Review them to keep using them here, or ${kind === 'cost' ? 'reset this cluster to Automatic' : 'use auto-discovery instead'}.`
                  : profile.error}
              </p>
              {profile.state === 'target_changed' && (
                <div className="flex flex-wrap items-center gap-3">
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
                  <button
                    type="button"
                    className="text-xs text-accent-text hover:underline"
                    onClick={() => confirm({ action: 'auto', ...(kind === 'cost' ? { mode: 'auto' } : {}) })}
                  >
                    {kind === 'cost' ? automaticAction.cost : 'Use auto-discovery instead'}
                  </button>
                </div>
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
              {hasSavedConfiguration && (
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
        </>
      )}
      <fieldset ref={editorRegion} className="min-w-0 empty:hidden">
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
      {!editor && task === 'main' && <Collapse open={showFeedback} className="!mt-0">
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
            The server, certificate authority, proxy or user for this context
            changed since these settings were saved. Keep an integration only
            if its backend serves this cluster; otherwise Radar would show
            another cluster’s data.
          </p>
          <dl className="grid grid-cols-[auto_1fr] gap-x-3 gap-y-2 text-xs">
            {previousIdentity && previousIdentity.server === currentIdentity.server ? (
              <>
                <dt className="text-theme-text-tertiary">Server</dt>
                <dd className="break-all">{currentIdentity.server} (unchanged)</dd>
              </>
            ) : (
              <>
                <dt className="text-theme-text-tertiary">Previous server</dt>
                <dd className="break-all">
                  {previousIdentity?.server || 'Not recorded'}
                </dd>
                <dt className="text-theme-text-tertiary">Current server</dt>
                <dd className="break-all">{currentIdentity.server}</dd>
              </>
            )}
            <dt className="text-theme-text-tertiary">User reference</dt>
            <dd>
              {previousIdentity && previousIdentity.user === currentIdentity.user
                ? `${currentIdentity.user || '—'} (unchanged)`
                : `${previousIdentity?.user || '—'} → ${currentIdentity.user || '—'}`}
            </dd>
            <dt className="text-theme-text-tertiary">Trust / proxy</dt>
            <dd>
              {!previousIdentity
                ? 'Not recorded'
                : trustChanges.length
                  ? trustChanges.join(' · ')
                  : 'Unchanged'}
            </dd>
          </dl>
          {reviewKinds
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
                {names[k]} · {snapshot[k].url || 'Auto-discovery'}
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
            Keep selected settings for this cluster
          </button>
          {separateReviewKinds.length > 0 && (
            <p className="text-xs text-theme-text-secondary">
              {separateReviewKinds.map((k) => names[k]).join(' and ')}{' '}
              {separateIdentityKnown
                ? `${separateReviewKinds.length > 1 ? 'were' : 'was'} saved for a different previous cluster, so ${separateReviewKinds.length > 1 ? 'review them in their own tabs' : 'review it in its own tab'}.`
                : `${separateReviewKinds.length > 1 ? 'need separate reviews in their own tabs' : 'needs a separate review in its own tab'}.`}
            </p>
          )}
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
