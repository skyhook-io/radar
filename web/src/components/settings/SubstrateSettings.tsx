import { useCallback, useEffect, useId, useRef, useState, type ReactNode } from 'react'
import { clsx } from 'clsx'
import { CheckCircle2, CircleHelp, XCircle } from 'lucide-react'
import { AlertBanner, Badge, Collapse, CollapseChevron, ConfirmDialog, Input, toneTextClass } from '@skyhook-io/k8s-ui'
import {
  substrateErrorCode,
  useSubstrateConnection,
  useSubstrateStatus,
  useUpdateSubstrateConnection,
  type SubstrateAPITier,
  type SubstrateConnection,
  type SubstrateConnectionUpdate,
  type SubstrateLocalConnection,
  type SubstrateManagedConnectionView,
  type SubstrateSettings as SavedSettings,
  type SubstrateStatus,
} from '../../api/substrateWorkspace'
import { tierReading } from '../substrate/readings'
import { CommandChip, Term } from '../substrate/shared'
import { FormSaveActions, type FormFeedback } from './FormSaveActions'
import { OperatorManagedNotice } from './OperatorManagedNotice'

const INPUT = 'block w-full min-w-0 rounded-md border border-theme-border bg-theme-elevated px-3 py-1.5 font-mono text-sm text-theme-text-primary placeholder:text-theme-text-tertiary focus:border-skyhook-500 focus:outline-none'
const SHARED_PRINCIPAL_FALLBACK = 'system:serviceaccount:ate-system:ate-client'
const AUTHZ_DOC = 'https://github.com/agent-substrate/substrate/blob/main/docs/guides/authorization.md#managing-access'

export interface SubstrateDraft {
  identity: 'shared' | 'tokenFile'
  tokenFile: string
  /** Comma-separated; for a token that may read some atespaces but not list them. */
  atespaces: string
  endpoint: string
  caFile: string
  serverName: string
}

export function draftOf(settings: SavedSettings | null | undefined): SubstrateDraft {
  return {
    identity: settings?.identity ?? 'tokenFile',
    tokenFile: settings?.tokenFile ?? '',
    atespaces: settings?.atespaces?.join(', ') ?? '',
    endpoint: settings?.endpoint ?? '',
    caFile: settings?.caFile ?? '',
    serverName: settings?.serverName ?? '',
  }
}

/** The settings a draft saves; empty optional fields are left out. */
export function settingsOf(d: SubstrateDraft): SavedSettings {
  const opt = (v: string) => (v.trim() ? v.trim() : undefined)
  const atespaces = d.atespaces.split(',').map((a) => a.trim()).filter(Boolean)
  return {
    identity: d.identity,
    tokenFile: d.identity === 'tokenFile' ? opt(d.tokenFile) : undefined,
    atespaces: atespaces.length ? atespaces : undefined,
    endpoint: opt(d.endpoint),
    caFile: opt(d.caFile),
    serverName: opt(d.serverName),
  }
}

/**
 * The update a draft saves. `target` and `revision` are the ones the
 * connection view returned, so a save for a context that has since moved, or
 * over a newer save from another Radar, is refused rather than applied.
 */
export function connectionUpdate(d: SubstrateDraft, view: Pick<SubstrateLocalConnection, 'target' | 'revision'>, confirmDestination = false): SubstrateConnectionUpdate {
  return { target: view.target, revision: view.revision, settings: settingsOf(d), ...(confirmDestination ? { confirmDestination: true } : {}) }
}

function sameDraft(a: SubstrateDraft, b: SubstrateDraft): boolean {
  return JSON.stringify(settingsOf(a)) === JSON.stringify(settingsOf(b))
}

/**
 * Settings → Agent Substrate. A local private Radar edits this context's saved
 * connection here; an in-cluster Radar or Radar Hub shows how the installation
 * reads Substrate and this person's own access, read-only.
 *
 * While the local form has edits, it stays on screen with the connection it was
 * read for, even as the cluster changes or the next view loads: the dialog
 * freezes it, and only "Discard draft and load current cluster" (a new
 * reloadKey upstream) replaces it.
 */
