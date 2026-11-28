import { Package, Layers, FlaskConical } from 'lucide-react'
import { Section, PropertyList, Property, ResourceLink } from '../../ui/drawer-components'
import { TimeValue } from '../../ui/ScheduleValue'
import {
  getFreightAlias,
  getFreightStageRecords,
  getVerificationPhaseTone,
  type FreightStageRecord,
  type KargoFreight,
} from '../resource-utils-kargo'
import { FreightVersionChips, KargoLink, OpenInPipelineLink, ToneBadge, type KargoNavigate, type KargoOpenPipeline } from './KargoShared'

/** A verification run of this Freight, as a host found it. */
export interface KargoFreightVerificationRun {
  name: string
  namespace: string
  stage?: string
  phase?: string
  finishedAt?: string
}

interface KargoFreightRendererProps {
  data: any
  onNavigate?: KargoNavigate
  /**
   * Verification runs of this Freight, from a host that can read them. Left out,
   * the section is not shown; an empty list says none ran.
   */
  verificationRuns?: KargoFreightVerificationRun[]
  onOpenPipeline?: KargoOpenPipeline
}

function StageList({ records, namespace, onNavigate, empty, atLabel }: {
  records: FreightStageRecord[]
  namespace: string
  onNavigate?: KargoNavigate
  empty: string
  atLabel: string
}) {
  if (records.length === 0) return <span className="text-theme-text-secondary">{empty}</span>
  return (
    <span className="flex flex-col gap-1">
      {records.map(r => (
        <span key={r.stage} className="flex flex-wrap items-center gap-2">
          <KargoLink kind="Stage" name={r.stage} namespace={namespace} onNavigate={onNavigate} />
          {r.at && <span className="text-xs text-theme-text-tertiary">{atLabel} <TimeValue timestamp={r.at} /></span>}
        </span>
      ))}
    </span>
  )
}

export function KargoFreightRenderer({ data, onNavigate, verificationRuns, onOpenPipeline }: KargoFreightRendererProps) {
  const freight = data as KargoFreight
  const namespace = freight.metadata?.namespace ?? ''
  const alias = getFreightAlias(freight)
  const records = getFreightStageRecords(freight)
  const commits = (freight.commits ?? []).filter(c => c?.message || c?.author)

  return (
    <>
      <OpenInPipelineLink onOpenPipeline={onOpenPipeline} stage={records.currentlyIn.length === 1 ? records.currentlyIn[0].stage : undefined} />
      <div className="card-inner-lg space-y-2">
        <div className="text-base font-medium text-theme-text-primary">{alias ?? 'No alias'}</div>
        <FreightVersionChips freight={freight} />
        {commits.map((c, i) => (
          <div key={`${c.id ?? i}`} className="text-xs text-theme-text-secondary">
            <span className="font-mono">{(c.id ?? '').slice(0, 7)}</span>
            {c.message && <span> {c.message.split('\n')[0]}</span>}
            {c.author && <span className="text-theme-text-tertiary"> by {c.author}</span>}
          </div>
        ))}
      </div>

      <Section title="Origin" icon={Package} defaultExpanded>
        <PropertyList>
          <Property
            label={freight.origin?.kind || 'Origin'}
            value={freight.origin?.name
              ? <KargoLink kind="Warehouse" name={freight.origin.name} namespace={namespace} onNavigate={onNavigate} />
              : <span className="text-theme-text-tertiary">Not recorded</span>}
          />
          <Property label="Discovered" value={<TimeValue timestamp={freight.discoveredAt ?? freight.metadata?.creationTimestamp} />} />
        </PropertyList>
      </Section>

      <Section title="Stages" icon={Layers} defaultExpanded>
        <div className="mb-2 text-xs text-theme-text-tertiary">As Kargo records them on this Freight.</div>
        <PropertyList>
          <Property label="Currently in" value={<StageList records={records.currentlyIn} namespace={namespace} onNavigate={onNavigate} empty="Not in any stage yet" atLabel="since" />} />
          <Property label="Verified in" value={<StageList records={records.verifiedIn} namespace={namespace} onNavigate={onNavigate} empty="Not verified in any stage yet" atLabel="verified" />} />
          <Property label="Approved for" value={<StageList records={records.approvedFor} namespace={namespace} onNavigate={onNavigate} empty="Not approved for any stage" atLabel="approved" />} />
        </PropertyList>
      </Section>

      {verificationRuns && (
        <Section title={`Verification runs (${verificationRuns.length})`} icon={FlaskConical} defaultExpanded>
          {verificationRuns.length === 0 ? (
            <div className="text-sm text-theme-text-secondary">No verification runs found in recent stage history.</div>
          ) : (
            <div className="space-y-1.5">
              {verificationRuns.map(run => (
                <div key={`${run.namespace}/${run.name}`} className="flex flex-wrap items-center gap-2 text-sm">
                  <ToneBadge size="sm" tone={getVerificationPhaseTone(run.phase)}>{run.phase ?? 'Unknown'}</ToneBadge>
                  <ResourceLink kind="AnalysisRun" group="argoproj.io" name={run.name} namespace={run.namespace} onNavigate={onNavigate} />
                  {run.stage && <span className="text-xs text-theme-text-tertiary">in {run.stage}</span>}
                  {run.finishedAt && <span className="text-xs text-theme-text-tertiary"><TimeValue timestamp={run.finishedAt} /></span>}
                </div>
              ))}
            </div>
          )}
        </Section>
      )}
    </>
  )
}
