import type { ReactNode } from 'react'
import { useNavigate } from 'react-router-dom'
import { Badge, formatAge, type CNPGFleet } from '@skyhook-io/k8s-ui'
import type { CNPGOperatorDiagnosis, CNPGOperatorReconcilePod, CNPGReadCoverage } from '../../api/cnpg-recovery'
import { buildWorkloadPath } from '../../utils/navigation'
import { GrantText, Mono, Sub } from './shared'

function Row({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="grid grid-cols-[10rem_minmax(0,1fr)] gap-x-4 border-t border-theme-border px-4 py-2.5 first:border-t-0">
      <div className="text-sm text-theme-text-tertiary">{label}</div>
      <div className="min-w-0 text-sm text-theme-text-primary">{children}</div>
    </div>
  )
}

function Unread({ cov, what }: { cov: CNPGReadCoverage; what: string }) {
  if (cov.state === 'denied' && cov.grant) {
    return (
      <span className="text-theme-text-tertiary">
        No access to {what} · needs <GrantText grant={cov.grant} />
      </span>
    )
  }
  const text =
    cov.state === 'denied'
      ? `No access to ${what} (needs a grant you do not have)`
      : cov.state === 'notFound'
        ? `${what} not found`
        : `${what} could not be read${cov.reason ? `: ${cov.reason}` : ''}`
  return <span className="text-theme-text-tertiary">{text}</span>
}

function Leader({ d }: { d: CNPGOperatorDiagnosis }) {
  const l = d.leader
  if (l.state === 'disabled') return <span className="text-theme-text-secondary">Leader election is off ({l.reason}); run a single replica.</span>
  if (l.state !== 'ok') return <Unread cov={{ state: l.state, grant: l.grant, reason: l.reason }} what={`Lease ${l.lease ?? ''}`.trim()} />
  return (
    <>
      <div className="flex flex-wrap items-center gap-2">
        {l.stale ? <Badge severity="error" size="sm">Not renewed</Badge> : <Badge severity="success" size="sm">Held</Badge>}
        {l.holderPod ? <Mono>{l.holderPod}</Mono> : <span className="text-theme-text-tertiary">no holder</span>}
        {l.holderPod && !l.holderIsCurrentPod && <span className="text-xs text-theme-text-tertiary">not one of the Deployment’s current Pods</span>}
      </div>
      <Sub>
        Lease {l.lease}
        {l.renewTime ? ` · renewed ${formatAge(l.renewTime)} ago` : ''}
        {l.leaseDurationSeconds !== undefined ? ` · duration ${l.leaseDurationSeconds}s` : ''}
        {l.transitions !== undefined ? ` · ${l.transitions} leader change${l.transitions === 1 ? '' : 's'}` : ''}
        {l.stale ? ' · no operator instance is leading, so nothing reconciles' : ''}
      </Sub>
    </>
  )
}

function Watch({ d, fleet }: { d: CNPGOperatorDiagnosis; fleet: CNPGFleet }) {
  const w = d.watch
  const outside = w.all || w.unresolved ? [] : fleet.rows.filter((r) => !w.namespaces.includes(r.namespace))
  return (
    <>
      <div>{w.all ? 'All namespaces' : w.unresolved ? <span className="text-theme-text-secondary">{w.unresolved}</span> : w.namespaces.join(', ')}</div>
      <Sub>{w.source}</Sub>
      {outside.length > 0 && (
        <div className="mt-1 text-xs text-theme-text-secondary">
          Not reconciled by this operator: {outside.map((r) => `${r.namespace}/${r.name}`).join(', ')}
        </div>
      )}
    </>
  )
}