export function SubstrateSettings({ active, onDirtyChange, onBusyChange }: {
  active: boolean
  onDirtyChange: (dirty: boolean) => void
  onBusyChange: (busy: boolean) => void
}) {
  // A first read takes seconds: poll faster while it runs, so Connected shows when it lands.
  const [connecting, setConnecting] = useState(false)
  const status = useSubstrateStatus(active, connecting ? { pollMs: 2_000 } : {})
  const connection = useSubstrateConnection(active)
  const [dirty, setDirty] = useState(false)
  const reportDirty = useCallback((d: boolean) => { setDirty(d); onDirtyChange(d) }, [onDirtyChange])
  const latest = connection.data
  const [lastLocal, setLastLocal] = useState<SubstrateLocalConnection | undefined>(latest?.view === 'local' ? latest : undefined)
  useEffect(() => { if (latest?.view === 'local') setLastLocal(latest) }, [latest])
  // The access row reads the polled status; the managed summary refetches when it moves.
  const tier = status.data?.apiTier
  useEffect(() => setConnecting(tier?.state === 'connecting'), [tier?.state])
  const refetch = connection.refetch
  useEffect(() => {
    if (latest?.view === 'managed' && tier && (tier.state !== latest.access.state || tier.code !== latest.access.code)) void refetch()
  }, [tier, latest, refetch])

  if (connection.upgrade || status.upgrade) {
    return <p className="text-sm text-theme-text-secondary">{connection.blockedReason ?? status.blockedReason ?? 'Agent Substrate settings need a newer Radar on this cluster.'}</p>
  }
  const keepForm = dirty && lastLocal
  if (connection.isError && !keepForm) {
    return <p role="alert" className="text-sm text-theme-text-secondary">Radar couldn’t read how it connects to Substrate: {connection.error instanceof Error ? connection.error.message : 'unknown error'}.</p>
  }
  if (!latest && !keepForm) {
    return <p className="text-sm text-theme-text-secondary">Loading…</p>
  }
  if (!keepForm && status.data && !status.data.installed) {
    return (
      <p className="text-sm text-theme-text-secondary">
        Agent Substrate isn’t installed on this cluster: it doesn’t serve the WorkerPool kind (ate.dev). Radar reads Substrate’s API once it is.
      </p>
    )
  }
  const statusError = status.isError ? (
    <p role="alert" className="text-xs text-theme-text-secondary">
      Radar couldn’t check Substrate’s status on this cluster: {status.error instanceof Error ? status.error.message : 'unknown error'}.
    </p>
  ) : null
  return (
    <div className="space-y-4">
      <p className="text-sm text-theme-text-secondary">
        Substrate keeps <Term term="actor">actors</Term>, <Term term="template">templates</Term> and <Term term="worker">workers</Term> in its own API, not Kubernetes, and checks its own roles there. Radar reads them, read-only, as the identity below. Pools, worker Pods and Prometheus readings show either way.
      </p>
      {statusError}
      {keepForm || latest?.view === 'local' ? (
        <LocalSubstrateForm
          initial={(keepForm ? lastLocal : latest) as SubstrateLocalConnection}
          latest={latest?.view === 'local' ? latest : undefined}
          tier={tier}
          status={status.data}
          onDirtyChange={reportDirty}
          onBusyChange={onBusyChange}
        />
      ) : latest ? (
        <ManagedSubstrateSummary view={latest} tier={tier} />
      ) : null}
    </div>
  )
}

