import { clsx } from 'clsx'
import { Badge } from '../ui/Badge'
import { Tooltip } from '../ui/Tooltip'
import { formatAge } from '../resources/resource-utils'
import { PrimaryConflictNote } from './primitives'
import {
  CNPG_ROLE_DETAIL_TEXT,
  cnpgCertificateViews,
  cnpgCertificatesSummary,
  cnpgHASourceText,
  cnpgHASummary,
  cnpgImageDrift,
  cnpgLeaseHolderPod,
  cnpgLiveGap,
  cnpgPDBFact,
  cnpgPendingRestart,
  cnpgQuorumFact,
  cnpgZoneSpread,
  type CNPGClusterHA,
  type CNPGHAJob,
  type CNPGHALease,
  type CNPGInstanceLive,
} from './ha'
import { type NavigateToRef, RefLink } from '../ui/RefLink'
import { StatusDot, toneTextClass } from '../ui/status-tone'
import { FactGrid, FactRow, FactValue, FoldSection, SectionHeading } from '../workspace'

function Unknown({ text }: { text: string }) {
  return <span className="text-theme-text-tertiary">{text}</span>
}

function LeaseValue({ lease, what }: { lease: CNPGHALease; what: string }) {
  if (lease.state !== 'ok') return <Unknown text={cnpgHASourceText(lease, what)} />
  return (
    <span>
      held by{' '}
      {lease.holder && cnpgLeaseHolderPod(lease.holder) !== lease.holder ? (
        <Tooltip content={lease.holder}>
          <span className="font-mono break-all">{cnpgLeaseHolderPod(lease.holder)}</span>
        </Tooltip>
      ) : (
        <span className="font-mono break-all">{lease.holder || '(nobody)'}</span>
      )}
      {lease.renewTime && <span className="text-theme-text-secondary"> · renewed {formatAge(lease.renewTime)} ago</span>}
      {lease.expired && <span className={toneTextClass('degraded')}> · expired</span>}
      {lease.controlledByCluster === false && <span className={toneTextClass('degraded')}> · not owned by this Cluster</span>}
    </span>
  )
}

const JOB_SEVERITY: Record<CNPGHAJob['phase'], 'success' | 'error' | 'info' | 'neutral'> = {
  succeeded: 'success',
  failed: 'error',
  running: 'info',
  pending: 'neutral',
}

/**
 * "HA and instances": whether the cluster survives losing an instance, and what
 * a planned switchover will meet. Every row names its source; a fact the caller
 * cannot read says so instead of reading as none.
 */
