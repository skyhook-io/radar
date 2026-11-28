import { Boxes, GitBranch, Activity, AppWindow } from 'lucide-react'
import { Section, PropertyList, Property, ConditionsSection, AlertBanner, ResourceLink, useOperationalIssuesShown } from '../../ui/drawer-components'
import { TimeValue } from '../../ui/ScheduleValue'
import { FactValue } from '../../facts/facts'
import {
  getFreightSince,
  getStageArgoCDApps,
  getStageCurrentFreight,
  getStageCurrentPromotion,
  getStageHealth,
  getStageLastPromotion,
  getStageSources,
  getStageVerification,
  formatFreightLabel,
  formatGoDurationWords,
  isControlFlowStage,
  type KargoFreight,
  type KargoStage,
  type StageFreight,
} from '../resource-utils-kargo'
import { FreightNameLink, FreightVersionChips, KargoLink, OpenInPipelineLink, ToneBadge, kargoConditionTone, type KargoNavigate, type KargoOpenPipeline } from './KargoShared'

interface KargoStageRendererProps {
  data: any
  onNavigate?: KargoNavigate
  /**
   * Freight objects by name, from a host that can read them. Adds each current
   * Freight's alias and since when the Stage has held it. Without it those two
   * read as unknown; everything else comes from the Stage alone.
   */
  freights?: Record<string, KargoFreight | undefined>
  onOpenPipeline?: KargoOpenPipeline
}

function SinceValue({ item, stageName, freights }: { item: StageFreight; stageName: string; freights?: Record<string, KargoFreight | undefined> }) {
  const freight = freights?.[item.name]
  const since = freight ? getFreightSince(freight, stageName) : undefined
  if (since) return <TimeValue timestamp={since} />
  return (
    <FactValue
      fact={{
        text: 'Unknown',
        tone: 'unknown',
        detail: freight ? 'Kargo has not recorded when this Stage received it.' : 'Kargo records this on the Freight, which is not loaded here.',
      }}
    />
  )
}