function Webhooks({ d }: { d: CNPGOperatorDiagnosis }) {
  return (
    <div className="space-y-2">
      {d.webhooks.map((cfg) => {
        if (cfg.state !== 'ok') {
          return (
            <div key={cfg.name}>
              <div className="text-xs text-theme-text-secondary">{cfg.kind}</div>
              {cfg.state === 'notFound' ? (
                <span className="text-theme-text-secondary">
                  <Mono>{cfg.name}</Mono> is missing: the operator recreates it at startup; until then CloudNativePG objects are not validated or defaulted.
                </span>
              ) : (
                <Unread cov={cfg} what={cfg.name} />
              )}
            </div>
          )
        }
        const noCA = cfg.webhooks.filter((w) => !w.caBundleSet && !w.url)
        const policies = [...new Set(cfg.webhooks.map((w) => w.failurePolicy))]
        return (
          <div key={cfg.name}>
            <div className="text-xs text-theme-text-secondary">{cfg.kind}</div>
            <div>
              <Mono>{cfg.name}</Mono> · {cfg.webhooks.length} webhook{cfg.webhooks.length === 1 ? '' : 's'} · failurePolicy {policies.join(', ')}
            </div>
            {noCA.length > 0 ? (
              <div className="text-xs text-theme-text-secondary">No CA bundle on {noCA.map((w) => w.name).join(', ')}: the apiserver cannot verify the webhook, so calls fail.</div>
            ) : (
              <Sub>CA bundle set on every webhook</Sub>
            )}
          </div>
        )
      })}
      {d.webhookServices.map((svc) => {
        const failClosed = d.webhooks.some((c) => c.webhooks.some((w) => w.service === `${svc.namespace}/${svc.name}` && w.failurePolicy === 'Fail'))
        return (
          <div key={`${svc.namespace}/${svc.name}`}>
            <div className="text-xs text-theme-text-secondary">Service {svc.namespace}/{svc.name}</div>
            {svc.state !== 'ok' || svc.readyEndpoints === null ? (
              <Unread cov={svc} what="its endpoints" />
            ) : svc.readyEndpoints > 0 ? (
              <div>
                {svc.readyEndpoints} ready endpoint{svc.readyEndpoints === 1 ? '' : 's'}
                {svc.notReadyEndpoints ? <span className="text-theme-text-tertiary"> · {svc.notReadyEndpoints} not ready</span> : null}
              </div>
            ) : (
              <div className="flex flex-wrap items-center gap-2">
                <Badge severity="error" size="sm">No ready endpoint</Badge>
                <span className="text-xs text-theme-text-secondary">
                  {failClosed ? 'With failurePolicy Fail, every create or update of a CloudNativePG object is rejected until the operator is ready.' : 'Webhook calls are skipped (failurePolicy Ignore).'}
                </span>
              </div>
            )}
          </div>
        )
      })}
    </div>
  )
}

