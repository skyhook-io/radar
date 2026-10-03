import type { ReactNode } from 'react'
import { getCNPGPoolerDeploymentName, getCNPGPoolerMode, getCNPGPoolerStatus, isCNPGPoolerPaused } from '../resources/resource-utils-cnpg'
import type { CNPGWorkspaceResponse } from './workspace'
import { ClusterLink, NotReported, Note, ObjectProblems, PhaseBadge, SummaryShell } from './CNPGSharedSummary'
import { refOf } from './relations'
import {
  POOLER_LIMIT_PARAMETERS,
  aggregatePoolerPools,
  poolerPodPressure,
  observedPause,
  poolerBackendService,
  poolerReadiness,
  type CNPGPoolerLive,
  type CNPGPoolerPoolRow,
} from './pooler'
import { type NavigateToRef, RefLink } from '../ui/RefLink'
import { toneTextClass } from '../ui/status-tone'
import { FactGrid, FactRow, FactValue, SectionHeading } from '../workspace'

const TYPE_LABEL: Record<string, string> = {
  rw: 'rw · routes to the primary',
  ro: 'ro · routes to replicas',
  r: 'r · routes to any instance',
}

export function CNPGPoolerSummary({
  resource,
  workspace,
  onNavigate,
  live,
  actions,
  lead,
}: {
  resource: any
  workspace: CNPGWorkspaceResponse | null
  onNavigate?: NavigateToRef
  /** Live reads a host adds (Deployment readiness, PgBouncer metrics and state). */
  live?: CNPGPoolerLive
  /** Operations rendered beside the paused state (pause / resume). */
  actions?: ReactNode
  /** Rendered first, e.g. a host's notice that a live read is stale. */
  lead?: ReactNode
}) {
  const ns = resource?.metadata?.namespace ?? ''
  const type = resource?.spec?.type
  const desired = resource?.spec?.instances
  const scheduled = resource?.status?.instances
  const deployment = getCNPGPoolerDeploymentName(resource)
  const paused = isCNPGPoolerPaused(resource)
  const readiness = live?.deployment ? poolerReadiness(live.deployment) : null
  const observed = observedPause(live?.observed)

  return (
    <SummaryShell>
      {lead}
      <ObjectProblems issues={workspace?.issues} subject={refOf(resource, 'Pooler')} onNavigate={onNavigate} />

      <SectionHeading>State</SectionHeading>
      <FactGrid>
        <FactRow label="Status">
          {readiness ? (
            <>
              <PhaseBadge status={{ text: paused ? 'Paused' : readiness.text, color: '', level: paused ? 'degraded' : readiness.level }} />
              {readiness.detail && (
                <Note>
                  {paused ? `${readiness.text} · ` : ''}
                  {readiness.detail}
                </Note>
              )}
            </>
          ) : (
            <PhaseBadge status={getCNPGPoolerStatus(resource)} />
          )}
        </FactRow>
        <FactRow label="Instances">
          <span>
            {typeof scheduled === 'number' ? `${scheduled} scheduled` : <NotReported text="Scheduled count not reported" />}
            <span className="text-theme-text-secondary">
              {' · '}
              {typeof desired === 'number' ? `${desired} desired` : 'desired not set'}
            </span>
          </span>
          <Note>The Pooler counts scheduled pods, not ready ones; readiness is on its Deployment.</Note>
        </FactRow>
        <FactRow label="Deployment">
          {deployment ? (
            <RefLink refTo={{ kind: 'Deployment', group: 'apps', namespace: ns, name: deployment }} onNavigate={onNavigate} mono />
          ) : (
            <NotReported />
          )}
        </FactRow>
        <FactRow label="Pause state">
          <div className="flex flex-wrap items-center gap-x-3 gap-y-1">
            <span>{paused ? 'Requested: Paused' : 'Requested: Serving (not paused)'}</span>
            {actions}
          </div>
          <Note>spec.pgbouncer.paused is what was asked for; each PgBouncer applies it with PAUSE / RESUME.</Note>
          {paused && <Note>While paused, PgBouncer holds client connections instead of serving them.</Note>}
          {observed && (
            <div className="mt-1">
              <FactValue fact={{ text: `Observed: ${observed.text}`, tone: observed.level, source: 'Each PgBouncer’s SHOW STATE' }} />
            </div>
          )}
        </FactRow>
      </FactGrid>

      <SectionHeading hint={live?.pressure ? 'live from each PgBouncer' : undefined}>Connections</SectionHeading>
      {live?.pressure ? (
        <PoolerPressure pressure={live.pressure} />
      ) : (
        <FactGrid>
          <FactRow label="Connection pressure">
            <FactValue fact={{ text: 'Not measured — needs PgBouncer metrics, which Radar does not read yet', tone: 'unknown' }} />
          </FactRow>
        </FactGrid>
      )}

      <SectionHeading>Limits</SectionHeading>
      <FactGrid>
        <FactRow label="Pool mode">
          {resource?.spec?.pgbouncer?.poolMode ? getCNPGPoolerMode(resource) : <span>session <span className="text-theme-text-tertiary">(default)</span></span>}
        </FactRow>
        {POOLER_LIMIT_PARAMETERS.map((p) => {
          const v = resource?.spec?.pgbouncer?.parameters?.[p.key]
          return (
            <FactRow key={p.key} label={p.label}>
              {v !== undefined ? (
                <span className="font-mono">{String(v)}</span>
              ) : (
                <span className="text-theme-text-secondary">
                  default{p.pgbouncerDefault ? <span className="text-theme-text-tertiary"> · PgBouncer uses {p.pgbouncerDefault}</span> : null}
                </span>
              )}
              <Note>
                <span className="font-mono">{p.key}</span>
              </Note>
            </FactRow>
          )
        })}
      </FactGrid>

      <SectionHeading>Routing</SectionHeading>
      <FactGrid>
        <FactRow label="Cluster">
          <ClusterLink resource={resource} workspace={workspace} onNavigate={onNavigate} />
        </FactRow>
        <FactRow label="Type">{type ? TYPE_LABEL[type] ?? type : <NotReported text="Not set" />}</FactRow>
        {live?.service && (
          <FactRow label="Path">
            <PoolerPath resource={resource} live={live} onNavigate={onNavigate} />
          </FactRow>
        )}
      </FactGrid>
    </SummaryShell>
  )
}