export function KargoStageRenderer({ data, onNavigate, freights, onOpenPipeline }: KargoStageRendererProps) {
  const stage = data as KargoStage
  const namespace = stage.metadata?.namespace ?? ''
  const stageName = stage.metadata?.name ?? ''
  const issueShown = useOperationalIssuesShown()

  const current = getStageCurrentFreight(stage, freights)
  const health = getStageHealth(stage)
  const verification = getStageVerification(stage)
  const last = getStageLastPromotion(stage)
  const running = getStageCurrentPromotion(stage)
  const sources = getStageSources(stage)
  const apps = getStageArgoCDApps(stage)
  const controlFlow = isControlFlowStage(stage)
  const latestVerification = verification.latest

  const lastFailed = !running && (last?.phase === 'Failed' || last?.phase === 'Errored')
  const verificationFailed = latestVerification?.phase === 'Failed' || latestVerification?.phase === 'Error'

  return (
    <>
      <OpenInPipelineLink onOpenPipeline={onOpenPipeline} stage={stageName || undefined} />
      {lastFailed && !issueShown && last && (
        <AlertBanner
          variant="error"
          title={last.phase === 'Errored' ? 'The last promotion to this Stage errored' : 'The last promotion to this Stage failed'}
          message={last.message ?? 'Kargo recorded no message. Open the promotion to see its steps.'}
        />
      )}
      {verificationFailed && !issueShown && (
        <AlertBanner
          variant="error"
          title="Verification of the current Freight failed"
          message={latestVerification?.message ?? 'Kargo recorded no message. Open the analysis run for the failing metric.'}
        />
      )}
      {health.status === 'Unhealthy' && !issueShown && (
        <AlertBanner
          variant="error"
          title="Kargo reports this Stage as unhealthy"
          items={health.issues.length > 0 ? health.issues : undefined}
        />
      )}

      <Section title="Current Freight" icon={Boxes} defaultExpanded>
        {current.length === 0 ? (
          <div className="text-sm text-theme-text-secondary">This Stage has not received any Freight yet.</div>
        ) : (
          <div className="space-y-2">
            {current.map((item) => (
              <div key={item.origin || item.name} className="card-inner space-y-1.5">
                <FreightVersionChips freight={item.ref} />
                <PropertyList>
                  <Property label="Freight" value={<FreightNameLink name={item.name} alias={item.alias} namespace={namespace} onNavigate={onNavigate} />} />
                  {item.origin && (
                    <Property label="From Warehouse" value={<KargoLink kind="Warehouse" name={item.origin} namespace={namespace} onNavigate={onNavigate} />} />
                  )}
                  <Property label="Here since" value={<SinceValue item={item} stageName={stageName} freights={freights} />} />
                </PropertyList>
              </div>
            ))}
          </div>
        )}
      </Section>

      <Section title="Status" icon={Activity} defaultExpanded>
        <PropertyList>
          <Property
            label="Health"
            value={health.status
              ? <ToneBadge tone={health.tone}>{health.status}</ToneBadge>
              : <FactValue fact={{ text: 'Not reported', tone: 'unknown' }} />}
          />
          {health.issues.length > 0 && health.status !== 'Unhealthy' && (
            <Property label="Health issues" value={<ul className="space-y-1">{health.issues.map((i, n) => <li key={n}>{i}</li>)}</ul>} />
          )}
          <Property
            label="Verification"
            value={!verification.configured
              ? <span className="text-theme-text-secondary">Not set up</span>
              : latestVerification
                ? (
                  <span className="flex flex-col gap-1">
                    <span className="flex flex-wrap items-center gap-2">
                      <ToneBadge tone={verification.tone}>{latestVerification.phase ?? 'Unknown'}</ToneBadge>
                      {latestVerification.analysisRun?.name && (
                        <ResourceLink
                          kind="AnalysisRun"
                          group="argoproj.io"
                          name={latestVerification.analysisRun.name}
                          namespace={latestVerification.analysisRun.namespace ?? namespace}
                          onNavigate={onNavigate}
                        />
                      )}
                      {latestVerification.finishTime && <TimeValue timestamp={latestVerification.finishTime} />}
                    </span>
                    {latestVerification.message && !verificationFailed && (
                      <span className="text-xs text-theme-text-secondary">{latestVerification.message}</span>
                    )}
                  </span>
                )
                : <FactValue fact={{ text: 'Not run yet for the current Freight', tone: 'unknown' }} />}
          />
          {running && (
            <Property
              label="Promoting now"
              value={
                <span className="flex flex-wrap items-center gap-2">
                  <KargoLink kind="Promotion" name={running.name} namespace={namespace} onNavigate={onNavigate} />
                  {running.freight && <span className="text-theme-text-secondary">{formatFreightLabel(running.freight)}</span>}
                </span>
              }
            />
          )}
          <Property
            label="Last promotion"
            value={last
              ? (
                <span className="flex flex-wrap items-center gap-2">
                  <ToneBadge tone={last.tone}>{last.phase ?? 'Not reported'}</ToneBadge>
                  <KargoLink kind="Promotion" name={last.name} namespace={namespace} onNavigate={onNavigate} />
                  {last.finishedAt && <TimeValue timestamp={last.finishedAt} />}
                </span>
              )
              : <span className="text-theme-text-secondary">None yet</span>}
          />
          {last?.freight && (
            <Property label={last.phase === 'Succeeded' ? 'Promoted Freight' : 'Freight'} value={<FreightVersionChips freight={last.freight} />} />
          )}
          <Property label="Auto-promotion" value={stage.status?.autoPromotionEnabled === true ? 'On' : 'Off'} />
          {controlFlow && <Property label="Promotion steps" value="None. Freight passes through this Stage without being deployed." />}
        </PropertyList>
      </Section>

      <Section title="Freight comes from" icon={GitBranch} defaultExpanded>
        {sources.length === 0 ? (
          <div className="text-sm text-theme-text-secondary">This Stage requests no Freight.</div>
        ) : (
          <div className="space-y-2">
            {sources.map((src, i) => (
              <div key={`${src.warehouse}-${i}`} className="card-inner">
                <PropertyList>
                  <Property label="Warehouse" value={src.warehouse ? <KargoLink kind="Warehouse" name={src.warehouse} namespace={namespace} onNavigate={onNavigate} /> : undefined} />
                  {src.direct && <Property label="Takes Freight" value="Straight from the Warehouse" />}
                  {src.upstreamStages.length > 0 && (
                    <Property
                      label={src.availabilityStrategy === 'All' ? 'After verified in all of' : 'After verified in'}
                      value={
                        <span className="flex flex-wrap gap-x-2 gap-y-1">
                          {src.upstreamStages.map(up => <KargoLink key={up} kind="Stage" name={up} namespace={namespace} onNavigate={onNavigate} />)}
                        </span>
                      }
                    />
                  )}
                  {src.requiredSoakTime && <Property label="Soak time" value={formatGoDurationWords(src.requiredSoakTime)} />}
                  {src.selectionPolicy && <Property label="Auto-promotes" value={src.selectionPolicy === 'MatchUpstream' ? 'What the upstream Stage holds' : 'Newest Freight'} />}
                </PropertyList>
              </div>
            ))}
          </div>
        )}
      </Section>

      {apps.length > 0 && (
        <Section title={`Argo CD applications (${apps.length})`} icon={AppWindow} defaultExpanded>
          <div className="space-y-1.5">
            {apps.map(app => (
              <div key={`${app.namespace}/${app.name}`} className="flex flex-wrap items-center gap-2 text-sm">
                <ResourceLink kind="Application" group="argoproj.io" name={app.name} namespace={app.namespace} onNavigate={onNavigate} />
                <span className="text-xs text-theme-text-tertiary">{app.namespace}</span>
                {app.health && <ToneBadge size="sm" tone={app.health === 'Healthy' ? 'healthy' : app.health === 'Degraded' || app.health === 'Missing' ? 'unhealthy' : app.health === 'Progressing' ? 'degraded' : app.health === 'Suspended' ? 'neutral' : 'unknown'}>{app.health}</ToneBadge>}
                {app.sync && <ToneBadge size="sm" tone={app.sync === 'Synced' ? 'healthy' : app.sync === 'OutOfSync' ? 'degraded' : 'unknown'}>{app.sync}</ToneBadge>}
                {app.healthMessage && <span className="w-full text-xs text-theme-text-secondary">{app.healthMessage}</span>}
              </div>
            ))}
          </div>
        </Section>
      )}

      <ConditionsSection conditions={stage.status?.conditions} getConditionTone={kargoConditionTone} />
    </>
  )
}
