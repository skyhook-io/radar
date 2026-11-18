import { useEffect, useId, useState, type ReactNode } from 'react'
import { clsx } from 'clsx'
import { CheckCircle2, CircleHelp, XCircle } from 'lucide-react'
import { AlertBanner, Badge, Collapse, CollapseChevron, ConfirmDialog, Input, toneTextClass } from '@skyhook-io/k8s-ui'
import {
  substrateErrorCode,
  useSubstrateConnection,
  useUpdateSubstrateConnection,
  type SubstrateAPITier,
  type SubstrateConnection,
  type SubstrateConnectionTarget,
  type SubstrateConnectionUpdate,
  type SubstrateStatus,
} from '../../api/substrateWorkspace'
import { tierReading } from './readings'
import { CommandChip, Term } from './shared'

const INPUT = 'block w-full min-w-0 rounded-md border border-theme-border bg-theme-elevated px-3 py-1.5 font-mono text-sm text-theme-text-primary placeholder:text-theme-text-tertiary focus:border-skyhook-500 focus:outline-none'

const SHARED_PRINCIPAL_FALLBACK = 'system:serviceaccount:ate-system:ate-client'

/**
 * Why actor values are missing, in a banner: not connected, or connected and
 * failing, each with what to do next. Nothing for a connected tier.
 */
export function SubstrateTierBanner({ tier }: { tier: SubstrateAPITier }) {
  if (tier.state === 'connected' || tier.state === 'notConfigured' || tier.state === 'connecting') return null
  if (tier.code === 'sharedRadar' || tier.code === 'targetChanged' || tier.code === 'ownersOnly') return null
  const r = tierReading(tier)
  return (
    <div role="status">
      <AlertBanner variant={r.tone === 'unhealthy' ? 'error' : 'unknown'} title={`Substrate API: ${r.title.toLowerCase()}`} message={r.detail} className="">
        {r.nextStep && (
          <div className="mt-1 break-words text-xs text-theme-text-primary">
            <span className="font-medium">Next step: </span>
            {r.nextStep}
            {r.command && <CommandChip command={r.command} />}
          </div>
        )}
      </AlertBanner>
    </div>
  )
}

function Step({ mark, title, children }: { mark: ReactNode; title: ReactNode; children: ReactNode }) {
  return (
    <div className="flex gap-2">
      <span className="mt-0.5 shrink-0">{mark}</span>
      <div className="min-w-0 flex-1">
        <div className="text-sm font-medium text-theme-text-primary">{title}</div>
        <div className="mt-0.5 text-xs text-theme-text-secondary">{children}</div>
      </div>
    </div>
  )
}

function ServiceStep({ status, endpoint }: { status: SubstrateStatus; endpoint?: string }) {
  const { namespace, name, port, found } = status.apiService
  if (endpoint) {
    return (
      <Step mark={<CheckCircle2 className={clsx('h-4 w-4', toneTextClass('healthy'))} />} title="Substrate’s API server">
        Radar dials the endpoint set under Advanced, <span className="font-mono">{endpoint}</span>, instead of port-forwarding to Service <span className="font-mono">{namespace}/{name}</span>.
      </Step>
    )
  }
  const service = <span className="font-mono">{namespace}/{name}</span>
  if (found === true) {
    return (
      <Step mark={<CheckCircle2 className={clsx('h-4 w-4', toneTextClass('healthy'))} />} title="Radar found Substrate’s API server">
        Service {service}, port {port}. Radar reaches it by port-forward, as kubectl ate does.
      </Step>
    )
  }
  if (found === false) {
    return (
      <Step mark={<XCircle className={clsx('h-4 w-4', toneTextClass('degraded'))} />} title="Substrate’s API server wasn’t found">
        There is no Service {service}. If the API server runs elsewhere, set its address under Advanced.
      </Step>
    )
  }
  return (
    <Step mark={<CircleHelp className="h-4 w-4 text-theme-text-tertiary" />} title="Substrate’s API server">
      Radar reaches Service {service}, port {port}, by port-forward, as kubectl ate does. You can’t read Services in {namespace}, so Radar can’t check it is there.
    </Step>
  )
}