/** This caller's own access in one row: the state, and what to do when it isn't connected. */
function AccessRow({ tier, label = 'Status' }: { tier: SubstrateAPITier; label?: string }) {
  const r = tierReading(tier)
  const connected = tier.state === 'connected'
  return (
    <div className="rounded-md border border-theme-border bg-theme-elevated/40 px-3 py-2 text-sm">
      <div className="flex flex-wrap items-baseline gap-x-2">
        <span className="text-xs font-medium text-theme-text-tertiary">{label}</span>
        <span className={clsx('font-medium', connected ? toneTextClass('healthy') : r.tone === 'unhealthy' ? toneTextClass('unhealthy') : 'text-theme-text-primary')}>
          {connected ? 'Connected' : r.title}
        </span>
        {connected && tier.endpoint && <span className="font-mono text-xs text-theme-text-secondary">{tier.endpoint}</span>}
      </div>
      {!connected && r.detail && <p className="mt-1 text-xs text-theme-text-secondary">{r.detail}</p>}
      {!connected && r.nextStep && (
        <p className="mt-1 text-xs text-theme-text-primary">
          <span className="font-medium">Next step: </span>
          {r.nextStep}
          {r.command && <CommandChip command={r.command} />}
        </p>
      )}
    </div>
  )
}

function Row({ label, children }: { label: ReactNode; children: ReactNode }) {
  return (
    <div className="grid grid-cols-[minmax(8rem,auto)_1fr] gap-x-4 py-1.5 text-sm">
      <dt className="text-theme-text-secondary">{label}</dt>
      <dd className="min-w-0 break-words text-theme-text-primary">{children}</dd>
    </div>
  )
}

/**
 * How an in-cluster Radar or Radar Hub reads Substrate: set by the operator in
 * Helm values, so shown as text, never as disabled inputs.
 */
function ManagedSubstrateSummary({ view, tier }: { view: SubstrateManagedConnectionView; tier?: SubstrateAPITier }) {
  const m = view.managed
  const access = tier ?? view.access
  return (
    <div className="space-y-4">
      <AccessRow tier={access} label="Your access" />
      <dl className="divide-y divide-theme-border rounded-md border border-theme-border px-3">
        <Row label="Reads Substrate’s API">
          {m.enabled ? 'Yes' : 'No'}
          {!m.enabled && access.reason && <span className="block text-xs text-theme-text-secondary">{access.reason}</span>}
        </Row>
        {m.enabled && (
          <Row label="As">
            <span className="font-mono text-xs">{view.sharedPrincipal || SHARED_PRINCIPAL_FALLBACK}</span>
            <span className="block text-xs text-theme-text-secondary">
              For each signed-in person, with a token created with their own Kubernetes access. Someone whose access can’t create that token sees no actors. On default installs this identity is a Substrate global owner; Radar only reads.
            </span>
          </Row>
        )}
        <Row label="API server"><span className="font-mono text-xs">{m.endpoint}</span></Row>
        <Row label="TLS server name"><span className="font-mono text-xs">{m.serverName}</span></Row>
        <Row label="Certificate checked against">{m.trust === 'caFile' ? 'A CA your operator provided' : 'The cluster’s trust bundle for Substrate'}</Row>
        {m.atespaces && m.atespaces.length > 0 && (
          <Row label={<Term term="atespace">Atespaces</Term>}>
            {m.atespaces.join(', ')}
            <span className="block text-xs text-theme-text-secondary">Read when the identity can’t list atespaces. They don’t limit what it can read.</span>
          </Row>
        )}
      </dl>
      <OperatorManagedNotice helmValue="substrate.api" />
    </div>
  )
}

function ServiceRow({ status, endpoint }: { status?: SubstrateStatus; endpoint?: string }) {
  if (!status) {
    return endpoint ? <p className="text-xs text-theme-text-secondary">Radar dials <span className="font-mono">{endpoint}</span>, set under Advanced.</p> : null
  }
  const { namespace, name, port, found } = status.apiService
  const service = <span className="font-mono">{namespace}/{name}</span>
  const [mark, text] = endpoint
    ? [<CheckCircle2 key="m" className={clsx('h-4 w-4', toneTextClass('healthy'))} />, <>Radar dials <span className="font-mono">{endpoint}</span>, set under Advanced, instead of port-forwarding to Service {service}.</>]
    : found === true
      ? [<CheckCircle2 key="m" className={clsx('h-4 w-4', toneTextClass('healthy'))} />, <>Radar found Service {service}, port {port}, and reaches it by port-forward, as kubectl ate does.</>]
      : found === false
        ? [<XCircle key="m" className={clsx('h-4 w-4', toneTextClass('degraded'))} />, <>There is no Service {service}. If Substrate’s API server runs elsewhere, set its address under Advanced.</>]
        : [<CircleHelp key="m" className="h-4 w-4 text-theme-text-tertiary" />, <>Radar reaches Service {service}, port {port}, by port-forward. You can’t read Services in {namespace}, so Radar can’t check it is there.</>]
  return (
    <div className="flex gap-2 text-xs text-theme-text-secondary">
      <span className="mt-0.5 shrink-0">{mark}</span>
      <span className="min-w-0">{text}</span>
    </div>
  )
}

