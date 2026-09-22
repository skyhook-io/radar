import { useEffect, useRef, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { DialogPortal } from '@skyhook-io/k8s-ui/components/ui/DialogPortal'
import { fetchJSON, useClusterInfo } from '../../api/client'
import { collectApplicationEvidence, type ApplicationEvidenceResult, type EvidenceCandidates } from '../../api/application-evidence'

const vaultNames: Record<string, string> = { initialized: 'Initialized', sealed: 'Sealed', standby: 'Standby', performanceStandby: 'Performance standby' }
const applicationNames = { rabbitmq: 'RabbitMQ', nats: 'NATS', vault: 'Vault' }
interface Props { kind: string; group?: string; namespace: string; name: string; uid: string }

export function ApplicationEvidenceAction(props: Props) {
  const { data: cluster } = useClusterInfo()
  const context = cluster?.context
  const { data } = useQuery({
    queryKey: ['application-evidence-candidates', context, props.kind, props.group, props.namespace, props.name, props.uid],
    queryFn: ({ signal }) => fetchJSON<EvidenceCandidates>(`/application-evidence/candidates?${new URLSearchParams({ kind: props.kind, group: props.group ?? '', namespace: props.namespace, name: props.name })}`, signal),
    enabled: !!context && !!props.uid,
    retry: false,
    staleTime: 30_000,
    refetchOnWindowFocus: false,
  })
  if (!data?.enabled || !data.candidates.length || data.context !== context || data.subjectUID !== props.uid) return null
  return <EvidenceControl key={`${context}:${props.uid}`} data={data} />
}

function EvidenceControl({ data }: { data: EvidenceCandidates }) {
  const [snapshot, setSnapshot] = useState<EvidenceCandidates>()
  return <div className="mt-3">
    <button type="button" className="text-sm text-accent-text hover:underline" onClick={() => setSnapshot(data)}>Collect application evidence</button>
    {snapshot && <EvidenceDialog data={snapshot} onClose={() => setSnapshot(undefined)} />}
  </div>
}

function EvidenceDialog({ data, onClose }: { data: EvidenceCandidates; onClose: () => void }) {
  const [selected, setSelected] = useState(0)
  const [result, setResult] = useState<ApplicationEvidenceResult>()
  const [error, setError] = useState<string>()
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
    setError(undefined)
    try {
      const response = await collectApplicationEvidence(candidate, data.context, controller.signal)
      if (!controller.signal.aborted) setResult(response)
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
    <select id="application-evidence-target" className="mt-1 w-full rounded border border-theme-border bg-theme-surface p-2 text-sm text-theme-text-primary" value={selected} disabled={pending} onChange={e => { setSelected(Number(e.target.value)); setResult(undefined); setError(undefined) }}>
      {data.candidates.map((item, index) => <option key={`${item.target.uid}:${item.application}`} value={index}>{applicationNames[item.application]} · {item.target.namespace}/{item.target.pod} · {item.target.container}</option>)}
    </select>
    <p className="mt-2 text-sm text-theme-text-secondary">Collects: {candidate.expectedEvidence.join(', ')}.</p>
    <p className="mt-1 text-xs text-theme-text-tertiary">{candidate.coverage}</p>
    {(data.truncated || data.coverageLimited) && <p className="mt-2 text-xs text-theme-text-secondary">This is a limited set of candidate endpoints; it does not cover every replica.</p>}
    <p className="mt-2 text-xs text-theme-text-tertiary">Endpoint availability is not yet verified. Results describe the selected Pod at collection time.</p>
    {error && <p role="alert" className="mt-4 text-sm text-theme-text-primary">{error}</p>}
    {result && <ApplicationEvidenceObservation result={result} />}
    <div className="mt-5 flex justify-end gap-2">
      <button type="button" className="btn-brand-muted" onClick={onClose}>{pending ? 'Cancel' : 'Close'}</button>
      <button type="button" className="btn-brand" disabled={pending} onClick={collect}>{pending ? 'Collecting…' : result ? 'Collect again' : 'Collect evidence'}</button>
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
    {result.reason && <p className="mt-2">{result.reason.replaceAll('_', ' ')}. No conclusion about application health can be drawn from unavailable evidence.</p>}
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