interface Draft {
  identity: 'shared' | 'tokenFile'
  tokenFile: string
  /** Comma-separated; for a token that may read some atespaces but not list them. */
  atespaces: string
  endpoint: string
  caFile: string
  serverName: string
}

function draftOf(connection: SubstrateConnection | undefined): Draft {
  const s = connection?.settings
  return {
    identity: s?.identity ?? 'tokenFile',
    tokenFile: s?.tokenFile ?? '',
    atespaces: s?.atespaces?.join(', ') ?? '',
    endpoint: s?.endpoint ?? '',
    caFile: s?.caFile ?? '',
    serverName: s?.serverName ?? '',
  }
}

/**
 * The update a draft saves; empty optional fields are left out. `target` is
 * the one the connection view returned, so a save for a context that has
 * since moved is refused rather than applied to another cluster.
 */
export function connectionUpdate(d: Draft, target: SubstrateConnectionTarget): SubstrateConnectionUpdate {
  const opt = (v: string) => (v.trim() ? v.trim() : undefined)
  const atespaces = d.atespaces.split(',').map((a) => a.trim()).filter(Boolean)
  return {
    target,
    settings: {
      identity: d.identity,
      tokenFile: d.identity === 'tokenFile' ? opt(d.tokenFile) : undefined,
      atespaces: atespaces.length ? atespaces : undefined,
      endpoint: opt(d.endpoint),
      caFile: opt(d.caFile),
      serverName: opt(d.serverName),
    },
  }
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

function ConnectForm({ connection, server }: { connection: SubstrateConnection | undefined; server: ReactNode }) {
  const id = useId()
  const [draft, setDraft] = useState<Draft>(() => draftOf(connection))
  const [advanced, setAdvanced] = useState(() => !!(connection?.settings?.endpoint || connection?.settings?.caFile || connection?.settings?.serverName))
  const [confirmDisconnect, setConfirmDisconnect] = useState(false)
  const save = useUpdateSubstrateConnection()
  // The saved settings arrive after the first render; adopt them until the user edits.
  const [touched, setTouched] = useState(false)
  useEffect(() => {
    if (!touched) setDraft(draftOf(connection))
  }, [connection, touched])
  const edit = (patch: Partial<Draft>) => {
    setTouched(true)
    setDraft((d) => ({ ...d, ...patch }))
  }
  const principal = connection?.sharedPrincipal || SHARED_PRINCIPAL_FALLBACK
  const missingToken = draft.identity === 'tokenFile' && !draft.tokenFile.trim()
  const advancedId = `${id}-advanced`
  return (
    <form
      onSubmit={(e) => {
        e.preventDefault()
        if (!missingToken && connection) save.mutate(connectionUpdate(draft, connection.target))
      }}
      className="space-y-4"
    >
      {/* Who Radar reads as comes first: it decides what Radar may see and whose name Substrate records. */}
      <fieldset className="space-y-2">
        <legend className="mb-1 text-sm font-semibold text-theme-text-primary">Who Radar reads Substrate as</legend>
        <label className="flex items-start gap-2 text-sm">
          <input type="radio" name={`${id}-identity`} className="mt-1" checked={draft.identity === 'tokenFile'} onChange={() => edit({ identity: 'tokenFile' })} />
          <span className="min-w-0">
            <span className="font-medium text-theme-text-primary">Your own viewer token</span>{' '}
            <Badge severity="neutral" size="sm">Recommended</Badge>
            <span className="block text-theme-text-secondary">
              Radar sees what your access lets you see. The token is a viewer grant from a Substrate owner, in a file on this machine; Radar saves only its path.
            </span>
          </span>
        </label>
        <Collapse open={draft.identity === 'tokenFile'}>
          <div className="space-y-3 pl-6">
            <Field id={`${id}-token`} label="Token file path">
              <PathInput id={`${id}-token`} value={draft.tokenFile} onChange={(v) => edit({ tokenFile: v })} placeholder="~/ate.token" />
            </Field>
            <Field id={`${id}-atespaces`} label={<><Term term="atespace">Atespaces</Term> (optional)</>} hint="If your token can’t list atespaces, name the ones to read. The rest stay unknown.">
              <Input id={`${id}-atespaces`} className={INPUT} value={draft.atespaces} onChange={(e) => edit({ atespaces: e.target.value })} placeholder="research, support-bots" spellCheck={false} />
            </Field>
            <Disclosure label="How Radar handles the token">
              Radar reads the file each time it connects, sends the token only to the selected Substrate API over verified TLS, and never returns it to the browser or stores it in its settings.
            </Disclosure>
            <TokenSetup />
          </div>
        </Collapse>
        <label className="flex items-start gap-2 text-sm">
          <input type="radio" name={`${id}-identity`} className="mt-1" checked={draft.identity === 'shared'} onChange={() => edit({ identity: 'shared' })} />
          <span className="min-w-0">
            <span className="font-medium text-theme-text-primary">Substrate’s shared client identity</span>
            <span className="block text-theme-text-secondary">
              Radar sees what that identity may see, and Substrate records the reads as that identity, not you. It is the one kubectl ate uses without <span className="font-mono text-[12.5px]">--token-file</span>:
            </span>
            <span className="block break-all font-mono text-[12.5px] text-theme-text-secondary">{principal}</span>
          </span>
        </label>
        <Collapse open={draft.identity === 'shared'}>
          <div className="pl-6">
            <AlertBanner variant="warning" title="This identity may be a global owner" className="">
              <div className="mt-1 text-xs text-theme-text-secondary">
                Substrate’s installer makes it a bootstrap owner unless the operators chose other owners, and Radar doesn’t narrow what it can do: Radar only reads, but anyone who can mint its token has every right it has. Use it on a cluster only you use.
              </div>
            </AlertBanner>
          </div>
        </Collapse>
      </fieldset>
      <div className="space-y-2 border-t border-theme-border pt-3">
        {server}
        <button type="button" aria-expanded={advanced} aria-controls={advancedId} onClick={() => setAdvanced((v) => !v)} className="inline-flex items-center gap-1 text-xs font-medium text-accent-text hover:underline">
          <CollapseChevron open={advanced} />
          Advanced: API server address and TLS
        </button>
        <Collapse open={advanced} id={advancedId}>
          <div className="grid gap-3 pt-1 sm:grid-cols-2">
            <Field id={`${id}-endpoint`} label="Endpoint" hint="host:port. Dials it directly instead of port-forwarding.">
              <Input id={`${id}-endpoint`} className={INPUT} value={draft.endpoint} onChange={(e) => edit({ endpoint: e.target.value })} placeholder="api.example.com:443" spellCheck={false} />
            </Field>
            <Field id={`${id}-sni`} label="Server name" hint="Defaults to api.ate-system.svc.">
              <Input id={`${id}-sni`} className={INPUT} value={draft.serverName} onChange={(e) => edit({ serverName: e.target.value })} placeholder="api.ate-system.svc" spellCheck={false} />
            </Field>
            <div className="sm:col-span-2">
              <Field id={`${id}-ca`} label="CA file" hint="PEM. Defaults to the cluster’s trust bundle for Substrate.">
                <PathInput id={`${id}-ca`} value={draft.caFile} onChange={(v) => edit({ caFile: v })} placeholder="/path/to/ca.pem" />
              </Field>
            </div>
          </div>
        </Collapse>
      </div>
      <div className="flex flex-wrap items-center gap-2 border-t border-theme-border pt-3">
        <button type="submit" disabled={missingToken || save.isPending || !connection} className="btn-brand px-3 py-1.5 text-sm font-medium">
          {save.isPending ? 'Checking the connection…' : connection?.saved ? 'Save and reconnect' : 'Connect'}
        </button>
        {missingToken && <span className="text-xs text-theme-text-tertiary">Enter the token file’s path.</span>}
        {/* Apart from saving, at the far end: it ends Radar's reads of Substrate. */}
        {connection?.saved && (
          <button
            type="button"
            disabled={save.isPending}
            onClick={() => setConfirmDisconnect(true)}
            className={clsx('btn-secondary ml-auto px-3 py-1.5 text-sm', toneTextClass('unhealthy'))}
          >
            Disconnect
          </button>
        )}
      </div>
      {save.error ? <SaveFailure error={save.error} /> : null}
      <ConfirmDialog
        open={confirmDisconnect}
        onClose={() => setConfirmDisconnect(false)}
        onConfirm={() => {
          setConfirmDisconnect(false)
          if (connection) save.mutate({ target: connection.target, settings: { identity: 'none' } })
        }}
        title="Disconnect from Substrate’s API?"
        message="Radar stops reading actors, templates and workers, and forgets the saved settings. It doesn’t revoke the token."
        confirmLabel="Disconnect"
        variant="danger"
      />
    </form>
  )
}

/**
 * Why a save did not take. The server dials the settings before saving, so a
 * failure means nothing changed; a moved context is refused outright.
 */
function SaveFailure({ error }: { error: unknown }) {
  const code = substrateErrorCode(error)
  const message = error instanceof Error ? error.message : 'unknown error'
  const text = code === 'targetChanged'
    ? 'Not saved: the cluster changed since you opened this. Review it again for the cluster Radar points at now.'
    : `Not saved: ${message}`
  return <p role="alert" className="text-xs text-theme-text-primary">{text}</p>
}

const AUTHZ_DOC = 'https://github.com/agent-substrate/substrate/blob/main/docs/guides/authorization.md#managing-access'

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

/**
 * How to connect Radar to Substrate's API: what it is for, the API server
 * Radar found, and who Radar reads as. In a shared Radar it says why it is
 * not offered instead of the form.
 */
export function SubstrateConnectPanel({ status, tier }: { status: SubstrateStatus; tier: SubstrateAPITier }) {
  const connection = useSubstrateConnection(status.canConfigure)
  const reading = tierReading(tier)
  return (
    <section aria-labelledby="substrate-connect" className="rounded-xl border border-theme-border bg-theme-surface px-4 py-4 shadow-theme-sm">
      <h2 id="substrate-connect" className="text-sm font-semibold text-theme-text-primary">See actors, templates and workers</h2>
      <p className="mt-1 text-sm text-theme-text-secondary">
        Substrate keeps <Term term="actor">actors</Term>, <Term term="template">templates</Term> and <Term term="worker">workers</Term> in its own API, not Kubernetes. Radar reads them, read-only, as the identity you choose.
      </p>
      <div className="mt-1">
        <Disclosure label="Why a separate identity?">
          Substrate checks its own roles, not Kubernetes RBAC, so reading it takes its own grant. Pools, worker Pods and Prometheus readings show either way.
        </Disclosure>
      </div>
      {!status.canConfigure ? (
        <p className="mt-3 text-sm text-theme-text-secondary">{tier.reason ?? 'This Radar is shared, so it does not read Substrate’s API.'}</p>
      ) : (
        <>
          {tier.code === 'targetChanged' && (
            <p className="mt-3 text-sm text-theme-text-secondary">
              {reading.detail} {reading.nextStep}
            </p>
          )}
          <div className="mt-4">
            {connection.isError ? (
              <p className="text-sm text-theme-text-secondary">Radar couldn’t read the saved connection: {connection.error instanceof Error ? connection.error.message : 'unknown error'}.</p>
            ) : (
              <ConnectForm key={connection.data?.target.context ?? ''} connection={connection.data} server={<ServiceStep status={status} endpoint={connection.data?.settings?.endpoint} />} />
            )}
          </div>
        </>
      )}
    </section>
  )
}

/** Whether the screen offers the Connect panel: whenever the API tier is not connected. */
/**
 * Whether a screen offers the Connect panel. Not in Radar Hub: an owner's
 * Radar reads as the shared client identity on its own, and everyone else
 * reads a note instead (ownersOnly).
 */
export function showsConnectPanel(tier: SubstrateAPITier): boolean {
  if (tier.code === 'ownersOnly' || tier.code === 'mintDenied' || tier.identity?.mode === 'sharedAsCaller') return false
  return tier.state !== 'connected' && tier.state !== 'connecting'
}