function Reconcile({ pods }: { pods: CNPGOperatorReconcilePod[] }) {
  if (pods.length === 0) return <span className="text-theme-text-tertiary">No operator Pods visible</span>
  return (
    <div className="space-y-3">
      {pods.map((p) => (
        <div key={p.pod}>
          <div className="flex flex-wrap items-center gap-2">
            <Mono>{p.pod}</Mono>
            {p.leader && <Badge severity="info" size="sm">leader</Badge>}
            <span className="text-xs text-theme-text-tertiary">
              {p.state === 'ok' || p.state === 'partial'
                ? p.startedAt
                  ? `counters since the operator container started ${formatAge(p.startedAt)} ago`
                  : 'counters since the operator container last started'
                : null}
              {!p.leader ? `${p.state === 'ok' || p.state === 'partial' ? ' · ' : ''}only the leader reconciles` : ''}
            </span>
          </div>
          {p.state !== 'ok' && p.state !== 'partial' ? (
            <span className="text-xs text-theme-text-tertiary">{p.error ?? p.state}</span>
          ) : (
            <table className="mt-1 w-full max-w-lg text-left text-xs">
              <thead className="text-theme-text-tertiary">
                <tr>
                  <th className="py-0.5 font-normal">Controller</th>
                  <th className="py-0.5 font-normal">Reconciles</th>
                  <th className="py-0.5 font-normal">Errors</th>
                </tr>
              </thead>
              <tbody>
                {p.controllers.map((c) => (
                  <tr key={c.controller} className="border-t border-theme-border">
                    <td className="py-0.5 font-mono">{c.controller}</td>
                    <td className="py-0.5">{c.total === null ? '—' : Math.round(c.total).toLocaleString()}</td>
                    <td className="py-0.5">
                      {c.errors === null ? '—' : c.errors > 0 ? <span className="font-medium text-theme-text-primary">{Math.round(c.errors).toLocaleString()}</span> : '0'}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
          {p.reason && <Sub>{p.reason}</Sub>}
        </div>
      ))}
      <Sub>From controller-runtime metrics on the operator’s metrics port, read through pods/proxy. Errors are cumulative; a count that grows between refreshes is the signal.</Sub>
    </div>
  )
}

/**
 * Why a Cluster may not be reconciling, from the operator's side: leader
 * Lease, watched namespaces, webhook reachability, reconcile errors and
 * recent events. Each fact reports its own access.
 */
export function CNPGOperatorDiagnosisSection({ diagnosis, fleet }: { diagnosis: CNPGOperatorDiagnosis[]; fleet: CNPGFleet }) {
  const navigate = useNavigate()
  if (diagnosis.length === 0) return null
  return (
    <>
      {diagnosis.map((d) => {
        const warnings = d.events.items.filter((e) => e.type === 'Warning')
        return (
          <section key={`${d.namespace}/${d.deployment}`}>
            <div className="mb-2 flex flex-wrap items-baseline gap-2">
              <h2 className="text-sm font-semibold text-theme-text-primary">Operator diagnosis</h2>
              <span className="text-xs text-theme-text-tertiary">
                Deployment {d.deployment} in {d.namespace}
              </span>
              <button
                type="button"
                onClick={() => navigate(buildWorkloadPath({ kind: 'deployments', namespace: d.namespace, name: d.deployment, tab: 'logs' }))}
                className="ml-auto text-xs text-accent-text hover:underline"
              >
                Operator logs
              </button>
            </div>
            <div className="overflow-hidden rounded-xl border border-theme-border bg-theme-surface shadow-theme-sm">
              <Row label="Leader">
                <Leader d={d} />
              </Row>
              <Row label="Watched namespaces">
                <Watch d={d} fleet={fleet} />
              </Row>
              <Row label="Admission webhooks">
                <Webhooks d={d} />
              </Row>
              <Row label="Reconcile errors">
                {d.podCoverage.state !== 'ok' ? <Unread cov={d.podCoverage} what="the operator Pods" /> : <Reconcile pods={d.reconcile} />}
              </Row>
              <Row label="Recent events">
                {d.events.state !== 'ok' ? (
                  <Unread cov={d.events} what={`events in ${d.namespace}`} />
                ) : d.events.items.length === 0 ? (
                  <span className="text-theme-text-tertiary">None retained for the operator’s Deployment, ReplicaSets or Pods</span>
                ) : (
                  <ul className="space-y-1">
                    {[...warnings, ...d.events.items.filter((e) => e.type !== 'Warning')].slice(0, 8).map((e, i) => (
                      <li key={`${e.kind}/${e.name}/${e.reason}/${i}`} className="text-xs text-theme-text-secondary">
                        {e.type === 'Warning' && <Badge severity="warning" size="sm">Warning</Badge>}{' '}
                        <span className="font-medium text-theme-text-primary">{e.reason}</span> on {e.kind} {e.name}
                        {e.lastSeen ? ` · ${formatAge(e.lastSeen)} ago` : ''}
                        {e.count > 1 ? ` · ×${e.count}` : ''}: {e.message}
                      </li>
                    ))}
                  </ul>
                )}
              </Row>
            </div>
          </section>
        )
      })}
    </>
  )
}
