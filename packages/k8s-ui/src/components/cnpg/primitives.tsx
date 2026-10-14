import type { ComponentProps } from 'react'
import type { ResourceRef } from '../../types/core'
import { WorkspaceProblemCallout } from '../problems'
import type { CNPGProblem } from './workspace'
import { cnpgProblemForPresentation } from './problems'
import { StatusDot } from '../ui/status-tone'

export { FactValue, FactSource, FactGrid, FactRow } from '../facts'
export { RefLink, type NavigateToRef as CNPGNavigate } from '../ui/RefLink'
export { SectionHeading as SummaryHeading } from '../ui/FoldSection'
export { toneTextClass } from '../ui/status-tone'
export type CNPGRef = ResourceRef
export { CNPG_PROBLEM_TONE, cnpgProblemForPresentation } from './problems'

export const CNPG_PRIMARY_BUTTON = 'btn-brand inline-flex items-center gap-1.5 px-3 py-1.5 text-sm font-medium'
export const CNPG_SECONDARY_BUTTON =
  'inline-flex items-center gap-1.5 rounded-lg border border-theme-border bg-theme-surface px-3 py-1.5 text-sm text-theme-text-primary transition-colors hover:bg-theme-hover'


export function ProblemCallout({ problem, ...props }: Omit<ComponentProps<typeof WorkspaceProblemCallout>, 'rootKind' | 'problem'> & { problem: CNPGProblem }) {
  return <WorkspaceProblemCallout {...props} problem={cnpgProblemForPresentation(problem)} rootKind="Cluster" />
}

export function ToneDot({ tone }: Pick<ComponentProps<typeof StatusDot>, 'tone'>) {
  return <StatusDot tone={tone} size="sm" />
}
