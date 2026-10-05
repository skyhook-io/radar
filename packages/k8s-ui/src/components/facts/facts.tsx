import type { ReactNode } from 'react'
import { clsx } from 'clsx'
import type { HealthLevel } from '../resources/resource-utils'
import { formatAge } from '../resources/resource-utils'
import { StatusDot, toneTextClass } from '../ui/status-tone'
import { Tooltip } from '../ui/Tooltip'

/**
 * One observed value and where it came from. A value the cluster does not
 * report is a fact too: its text says so and its tone is `unknown`, never a
 * zero or a calm default.
 */
export interface Fact {
  text: string
  tone: HealthLevel
  /** Where the value comes from, available inline or on hover so claims carry their source. */
  source?: string
  /** A timestamp the text refers to; the UI renders it as an age. */
  at?: string
  /** `since`: `at` is when a still-current state began, rendered "Failing for 2d" rather than "· 2d ago". */
  atMeaning?: 'since'
  /** The full explanation behind a short `source`, shown on hover only. */
  detail?: string
}

export function FactValue({ fact, className, showStatusDot = false }: { fact: Fact; className?: string; showStatusDot?: boolean }) {
  const age = fact.at ? formatAge(fact.at) : null
  const body = (
    <span className={clsx(toneTextClass(fact.tone), showStatusDot && 'inline-flex items-center gap-1.5', className)}>
      {showStatusDot && <StatusDot tone={fact.tone} />}
      {fact.text}
      {age && fact.atMeaning === 'since' && <span className="text-theme-text-secondary"> for {age}</span>}
      {age && fact.atMeaning !== 'since' && <span className="text-theme-text-secondary">{fact.text ? ' · ' : ''}{age} ago</span>}
    </span>
  )
  if (!fact.source && !fact.at && !fact.detail) return body
  return (
    <Tooltip content={[fact.at ? `${fact.atMeaning === 'since' ? 'since ' : ''}${new Date(fact.at).toUTCString()}` : null, fact.source, fact.detail].filter(Boolean).join(' · ')} position="top">
      {body}
    </Tooltip>
  )
}

export function FactSource({ fact }: { fact: Fact }) {
  if (!fact.source) return null
  return <div className="mt-0.5 text-[11.5px] text-theme-text-tertiary">{fact.source}</div>
}

/** Label/value rows. Empty values stay visible: an unread value is shown as unread, not hidden. */
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