function PoolerPath({ resource, live, onNavigate }: { resource: any; live: CNPGPoolerLive; onNavigate?: NavigateToRef }) {
  const ns = resource?.metadata?.namespace ?? ''
  const svc = live.service!
  const backend = poolerBackendService(resource?.spec?.cluster?.name, resource?.spec?.type)
  const svcState: Record<string, string> = { missing: 'does not exist', unreadable: 'no access', foreign: 'not controlled by this Pooler' }
  return (
    <div className="space-y-0.5">
      <div>
        Service <RefLink refTo={{ kind: 'Service', namespace: ns, name: svc.name }} onNavigate={onNavigate} mono />
        {svc.state === 'ok' ? (
          <span className="text-theme-text-secondary">{svc.port ? ` :${svc.port}` : ''}{svc.type ? ` · ${svc.type}` : ''}</span>
        ) : (
          <span className={toneTextClass('unknown')}> · {svcState[svc.state]}</span>
        )}
      </div>
      <div className="text-theme-text-secondary">→ PgBouncer ({resource?.metadata?.name})</div>
      <div className="text-theme-text-secondary">
        → {backend ? <RefLink refTo={{ kind: 'Service', namespace: ns, name: backend }} onNavigate={onNavigate} mono /> : 'the cluster'}
        {' '}of Cluster {resource?.spec?.cluster?.name ?? '—'}
      </div>
    </div>
  )
}

function num(v: number | undefined): string {
  return v === undefined ? '—' : String(v)
}