/** A path input: shown whole on hover, and scrolled to its end, the part that names the file. */
function PathInput({ id, value, onChange, placeholder }: { id: string; value: string; onChange: (v: string) => void; placeholder: string }) {
  return (
    <Input
      id={id}
      className={INPUT}
      value={value}
      title={value || undefined}
      onChange={(e) => onChange(e.target.value)}
      onBlur={(e) => { e.currentTarget.scrollLeft = e.currentTarget.scrollWidth }}
      placeholder={placeholder}
      spellCheck={false}
    />
  )
}

function Field({ label, hint, id, children }: { label: ReactNode; hint?: string; id: string; children: ReactNode }) {
  return (
    <div>
      <label htmlFor={id} className="block text-xs font-medium text-theme-text-secondary">{label}</label>
      <div className="mt-1">{children}</div>
      {hint && <p className="mt-1 text-xs text-theme-text-tertiary">{hint}</p>}
    </div>
  )
}

/** Why a save didn't take. The server checks the connection before saving, so a failure changed nothing. */
function saveError(error: unknown): string | undefined {
  if (!error) return undefined
  const code = substrateErrorCode(error)
  if (code === 'targetChanged') return 'Not saved: the cluster changed since you opened this. Review it again for the cluster Radar points at now.'
  if (code === 'conflict') return 'Not saved: the connection was changed elsewhere (another Radar, or the Desktop app) since you opened it. Discard to load it, then edit again.'
  return `Not saved: ${error instanceof Error ? error.message : 'unknown error'}. Nothing changed.`
}

