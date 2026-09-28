import type { ReactNode } from 'react'
import { clsx } from 'clsx'
import type { HealthLevel } from '../resources/resource-utils'
import { formatAge } from '../resources/resource-utils'
import { StatusDot } from '../ui/status-tone'
import { Tooltip } from '../ui/Tooltip'
import { AlertBanner } from '../ui/drawer-components'
import { TONE_TEXT_CLASS } from '../ui/severity-tone'
import type { CNPGFact, CNPGProblem } from './workspace'

export interface CNPGRef {
  kind: string
  group?: string
  namespace: string
  name: string
}

export type CNPGNavigate = (ref: CNPGRef) => void

const TONE_TEXT: Record<HealthLevel, string> = {
  healthy: 'text-theme-text-primary',
  degraded: TONE_TEXT_CLASS.amber,
  alert: TONE_TEXT_CLASS.orange,
  unhealthy: TONE_TEXT_CLASS.red,
  unknown: 'text-theme-text-tertiary',
  neutral: 'text-theme-text-secondary',
}

export const CNPG_PRIMARY_BUTTON = 'btn-brand inline-flex items-center gap-1.5 px-3 py-1.5 text-sm font-medium'
export const CNPG_SECONDARY_BUTTON =
  'inline-flex items-center gap-1.5 rounded-lg border border-theme-border bg-theme-surface px-3 py-1.5 text-sm text-theme-text-primary transition-colors hover:bg-theme-hover'

export function toneTextClass(tone: HealthLevel): string {
  return TONE_TEXT[tone]
}

export function FactValue({ fact, className }: { fact: CNPGFact; className?: string }) {
  const age = fact.at ? formatAge(fact.at) : null
  const body = (
    <span className={clsx(toneTextClass(fact.tone), className)}>
      {fact.text}
      {age && <span className="text-theme-text-secondary">{fact.text ? ' · ' : ''}{age} ago</span>}
    </span>
  )
  if (!fact.source && !fact.at) return body
  return (
    <Tooltip content={[fact.at ? new Date(fact.at).toUTCString() : null, fact.source].filter(Boolean).join(' · ')} position="top">
      {body}
    </Tooltip>
  )
}

export function FactSource({ fact }: { fact: CNPGFact }) {
  if (!fact.source) return null
  return <div className="mt-0.5 text-[11.5px] text-theme-text-tertiary">{fact.source}</div>
}

export function FactGrid({ children }: { children: ReactNode }) {
  return <dl className="grid grid-cols-[9.5rem_minmax(0,1fr)] gap-x-4 gap-y-2 text-sm">{children}</dl>
}

export function FactRow({ label, children }: { label: ReactNode; children: ReactNode }) {
  return (
    <>
      <dt className="text-theme-text-tertiary">{label}</dt>
      <dd className="min-w-0 break-words text-theme-text-primary">{children}</dd>
    </>
  )
}

export function SummaryHeading({ children, hint }: { children: ReactNode; hint?: ReactNode }) {
  return (
    <div className="mb-2 mt-5 flex items-baseline gap-2 first:mt-0">
      <h3 className="text-[11px] font-semibold uppercase tracking-wide text-theme-text-tertiary">{children}</h3>
      {hint && <span className="text-[11px] text-theme-text-tertiary">{hint}</span>}
    </div>
  )
}

export function RefLink({ refTo, onNavigate, children, mono }: { refTo: CNPGRef; onNavigate?: CNPGNavigate; children?: ReactNode; mono?: boolean }) {
  const label = children ?? refTo.name
  if (!onNavigate) return <span className={clsx(mono && 'font-mono')}>{label}</span>
  return (
    <button
      type="button"
      onClick={() => onNavigate(refTo)}
      className={clsx('text-accent-text hover:underline text-left break-all', mono && 'font-mono')}
    >
      {label}
    </button>
  )
}

const PROBLEM_VARIANT: Record<CNPGProblem['severity'], 'error' | 'warning' | 'info'> = {
  critical: 'error',
  warning: 'warning',
  posture: 'info',
}

export function ProblemCallout({
  problem,
  more,
  onNavigate,
  action,
}: {
  problem: CNPGProblem
  more?: ReactNode
  onNavigate?: CNPGNavigate
  action?: ReactNode
}) {
  const aboutChild = problem.subject.kind !== 'Cluster'
  return (
    <AlertBanner variant={PROBLEM_VARIANT[problem.severity]} title={problem.title} message={problem.detail}>
      <div className="mt-1.5 flex flex-wrap items-center gap-x-3 gap-y-1 text-xs text-theme-text-tertiary">
        {aboutChild && (
          <span>
            {problem.subject.kind}{' '}
            <RefLink refTo={problem.subject} onNavigate={onNavigate} mono />
          </span>
        )}
        <span>{problem.source === 'audit' ? 'Radar check' : 'Radar issue'}</span>
        {action}
        {more}
      </div>
    </AlertBanner>
  )
}

export function ToneDot({ tone }: { tone: HealthLevel }) {
  return <StatusDot tone={tone} size="sm" />
}
