import { getCNPGPoolerDeploymentName, getCNPGPoolerMode, getCNPGPoolerStatus, isCNPGPoolerPaused } from '../resources/resource-utils-cnpg'
import type { CNPGWorkspaceResponse } from './workspace'
import { FactGrid, FactRow, FactValue, RefLink, SummaryHeading, type CNPGNavigate } from './primitives'
import { ClusterLink, NotReported, Note, ObjectProblems, PhaseBadge, SummaryShell } from './CNPGSharedSummary'
import { refOf } from './relations'

const TYPE_LABEL: Record<string, string> = {
  rw: 'rw · routes to the primary',
  ro: 'ro · routes to replicas',
  r: 'r · routes to any instance',
}

export function CNPGPoolerSummary({
  resource,
  workspace,
  onNavigate,
}: {
  resource: any
  workspace: CNPGWorkspaceResponse | null
  onNavigate?: CNPGNavigate
}) {
  const ns = resource?.metadata?.namespace ?? ''
  const type = resource?.spec?.type
  const desired = resource?.spec?.instances
  const scheduled = resource?.status?.instances
  const deployment = getCNPGPoolerDeploymentName(resource)

  return (
    <SummaryShell>
      <ObjectProblems issues={workspace?.issues} subject={refOf(resource, 'Pooler')} onNavigate={onNavigate} />

      <SummaryHeading>State</SummaryHeading>
      <FactGrid>
        <FactRow label="Status">
          <PhaseBadge status={getCNPGPoolerStatus(resource)} />
          {isCNPGPoolerPaused(resource) && <Note>PgBouncer is paused: it holds client connections instead of serving them.</Note>}
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
        <FactRow label="Connection pressure">
          <FactValue fact={{ text: 'Not measured — needs PgBouncer metrics, which Radar does not read yet', tone: 'unknown' }} />
        </FactRow>
      </FactGrid>

      <SummaryHeading>Routing</SummaryHeading>
      <FactGrid>
        <FactRow label="Cluster">
          <ClusterLink resource={resource} workspace={workspace} onNavigate={onNavigate} />
        </FactRow>
        <FactRow label="Type">{type ? TYPE_LABEL[type] ?? type : <NotReported text="Not set" />}</FactRow>
        <FactRow label="Pool mode">{getCNPGPoolerMode(resource)}</FactRow>
      </FactGrid>
    </SummaryShell>
  )
}
