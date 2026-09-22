import { useEffect, useRef, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { AlertBanner } from '@skyhook-io/k8s-ui/components/ui/drawer-components'
import { SelectMenu } from '@skyhook-io/k8s-ui/components/ui/SelectMenu'
import { copyText } from '@skyhook-io/k8s-ui/utils/clipboard'
import { DialogPortal } from '@skyhook-io/k8s-ui/components/ui/DialogPortal'
import { fetchJSON, useClusterInfo, useCapabilities } from '../../api/client'
import { collectApplicationEvidence, type ApplicationEvidenceResult, type EvidenceCandidates } from '../../api/application-evidence'

const vaultNames: Record<string, string> = { initialized: 'Initialized', sealed: 'Sealed', standby: 'Standby', performanceStandby: 'Performance standby' }
const unavailableReasons: Record<string, string> = {
  endpoint_denied: 'The application endpoint requires authorization.',
  endpoint_unavailable: 'The application endpoint could not be reached.',
  endpoint_response_unavailable: 'The application endpoint did not return a readable response.',
  port_forward_unavailable: 'Radar could not open a Kubernetes port-forward to this Pod.',
  pod_read_unavailable: 'Radar could not read the selected Pod.',
  pod_recheck_unavailable: 'Radar could not verify the Pod identity after collection.',
  target_changed: 'The selected Pod changed. Close this dialog and reopen the collection action to select a current target.',
  collection_busy: 'Other evidence collections are running. Try again shortly.',
  collection_cancelled: 'Collection was cancelled or timed out.',
  response_too_large: 'The endpoint response exceeded the collection size limit.',
  missing_alarm_metrics: 'The response did not include the required RabbitMQ alarm metrics.',
  invalid_alarm_metric: 'The endpoint returned an invalid alarm value.',
  ambiguous_alarm_metric: 'The endpoint returned conflicting alarm values.',
  unexpected_shape: 'The response did not match the supported application format.',
  unexpected_http_status: 'The endpoint returned an unsupported HTTP status.',
  redirect_refused: 'The endpoint redirected the request; Radar does not follow redirects.',
}
const applicationNames = { rabbitmq: 'RabbitMQ', nats: 'NATS', vault: 'Vault' }
type AvailableCandidates = EvidenceCandidates & { context: string }
interface Props { kind: string; group?: string; namespace: string; name: string; uid: string }

export function ApplicationEvidenceAction(props: Props) {
  const { data: cluster } = useClusterInfo()
  const { data: capabilities } = useCapabilities()
  const context = cluster?.context
  const subjectKey = `${context}:${props.kind}:${props.group}:${props.namespace}:${props.name}:${props.uid}`
  const [permissionRetryTarget, setPermissionRetryTarget] = useState<string>()
  const retryPermissions = permissionRetryTarget === subjectKey
  const { data, error, refetch, isFetching } = useQuery({
    queryKey: ['application-evidence-candidates', context, props.kind, props.group, props.namespace, props.name, props.uid, retryPermissions],
    queryFn: ({ signal }) => fetchJSON<EvidenceCandidates>(`/application-evidence/candidates?${new URLSearchParams({ kind: props.kind, group: props.group ?? '', namespace: props.namespace, name: props.name, retryPermissions: String(retryPermissions) })}`, signal),
    enabled: !!context && !!props.uid && !!props.namespace && capabilities?.deployment?.mode === 'local',
    placeholderData: previous => previous,
    retry: false,
    staleTime: 30_000,
    refetchOnWindowFocus: false,
  })
  if (!context || !props.namespace || capabilities?.deployment?.mode !== 'local') return null
  if (retryPermissions && error) return <div className="mt-3"><AlertBanner variant="warning" title="Permission check failed" message={<>{error instanceof Error ? error.message : 'Could not verify application evidence permissions.'} <button type="button" className="text-accent-text hover:underline disabled:opacity-50" disabled={isFetching} onClick={() => void refetch()}>{isFetching ? 'Checking…' : 'Retry permission check'}</button></>} /></div>
  if (!data?.enabled || !data.context || data.context !== context || data.subjectUID !== props.uid) return null
  const retry = () => { if (retryPermissions) void refetch(); else setPermissionRetryTarget(subjectKey) }
  return <>
    {data.permissionCheckTimedOut && <div className="mt-3 text-sm text-theme-text-secondary">Application evidence permissions could not be verified in time. <button type="button" className="text-accent-text hover:underline disabled:opacity-50" disabled={isFetching} onClick={retry}>{isFetching ? 'Checking…' : 'Retry permission check'}</button></div>}
    <EvidenceControl key={`${context}:${props.uid}`} data={{ ...data, context: data.context }} refreshing={isFetching} onTargetChanged={() => { void refetch() }} />
  </>
}

function EvidenceControl({ data, refreshing, onTargetChanged }: { data: AvailableCandidates; refreshing: boolean; onTargetChanged: () => void }) {
  const [snapshot, setSnapshot] = useState<AvailableCandidates>()
  if (!snapshot && data.candidates.length === 0) return null
  const names = [...new Set(data.candidates.map(item => applicationNames[item.application]))].join(', ')
  return <div className="mt-3">
    {data.candidates.length > 0 && <button type="button" className="text-sm text-accent-text hover:underline disabled:opacity-50" disabled={refreshing} onClick={() => setSnapshot(data)}>Collect {names} evidence</button>}
    {snapshot && <EvidenceDialog data={snapshot} onTargetChanged={onTargetChanged} onClose={() => setSnapshot(undefined)} />}
  </div>
}

function EvidenceDialog({ data, onClose, onTargetChanged }: { data: AvailableCandidates; onClose: () => void; onTargetChanged: () => void }) {
  const [selected, setSelected] = useState(0)
  const [result, setResult] = useState<ApplicationEvidenceResult>()
  const [error, setError] = useState<string>()
  const [copied, setCopied] = useState(false)
  const [pending, setPending] = useState(false)
  const request = useRef<AbortController | undefined>(undefined)
  useEffect(() => () => request.current?.abort(), [])
  const candidate = data.candidates[selected]
  const collect = async () => {
    request.current?.abort()
    const controller = new AbortController()
    request.current = controller
    setPending(true)
    setResult(undefined)
    setCopied(false)
    setError(undefined)
    try {
      const response = await collectApplicationEvidence(candidate, data.context, controller.signal)
      if (!controller.signal.aborted) {
        setResult(response)
        if (response.reason === 'target_changed') onTargetChanged()
      }
    } catch (e) {
      if (!controller.signal.aborted) setError(e instanceof Error ? e.message : 'Collection failed')
    } finally {
      if (!controller.signal.aborted) setPending(false)
    }
  }
  return <DialogPortal open onClose={onClose} className="w-[min(92vw,640px)] max-h-[85vh] overflow-y-auto p-5">
    <h2 className="text-lg font-semibold text-theme-text-primary">Collect application evidence</h2>
    <p className="mt-2 text-sm text-theme-text-secondary">Read one Pod’s application endpoint through a temporary Kubernetes port-forward using your current permissions.</p>
    <p className="mt-2 break-all text-xs text-theme-text-tertiary">Context: {data.context}</p>
    <label className="mt-4 block text-sm text-theme-text-primary" htmlFor="application-evidence-target">Pod endpoint</label>
    <SelectMenu id="application-evidence-target" ariaLabel="Pod endpoint" className="mt-1" value={String(selected)} disabled={pending}
      options={data.candidates.map((item, index) => ({ value: String(index), label: `${applicationNames[item.application]} · ${item.target.namespace}/${item.target.pod} · ${item.target.container}` }))}
      onChange={value => { setSelected(Number(value)); setResult(undefined); setError(undefined); setCopied(false) }} />
    <p className="mt-2 text-sm text-theme-text-secondary">Collects: {candidate.expectedEvidence.join(', ')}.</p>
    <p className="mt-1 text-xs text-theme-text-tertiary">{candidate.coverage}</p>
    {(data.truncated || data.coverageLimited) && <p className="mt-2 text-xs text-theme-text-secondary">This is a limited set of candidate endpoints; it does not cover every replica.</p>}
    <p className="mt-2 text-xs text-theme-text-tertiary">Collection may be unavailable if the endpoint requires credentials or cannot be reached. Results describe this Pod at collection time.</p>
    {error && <div role="alert" className="mt-4"><AlertBanner variant="error" title="Could not complete the request" message={error} /></div>}
    {result && <ApplicationEvidenceObservation result={result} />}
    <div className="mt-5 flex flex-wrap justify-end gap-2">
      {result && <button type="button" className="btn-brand-muted px-3 py-2 text-sm" onClick={async () => { const success = await copyText(JSON.stringify({ context: data.context, ...result }, null, 2)); setCopied(success); if (!success) setError('Could not copy the observation.'); }}>{copied ? 'Copied' : 'Copy observation'}</button>}
      <button type="button" className="btn-brand-muted px-3 py-2 text-sm" onClick={onClose}>{pending ? 'Cancel' : 'Close'}</button>
      <button type="button" className="btn-brand px-3 py-2 text-sm" disabled={pending || result?.reason === 'target_changed'} onClick={collect}>{pending ? 'Collecting…' : result ? 'Collect again' : 'Collect evidence'}</button>
    </div>
  </DialogPortal>
}

export function ApplicationEvidenceObservation({ result }: { result: ApplicationEvidenceResult }) {
  const facts = result.outcome === 'observed' ? result.facts : undefined
  const nats = facts?.nats
  return <section aria-label="Application observations" className="mt-4 rounded border border-theme-border p-3 text-sm text-theme-text-primary">
    <h3 className="font-semibold">{result.outcome === 'observed' ? 'Endpoint observations' : 'Evidence unavailable'}</h3>
    <p className="mt-1 break-all text-xs text-theme-text-secondary">{result.target.namespace}/{result.target.pod}{result.target.container && ` · ${result.target.container}`}</p>
    <p className="mt-1 text-xs text-theme-text-tertiary">{new Date(result.observedAt).toLocaleString()} · {result.source.replaceAll('_', ' ')}{result.httpStatus !== undefined && ` · HTTP ${result.httpStatus}`}</p>
    {result.reason && <p className="mt-2">{unavailableReasons[result.reason] ?? `Collection unavailable (${result.reason}).`} No conclusion about application health can be drawn from unavailable evidence.</p>}
    {facts?.rabbitmq && <dl className="mt-3 grid grid-cols-2 gap-2"><dt>Disk alarm</dt><dd>{facts.rabbitmq.diskAlarm ? 'Reported' : 'Not reported'}</dd><dt>Memory alarm</dt><dd>{facts.rabbitmq.memoryAlarm ? 'Reported' : 'Not reported'}</dd></dl>}
    {facts?.vault && <dl className="mt-3 grid grid-cols-2 gap-2">{Object.entries(facts.vault).map(([key, value]) => <div key={key} className="contents"><dt>{vaultNames[key]}</dt><dd>{value ? 'Yes' : 'No'}</dd></div>)}</dl>}
    {nats && <div className="mt-3 space-y-2">
      <p>JetStream: {nats.jetStreamEnabled ? 'Enabled' : 'Disabled'}</p>
      <p className="text-xs text-theme-text-secondary">Coverage: {nats.coverage.replaceAll('_', ' ')} · {nats.returnedAccounts} accounts · {nats.returnedStreams} streams · {nats.returnedConsumers} consumers returned{nats.truncated ? ' · Truncated' : ''}</p>
      {nats.totals && <p>Node reports {nats.totals.messages} messages across {nats.totals.streams} streams and {nats.totals.consumers} consumers.</p>}
      {nats.consumers.length > 0 && <div className="overflow-x-auto"><table className="w-full text-left text-xs"><thead><tr><th className="pr-3">Account / stream / consumer</th><th className="pr-3">Pending</th><th className="pr-3">Ack pending</th><th>Redelivered</th></tr></thead><tbody>{nats.consumers.map(item => <tr key={JSON.stringify([item.account, item.stream, item.name])}><td className="py-2 pr-3 break-all">{item.account} / {item.stream} / {item.name}</td><td>{item.pending}</td><td>{item.ackPending}</td><td>{item.redelivered}</td></tr>)}</tbody></table></div>}
    </div>}
    <ul className="mt-3 space-y-1 text-xs text-theme-text-secondary">{result.limitations.map(item => <li key={item}>{item}</li>)}</ul>
  </section>
}