function LocalSubstrateForm({ initial, latest, tier, status, onDirtyChange, onBusyChange }: {
  /** The connection the form is read for at mount. */
  initial: SubstrateLocalConnection
  /** The newest view of the current context; absent while it loads. */
  latest?: SubstrateLocalConnection
  /** The polled access tier, newer than any view's. */
  tier?: SubstrateAPITier
  status?: SubstrateStatus
  onDirtyChange: (dirty: boolean) => void
  onBusyChange: (busy: boolean) => void
}) {
  const id = useId()
  // The view the draft was started from: its settings, target and revision
  // travel together, so a save is checked against exactly what was reviewed.
  const [base, setBase] = useState(initial)
  const saved = draftOf(base.saved ? base.settings : undefined)
  const [draft, setDraft] = useState<SubstrateDraft>(saved)
  const [advanced, setAdvanced] = useState(() => !!(saved.endpoint || saved.caFile || saved.serverName))
  const [confirm, setConfirm] = useState<'disconnect' | 'destination' | null>(null)
  const [destinationReason, setDestinationReason] = useState('')
  const [feedback, setFeedback] = useState<FormFeedback | undefined>()
  const save = useUpdateSubstrateConnection()
  const dirty = !sameDraft(draft, saved) && !base.targetChanged
  const busy = save.isPending
  const view = base
  const adopt = useCallback((next: SubstrateLocalConnection) => {
    setBase(next)
    setDraft(draftOf(next.saved ? next.settings : undefined))
  }, [])
  // The revision a save just replaced: the query may still hold it for a moment, and it is not newer.
  // Once the query shows anything else, the guard ends: revisions are content hashes, so that same
  // revision can legitimately come back later.
  const replaced = useRef<string | null>(null)
  if (replaced.current && latest && latest.revision !== replaced.current) replaced.current = null
  // A clean form follows the newest view; an edited one keeps its own until saved or discarded. The
  // whole target counts, generations included: the server refuses a save for any other.
  const moved = !!latest && latest.revision !== replaced.current &&
    (latest.revision !== base.revision || JSON.stringify(latest.target) !== JSON.stringify(base.target))
  useEffect(() => { if (moved && !dirty && !busy && latest) adopt(latest) }, [moved, dirty, busy, latest, adopt])
  useEffect(() => onDirtyChange(dirty), [dirty, onDirtyChange])
  useEffect(() => onBusyChange(busy), [busy, onBusyChange])
  // Cleared on unmount only, whatever callbacks the parent passes on the way.
  const callbacks = useRef({ onDirtyChange, onBusyChange })
  callbacks.current = { onDirtyChange, onBusyChange }
  useEffect(() => () => { callbacks.current.onDirtyChange(false); callbacks.current.onBusyChange(false) }, [])

  const edit = (patch: Partial<SubstrateDraft>) => {
    setFeedback(undefined)
    save.reset()
    setDraft((d) => ({ ...d, ...patch }))
  }
  const principal = view.sharedPrincipal || SHARED_PRINCIPAL_FALLBACK
  const missingToken = draft.identity === 'tokenFile' && !draft.tokenFile.trim()
  const submit = (update: SubstrateConnectionUpdate, done: string) => {
    setFeedback(undefined)
    save.mutate(update, {
      onSuccess: (next: SubstrateConnection) => {
        if (next.view === 'local') {
          replaced.current = base.revision
          adopt(next)
        }
        setFeedback({ tone: 'success', message: done })
      },
      onError: (error) => {
        if (substrateErrorCode(error) === 'confirmDestination') {
          setDestinationReason(error instanceof Error ? error.message : '')
          setConfirm('destination')
        }
      },
    })
  }
  const saveDraft = (confirmDestination = false) => {
    if (missingToken) return
    submit(connectionUpdate(draft, view, confirmDestination), 'Saved. Connection checked; Substrate may still limit what this identity can read.')
  }
  const advancedId = `${id}-advanced`
  const error = substrateErrorCode(save.error) === 'confirmDestination' ? undefined : saveError(save.error)

  return (
    <form onSubmit={(e) => { e.preventDefault(); if (dirty) saveDraft() }} className="space-y-4">
      <AccessRow tier={tier ?? view.access} />
      {moved && dirty && (
        <p role="status" className="text-xs text-theme-text-secondary">
          This connection changed since you started editing (another Radar, the Desktop app, or the cluster). Saving is refused until you discard your edits and review it again.
        </p>
      )}
      {view.targetChanged && (
        <AlertBanner variant="warning" title="Saved for another cluster" className="">
          <div className="mt-1 space-y-2 text-xs text-theme-text-secondary">
            <p>This connection was saved while context {view.target.context} pointed at another cluster, so Radar doesn’t use it here. Use it for this cluster only if the same identity and API server belong here.</p>
            <div className="flex flex-wrap gap-2">
              <button type="button" disabled={busy} className="btn-secondary px-3 py-1 text-xs" onClick={() => submit({ target: view.target, revision: view.revision, settings: settingsOf(saved), action: 'reconfirm' }, 'Saved for this cluster.')}>
                Use it for this cluster
              </button>
              <button type="button" disabled={busy} className={clsx('btn-secondary px-3 py-1 text-xs', toneTextClass('unhealthy'))} onClick={() => setConfirm('disconnect')}>
                Disconnect API instead
              </button>
            </div>
          </div>
        </AlertBanner>
      )}
      <fieldset disabled={view.targetChanged} className="space-y-2">
        <legend className="mb-1 text-sm font-semibold text-theme-text-primary">Who Radar reads Substrate as</legend>
        <label className="flex items-start gap-2 text-sm">
          <input type="radio" name={`${id}-identity`} className="mt-1" checked={draft.identity === 'tokenFile'} onChange={() => edit({ identity: 'tokenFile' })} />
          <span className="min-w-0">
            <span className="font-medium text-theme-text-primary">A token file</span>{' '}
            <Badge severity="neutral" size="sm">Recommended</Badge>
            <span className="block text-theme-text-secondary">
              A viewer grant from a Substrate owner, in a file on the machine running Radar. Radar saves only the path, and never sends the token to the browser.
            </span>
          </span>
        </label>
        <Collapse open={draft.identity === 'tokenFile'}>
          <div className="space-y-3 pl-6">
            <Field id={`${id}-token`} label="Token file path">
              <PathInput id={`${id}-token`} value={draft.tokenFile} onChange={(v) => edit({ tokenFile: v })} placeholder="~/ate.token" />
            </Field>
            <TokenSetup />
          </div>
        </Collapse>
        <label className="flex items-start gap-2 text-sm">
          <input type="radio" name={`${id}-identity`} className="mt-1" checked={draft.identity === 'shared'} onChange={() => edit({ identity: 'shared' })} />
          <span className="min-w-0">
            <span className="font-medium text-theme-text-primary">Substrate’s shared client</span>
            <span className="block text-theme-text-secondary">
              The identity kubectl ate uses without <span className="font-mono text-[12.5px]">--token-file</span>. Radar creates its token with your Kubernetes access, and Substrate records the reads as it, not you:
            </span>
            <span className="block break-all font-mono text-[12.5px] text-theme-text-secondary">{principal}</span>
          </span>
        </label>
        <Collapse open={draft.identity === 'shared'}>
          <div className="pl-6">
            <AlertBanner variant="warning" title="This identity may be a global owner" className="">
              <div className="mt-1 text-xs text-theme-text-secondary">
                Substrate’s installer makes it an owner of every atespace unless the operators chose other owners. Radar only reads, but anyone who can create its token has every right it has.
              </div>
            </AlertBanner>
          </div>
        </Collapse>
      </fieldset>
      <div className="space-y-2 border-t border-theme-border pt-3">
        <ServiceRow status={status} endpoint={draft.endpoint.trim() || undefined} />
        <button type="button" aria-expanded={advanced} aria-controls={advancedId} onClick={() => setAdvanced((v) => !v)} className="inline-flex items-center gap-1 text-xs font-medium text-accent-text hover:underline">
          <CollapseChevron open={advanced} />
          Advanced: API server address, TLS and atespaces
        </button>
        <Collapse open={advanced} id={advancedId}>
          <fieldset disabled={view.targetChanged} className="grid gap-3 pt-1 sm:grid-cols-2">
            <Field id={`${id}-endpoint`} label="Endpoint" hint="host:port. Radar dials it directly instead of port-forwarding.">
              <Input id={`${id}-endpoint`} className={INPUT} value={draft.endpoint} onChange={(e) => edit({ endpoint: e.target.value })} placeholder="api.example.com:443" spellCheck={false} />
            </Field>
            <Field id={`${id}-sni`} label="TLS server name" hint="Defaults to api.ate-system.svc.">
              <Input id={`${id}-sni`} className={INPUT} value={draft.serverName} onChange={(e) => edit({ serverName: e.target.value })} placeholder="api.ate-system.svc" spellCheck={false} />
            </Field>
            <div className="sm:col-span-2">
              <Field id={`${id}-ca`} label="CA file" hint="PEM. Defaults to the cluster’s trust bundle for Substrate.">
                <PathInput id={`${id}-ca`} value={draft.caFile} onChange={(v) => edit({ caFile: v })} placeholder="/path/to/ca.pem" />
              </Field>
            </div>
            <div className="sm:col-span-2">
              <Field id={`${id}-atespaces`} label={<><Term term="atespace">Atespaces</Term> to read if listing is denied</>} hint="Only used when the identity can’t list atespaces; the rest stay unknown. These names don’t limit what it can read.">
                <Input id={`${id}-atespaces`} className={INPUT} value={draft.atespaces} onChange={(e) => edit({ atespaces: e.target.value })} placeholder="research, support-bots" spellCheck={false} />
              </Field>
            </div>
          </fieldset>
        </Collapse>
      </div>
      {missingToken && dirty && <p className="text-xs text-theme-text-tertiary">Enter the token file’s path to save.</p>}
      <FormSaveActions
        dirty={dirty && !missingToken}
        busy={busy}
        onSave={() => saveDraft()}
        onDiscard={() => { if (latest) adopt(latest); else setDraft(saved); setFeedback(undefined); save.reset() }}
        feedback={feedback}
        error={error}
      />
      {view.saved && !view.targetChanged && (
        <div className="flex justify-end">
          <button type="button" disabled={busy} onClick={() => setConfirm('disconnect')} className={clsx('text-xs hover:underline disabled:opacity-50', toneTextClass('unhealthy'))}>
            Disconnect API
          </button>
        </div>
      )}
      <ConfirmDialog
        open={confirm === 'disconnect'}
        onClose={() => setConfirm(null)}
        onConfirm={() => {
          setConfirm(null)
          submit({ target: view.target, revision: view.revision, settings: { identity: 'none' } }, 'Disconnected. Radar no longer reads Substrate’s API on this cluster.')
        }}
        title="Disconnect from Substrate’s API?"
        message="Radar stops reading actors, templates and workers on this cluster, and forgets the saved settings. It doesn’t revoke the token."
        confirmLabel="Disconnect"
        variant="danger"
        showWarning={false}
      />
      <ConfirmDialog
        open={confirm === 'destination'}
        onClose={() => setConfirm(null)}
        onConfirm={() => {
          setConfirm(null)
          saveDraft(true)
        }}
        title="Send the saved credential to a new address?"
        message={`${destinationReason ? `${destinationReason} ` : ''}Radar will present the same identity to this API server. Continue only if you trust it.`}
        confirmLabel="Send and save"
        variant="warning"
        showWarning={false}
      />
    </form>
  )
}