export function CNPGClusterHASection({
  ha,
  live,
  liveUnavailable,
  loading,
  error,
  onNavigate,
  primaryConflict,
}: {
  /** status.currentPrimary vs the Pod labelled primary, when they disagree. */
  primaryConflict?: { status: string; labelled: string }
  ha?: CNPGClusterHA
  /** Instance-manager facts, when the caller can read them. */
  live?: CNPGInstanceLive[]
  /** Why `live` is absent (e.g. "needs get pods/proxy in db"); read from `live` itself when it is present. */
  liveUnavailable?: string
  loading?: boolean
  error?: string
  onNavigate?: NavigateToRef
}) {
  if (!ha) {
    return (
      <>
        <SectionHeading>HA and instances</SectionHeading>
        <div className="text-sm text-theme-text-tertiary">{loading ? 'Reading HA facts…' : `HA facts could not be read${error ? `: ${error}` : ''}`}</div>
      </>
    )
  }
  const ns = ha.cluster.namespace
  const spread = cnpgZoneSpread(ha)
  const drift = cnpgImageDrift(ha)
  const pending = cnpgPendingRestart(live)
  const certs = cnpgCertificateViews(ha.certificates)
  const liveBy = new Map((live ?? []).map((l) => [l.pod, l]))
  const jobs = [...ha.jobs.items].sort((a, b) => (a.phase === 'succeeded' ? 1 : 0) - (b.phase === 'succeeded' ? 1 : 0))
  const versions = new Set((live ?? []).map((l) => l.instanceManagerVersion).filter(Boolean))

  const haSummary = cnpgHASummary(ha, live, primaryConflict)
  const certSummary = cnpgCertificatesSummary(ha.certificates)

  return (
    <>
      <FoldSection title="HA and instances" hint={`sampled ${formatAge(ha.sampledAt)} ago`} summary={haSummary.text} attention={haSummary.attention}>
        <FactGrid>
          <FactRow label="Failure domains">
            {!spread.known ? (
              <div>
                <Unknown text={ha.pods.state !== 'ok' ? cnpgHASourceText(ha.pods, 'instance Pods') : cnpgHASourceText(ha.nodes, 'Nodes (zones)')} />
                {spread.nodes.length > 0 && (
                  <div className="text-xs text-theme-text-secondary">
                    Nodes: {spread.nodes.map((n) => `${n.node} (${n.pods.join(', ')})`).join(' · ')}
                  </div>
                )}
              </div>
            ) : (
              <div>
                <div className="flex flex-wrap gap-x-3">
                  {spread.zones.map((z) => (
                    <span key={z.zone}>
                      <span className="font-mono">{z.zone}</span>
                      <span className="text-theme-text-secondary">: {z.pods.join(', ')}</span>
                    </span>
                  ))}
                  {spread.unlabelled.length > 0 && <Unknown text={`no zone label: ${spread.unlabelled.join(', ')}`} />}
                </div>
                {(spread.singleZone || spread.sharedNode) && (
                  <div className={clsx('text-xs', toneTextClass('degraded'))}>
                    {spread.singleZone ? 'Every instance is in one zone: losing it loses the cluster. ' : ''}
                    {spread.sharedNode ? 'Two or more instances share a Node.' : ''}
                  </div>
                )}
                <div className="text-[11.5px] text-theme-text-tertiary">topology.kubernetes.io/zone of each instance’s Node</div>
              </div>
            )}
          </FactRow>

          <FactRow label="Instances">
            {ha.pods.state !== 'ok' ? (
              <Unknown text={cnpgHASourceText(ha.pods, 'instance Pods')} />
            ) : (
              <div className="space-y-1">
                {ha.instances.map((i) => {
                  const l = liveBy.get(i.pod)
                  return (
                    <div key={i.pod} className="flex flex-wrap items-center gap-x-2 gap-y-0.5 text-xs">
                      <StatusDot tone={i.ready ? 'healthy' : 'unhealthy'} />
                      <RefLink refTo={{ kind: 'Pod', group: '', namespace: ns, name: i.pod }} onNavigate={onNavigate} mono />
                      <span className="text-theme-text-secondary">
                        {l?.roleDetail ? CNPG_ROLE_DETAIL_TEXT[l.roleDetail] : i.role === 'unknown' ? 'role unknown' : i.role}
                      </span>
                      {l?.timeline !== undefined && <span className="text-theme-text-tertiary">TL {l.timeline}</span>}
                      {i.qosClass && <span className="text-theme-text-tertiary">QoS {i.qosClass}</span>}
                      {l?.pendingRestart && <Badge severity="warning" size="sm">pending restart</Badge>}
                      {i.imageMatches === false && <Badge severity="warning" size="sm">image differs</Badge>}
                      {l?.instanceManagerVersion && versions.size > 1 && <span className="text-theme-text-tertiary">manager {l.instanceManagerVersion}</span>}
                    </div>
                  )
                })}
                {primaryConflict && <PrimaryConflictNote conflict={primaryConflict} />}
                <div className="text-[11.5px] text-theme-text-tertiary">
                  {live ? 'Role detail and pending restart from each instance manager' : `Role from Pod labels; role detail ${cnpgLiveGap(undefined, liveUnavailable)}`}
                  {versions.size === 1 ? ` · instance manager ${[...versions][0]}` : ''}
                </div>
              </div>
            )}
          </FactRow>

          <FactRow label="Pending restart">
            {!pending.known && pending.pods.length === 0 ? (
              <Unknown text={`Unknown: ${cnpgLiveGap(live, liveUnavailable)}`} />
            ) : pending.pods.length === 0 ? (
              <span className="text-theme-text-secondary">None reported</span>
            ) : (
              <span className={toneTextClass('degraded')}>
                {pending.pods.join(', ')} {pending.pods.length === 1 ? 'needs' : 'need'} a restart to apply changed parameters
                {pending.forDecrease ? ' (a lowered setting: the primary restarts first)' : ''}
                {!pending.known ? ` · ${cnpgLiveGap(live, liveUnavailable)}` : ''}
              </span>
            )}
          </FactRow>

          <FactRow label="Image">
            {!drift.known ? (
              <Unknown text={ha.desiredImage ? 'Running images unknown (Pods not readable)' : 'Desired image not reported'} />
            ) : drift.drifted.length === 0 ? (
              <span>
                <span className="font-mono text-xs break-all">{ha.desiredImage}</span>
                <span className="text-theme-text-secondary"> · every instance runs it</span>
              </span>
            ) : (
              <span className={toneTextClass('degraded')}>
                {drift.drifted.map((d) => `${d.pod} runs ${d.image}`).join(' · ')} · desired <span className="font-mono">{ha.desiredImage}</span>
              </span>
            )}
          </FactRow>

          <FactRow label="Failover quorum">
            <FactValue fact={cnpgQuorumFact(ha.quorum)} />
          </FactRow>

          <FactRow label="Disruption budgets">
            <FactValue fact={cnpgPDBFact(ha.pdbs)} />
          </FactRow>

          <FactRow label="Primary lease">
            <LeaseValue lease={ha.primaryLease} what="the primary Lease" />
          </FactRow>

          <FactRow label="Operator leader">
            <LeaseValue lease={ha.operatorLease} what="the operator’s Lease" />
          </FactRow>

          <FactRow label="Cluster Jobs">
            {ha.jobs.state !== 'ok' ? (
              <Unknown text={cnpgHASourceText(ha.jobs, 'Jobs')} />
            ) : jobs.length === 0 ? (
              <span className="text-theme-text-secondary">None present</span>
            ) : (
              <div className="space-y-0.5">
                {jobs.slice(0, 6).map((j) => (
                  <div key={j.name} className="flex flex-wrap items-center gap-2 text-xs">
                    <Badge severity={JOB_SEVERITY[j.phase]} size="sm">{j.phase}</Badge>
                    <span className="text-theme-text-secondary">{j.role ?? 'job'}</span>
                    <RefLink refTo={{ kind: 'Job', group: 'batch', namespace: ns, name: j.name }} onNavigate={onNavigate} mono />
                    {j.reason && <span className={toneTextClass('degraded')}>{j.reason}</span>}
                  </div>
                ))}
                {jobs.length > 6 && <div className="text-xs text-theme-text-tertiary">+{jobs.length - 6} more</div>}
              </div>
            )}
          </FactRow>
        </FactGrid>
      </FoldSection>

      <FoldSection title="Certificates" summary={certSummary.text} attention={certSummary.attention}>
        <FactGrid>
          <FactRow label="Expiry">
            {certs.length === 0 ? (
              <Unknown text="No expiry reported by the operator" />
            ) : (
              <div className="space-y-0.5">
                {certs.map((c) => (
                  <div key={c.secret} className="text-xs">
                    <RefLink refTo={{ kind: 'Secret', group: '', namespace: ns, name: c.secret }} onNavigate={onNavigate} mono />{' '}
                    <span className={toneTextClass(c.tone)}>
                      {c.expiresAt ? (c.daysLeft !== undefined && c.daysLeft < 0 ? `expired ${c.expiresAt}` : `expires in ${c.daysLeft} d`) : `expiry unreadable (“${c.raw}”)`}
                    </span>
                    <span className="text-theme-text-secondary">
                      {' · '}
                      {c.renewal === 'operator' ? 'CloudNativePG renews it' : 'you renew it (spec.certificates)'}
                    </span>
                    {c.renewal === 'user' &&
                      (c.certManager ? (
                        <span className="text-theme-text-secondary">
                          {' · cert-manager '}
                          <RefLink refTo={{ kind: 'Certificate', group: 'cert-manager.io', namespace: ns, name: c.certManager.certificate }} onNavigate={onNavigate} mono />
                        </span>
                      ) : c.metadata?.state === 'ok' ? (
                        <span className="text-theme-text-tertiary"> · not issued by cert-manager</span>
                      ) : (
                        <span className="text-theme-text-tertiary"> · issuer unknown ({cnpgHASourceText(c.metadata, 'Secret metadata')})</span>
                      ))}
                  </div>
                ))}
                <div className="text-[11.5px] text-theme-text-tertiary">status.certificates.expirations</div>
              </div>
            )}
          </FactRow>
        </FactGrid>
      </FoldSection>
    </>
  )
}
