import { Fragment, type ReactNode } from 'react'
import { Workflow } from 'lucide-react'
import { clsx } from 'clsx'
import type { KargoStageRef } from '../../utils/kargo-authorized-stages'
import { Tooltip } from '../ui/Tooltip'

const REPLACED = 'A revision changed here can be replaced by the next promotion.'

function joinAnd(parts: ReactNode[]): ReactNode {
  return parts.map((part, i) => (
    <Fragment key={i}>
      {i > 0 && (i === parts.length - 1 ? ' and ' : ', ')}
      {part}
    </Fragment>
  ))
}

function StageName({ stage, onOpen }: { stage: KargoStageRef; onOpen?: (stage: KargoStageRef) => void }) {
  if (!onOpen) return <span className="font-medium text-theme-text-primary">{stage.stage}</span>
  return (
    <button type="button" onClick={() => onOpen(stage)} className="font-medium text-accent-text hover:underline">
      {stage.stage}
    </button>
  )
}

/**
 * The line under an Argo CD Application's title naming the Kargo Stages its
 * authorized-stage annotation lets set its version, each linking to the
 * pipeline at that stage when the host wires onOpen.
 */
export function KargoAuthorizedStageLine({ stages, onOpen, className }: {
  stages: KargoStageRef[]
  onOpen?: (stage: KargoStageRef) => void
  className?: string
}) {
  if (stages.length === 0) return null
  const projects = [...new Set(stages.map((s) => s.project))]
  const noun = stages.length === 1 ? 'stage' : 'stages'
  const names = projects.length === 1
    ? <>{joinAnd(stages.map((s) => <StageName key={`${s.project}/${s.stage}`} stage={s} onOpen={onOpen} />))} (project {projects[0]})</>
    : joinAnd(stages.map((s) => <Fragment key={`${s.project}/${s.stage}`}><StageName stage={s} onOpen={onOpen} /> (project {s.project})</Fragment>))
  return (
    <p className={clsx('flex items-start gap-1.5 text-xs text-theme-text-secondary', className)} data-testid="kargo-authorized-stage">
      <Workflow className="mt-0.5 h-3 w-3 shrink-0 text-theme-text-tertiary" aria-hidden />
      <span>
        Kargo {noun} {names} can set this app's version. {REPLACED}
      </span>
    </p>
  )
}

/**
 * One chip per Kargo Stage allowed to set the version of the Argo CD
 * Application that manages this resource, beside its "Managed by" chip.
 */
export function KargoStageChips({ stages, onOpen, variant = 'inline' }: {
  stages: KargoStageRef[]
  onOpen?: (stage: KargoStageRef) => void
  variant?: 'inline' | 'block'
}) {
  return (
    <>
      {stages.map((stage) => {
        const content = (
          <>
            <Workflow className="h-3 w-3 shrink-0" aria-hidden />
            <span className="shrink-0 text-theme-text-tertiary">Kargo stage</span>
            <span className="truncate max-w-[180px]">{stage.stage}</span>
          </>
        )
        const chipClass = clsx(
          'inline-flex min-w-0 items-center gap-1 rounded border border-theme-border bg-theme-elevated px-1.5 py-0.5 text-[11px] text-theme-text-secondary',
          onOpen && 'hover:border-accent hover:text-accent transition-colors',
        )
        return (
          <Tooltip
            key={`${stage.project}/${stage.stage}`}
            content={`Kargo stage ${stage.stage} (project ${stage.project}) can set the version of the Argo CD app that manages this. A change made here can be replaced by the next promotion.`}
            delay={500}
            position="bottom"
            wrapperClassName={variant === 'block' ? 'mt-1' : undefined}
          >
            {onOpen ? (
              <button type="button" onClick={() => onOpen(stage)} className={chipClass}>{content}</button>
            ) : (
              <span tabIndex={0} className={chipClass}>{content}</span>
            )}
          </Tooltip>
        )
      })}
    </>
  )
}