/**
 * How to get a viewer token, from Substrate's authentication and
 * authorization guides. There is no one-line grant: an owner edits the
 * access policy.
 */
function TokenSetup() {
  return (
    <Disclosure label="How to get a viewer token">
      <ol className="list-decimal space-y-2 pl-5">
        <li>
          Mint a token for your own ServiceAccount, for example in a namespace set aside for Substrate users (Substrate’s authentication guide, “Your own ServiceAccount”):
          <CommandChip command="kubectl create token -n ate-users <you> --audience api.ate-system.svc > ~/ate.token" />
          The token expires; Radar rereads the file when it reconnects, so write a fresh one there when it does.
        </li>
        <li>
          Ask a Substrate owner to add <span className="font-mono">user:system:serviceaccount:ate-users:&lt;you&gt;</span> as a <span className="font-medium">viewer</span>:
          <ul className="mt-1 list-disc space-y-0.5 pl-4">
            <li>in the global access policy, to see workers and every atespace;</li>
            <li>or in one atespace’s policy, to see its actors and templates only. Workers then stay unread: only a global viewer may list them.</li>
          </ul>
          There is no single grant command. The owner edits the policy and writes it back:
          <CommandChip command="kubectl ate get access-policy --global -o yaml > policy.yaml" />
          <CommandChip command="kubectl ate update access-policy --global -f policy.yaml" />
          See <a href={AUTHZ_DOC} target="_blank" rel="noreferrer" className="text-accent-text hover:underline">Managing access</a> in Substrate’s authorization guide.
        </li>
      </ol>
    </Disclosure>
  )
}

/** Secondary detail under a label that says what it explains; folded until asked for. */
function Disclosure({ label, children }: { label: string; children: ReactNode }) {
  const [open, setOpen] = useState(false)
  const id = useId()
  return (
    <div>
      <button type="button" aria-expanded={open} aria-controls={id} onClick={() => setOpen((v) => !v)} className="inline-flex items-center gap-1 text-xs font-medium text-accent-text hover:underline">
        <CollapseChevron open={open} />
        {label}
      </button>
      <Collapse open={open} id={id}>
        <div className="pt-1.5 text-xs text-theme-text-secondary">{children}</div>
      </Collapse>
    </div>
  )
}
