import { Activity, ListOrdered } from 'lucide-react'
import { clsx } from 'clsx'
import { Section, PropertyList, Property, AlertBanner, useOperationalIssuesShown } from '../../ui/drawer-components'
import { TimeValue } from '../../ui/ScheduleValue'
import {
  getFreightAlias,
  getPromotionError,
  getPromotionStatus,
  getPromotionSteps,
  type KargoFreight,
  type KargoPromotion,
} from '../resource-utils-kargo'
import { FreightNameLink, FreightVersionChips, KargoLink, OpenInPipelineLink, ToneBadge, type KargoNavigate, type KargoOpenPipeline } from './KargoShared'

interface KargoPromotionRendererProps {
  data: any
  onNavigate?: KargoNavigate
  /** The promoted Freight object, from a host that can read it. Adds its alias. */
  freight?: KargoFreight
  onOpenPipeline?: KargoOpenPipeline
}

export function KargoPromotionRenderer({ data, onNavigate, freight, onOpenPipeline }: KargoPromotionRendererProps) {
  const promotion = data as KargoPromotion
  const namespace = promotion.metadata?.namespace ?? ''
  const status = getPromotionStatus(promotion)
  const steps = getPromotionSteps(promotion)
  const error = getPromotionError(promotion)
  const issueShown = useOperationalIssuesShown()
  const freightName = promotion.status?.freight?.name ?? promotion.spec?.freight ?? ''
  const freightContent = promotion.status?.freight
  const phase = promotion.status?.phase

  return (
    <>
      <OpenInPipelineLink onOpenPipeline={onOpenPipeline} stage={promotion.spec?.stage || undefined} />
      {error && !issueShown && (
        <AlertBanner
          variant="error"
          title={phase === 'Errored'
            ? (error.step ? `Kargo could not run step ${error.step.index + 1} (${error.step.alias})` : 'Kargo could not run this promotion')
            : (error.step ? `Step ${error.step.index + 1} (${error.step.alias}) failed` : 'This promotion failed')}
          message={error.message ?? 'Kargo recorded no message for this failure.'}
        />
      )}

      <Section title="Promotion" icon={Activity} defaultExpanded>
        <PropertyList>
          <Property label="Phase" value={<ToneBadge tone={status.level}>{phase === 'Aborted' ? 'Aborted (cancelled)' : status.text}</ToneBadge>} />
          <Property
            label="Stage"
            value={promotion.spec?.stage ? <KargoLink kind="Stage" name={promotion.spec.stage} namespace={namespace} onNavigate={onNavigate} /> : undefined}
          />
          <Property label="Freight" value={<FreightNameLink name={freightName} alias={getFreightAlias(freight)} namespace={namespace} onNavigate={onNavigate} />} />
          {freightContent && <Property label="Versions" value={<FreightVersionChips freight={freightContent} />} />}
          <Property label="Started" value={promotion.status?.startedAt ? <TimeValue timestamp={promotion.status.startedAt} /> : undefined} />
          <Property label="Finished" value={promotion.status?.finishedAt ? <TimeValue timestamp={promotion.status.finishedAt} /> : undefined} />
          {!error && <Property label="Message" value={promotion.status?.message} />}
        </PropertyList>
      </Section>

      <Section title={`Steps (${steps.length})`} icon={ListOrdered} defaultExpanded>
        {steps.length === 0 ? (
          <div className="text-sm text-theme-text-secondary">This promotion has no steps.</div>
        ) : (
          <ol className="space-y-1.5">
            {steps.map(step => {
              const failing = error?.step?.index === step.index
              return (
                <li key={step.index} className="card-inner space-y-1">
                  <div className="flex flex-wrap items-center gap-2">
                    <span className="text-xs text-theme-text-tertiary">{step.index + 1}.</span>
                    <span className="font-mono text-sm text-theme-text-primary">{step.alias}</span>
                    {step.uses && step.uses !== step.alias && <span className="font-mono text-xs text-theme-text-tertiary">{step.uses}</span>}
                    <span className="ml-auto">
                      <ToneBadge size="sm" tone={step.tone}>{step.status ?? 'Not started'}</ToneBadge>
                    </span>
                  </div>
                  {step.message && (
                    <div className={clsx('whitespace-pre-wrap break-words text-xs', failing ? 'text-theme-text-primary' : 'text-theme-text-secondary')}>{step.message}</div>
                  )}
                  <div className="flex flex-wrap gap-x-3 text-xs text-theme-text-tertiary">
                    {step.errorCount && <span>{step.errorCount} {step.errorCount === 1 ? 'error' : 'errors'}</span>}
                    {step.continueOnError && <span>continues on error</span>}
                    {step.finishedAt
                      ? <span>finished <TimeValue timestamp={step.finishedAt} /></span>
                      : step.startedAt && <span>started <TimeValue timestamp={step.startedAt} /></span>}
                  </div>
                </li>
              )
            })}
          </ol>
        )}
      </Section>
    </>
  )
}