// Pods take their client connections independently, so one can queue while
// the sum still looks calm.
function PoolerPodPressure({ pods }: { pods: NonNullable<CNPGPoolerLive['pressure']>['pods'] }) {
  return (
    <table className="mt-3 w-full text-sm">
      <thead className="text-left text-[11px] uppercase tracking-wide text-theme-text-tertiary">
        <tr>
          <th className="py-1 pr-3">PgBouncer Pod</th>
          <th className="pr-3 text-right">Clients active</th>
          <th className="pr-3 text-right">Waiting</th>
          <th className="pr-3 text-right">Servers active</th>
          <th className="text-right">Max wait</th>
        </tr>
      </thead>
      <tbody className="table-divide-subtle">
        {poolerPodPressure(pods).map((r) =>
          r.state === 'ok' || r.state === 'partial' ? (
            <tr key={r.pod}>
              <td className="py-1 pr-3 font-mono text-xs">
                {r.pod}
                {r.state === 'partial' && <span className="ml-1 font-sans text-theme-text-tertiary">partial</span>}
              </td>
              <td className="pr-3 text-right font-mono">{num(r.clActive)}</td>
              <td className={`pr-3 text-right font-mono ${r.clWaiting ? toneTextClass('degraded') : ''}`}>{num(r.clWaiting)}</td>
              <td className="pr-3 text-right font-mono">{num(r.svActive)}</td>
              <td className={`text-right font-mono ${r.maxwaitSeconds ? toneTextClass('degraded') : ''}`}>{r.maxwaitSeconds !== undefined ? `${r.maxwaitSeconds.toFixed(1)} s` : '—'}</td>
            </tr>
          ) : (
            <tr key={r.pod}>
              <td className="py-1 pr-3 font-mono text-xs">{r.pod}</td>
              <td colSpan={4} className="text-right text-xs text-theme-text-tertiary">
                Not read: {r.error ?? r.state}
              </td>
            </tr>
          ),
        )}
      </tbody>
    </table>
  )
}

function PoolerPressure({ pressure }: { pressure: NonNullable<CNPGPoolerLive['pressure']> }) {
  if (pressure.state === 'loading') return <div className="text-sm text-theme-text-tertiary">Reading PgBouncer metrics…</div>
  if (pressure.state === 'denied' || pressure.state === 'error') {
    return <FactValue fact={{ text: `Not measured — ${pressure.reason ?? (pressure.state === 'denied' ? 'needs get pods/proxy' : 'read failed')}`, tone: 'unknown' }} />
  }
  const reporting = pressure.pods.filter((p) => p.state === 'ok' || p.state === 'partial')
  const rows = aggregatePoolerPools(reporting)
  const partial = reporting.length < pressure.pods.length || reporting.some((p) => p.state !== 'ok')
  if (reporting.length === 0) {
    return <FactValue fact={{ text: `Not measured — ${pressure.pods[0]?.error ?? 'no PgBouncer answered'}`, tone: 'unknown' }} />
  }
  return (
    <div>
      {rows.length === 0 ? (
        <div className="text-sm text-theme-text-secondary">Idle: no client pools open.</div>
      ) : (
        <table className="w-full text-sm">
          <thead className="text-left text-[11px] uppercase tracking-wide text-theme-text-tertiary">
            <tr>
              <th className="py-1 pr-3">Pool</th>
              <th className="pr-3">Mode</th>
              <th className="pr-3 text-right">Clients active</th>
              <th className="pr-3 text-right">Waiting</th>
              <th className="pr-3 text-right">Servers active / idle / used</th>
              <th className="text-right">Max wait</th>
            </tr>
          </thead>
          <tbody className="table-divide-subtle">
            {rows.map((r: CNPGPoolerPoolRow) => (
              <tr key={`${r.database}/${r.user}`}>
                <td className="py-1 pr-3 font-mono text-xs">{r.database}/{r.user}</td>
                <td className="pr-3 text-xs">{r.poolModes.join(', ') || '—'}</td>
                <td className="pr-3 text-right font-mono">{num(r.clActive)}</td>
                <td className={`pr-3 text-right font-mono ${r.clWaiting ? toneTextClass('degraded') : ''}`}>{num(r.clWaiting)}</td>
                <td className="pr-3 text-right font-mono">{num(r.svActive)} / {num(r.svIdle)} / {num(r.svUsed)}</td>
                <td className={`text-right font-mono ${r.maxwaitSeconds ? toneTextClass('degraded') : ''}`}>{r.maxwaitSeconds !== undefined ? `${r.maxwaitSeconds.toFixed(1)} s` : '—'}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
      <Note>
        Summed over {reporting.length} of {pressure.pods.length} PgBouncer Pods{partial ? ' — a lower bound: not every Pod reported in full' : ''}. PgBouncer’s admin and
        authentication pools are excluded.
      </Note>
      {pressure.pods.length > 1 && <PoolerPodPressure pods={pressure.pods} />}
    </div>
  )
}
