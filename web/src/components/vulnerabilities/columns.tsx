import { useState } from 'react'
import { clsx } from 'clsx'
import { Badge } from '@skyhook-io/k8s-ui'
import { WithTooltip } from '../ui/Tooltip'
import type { VulnerabilitiesResponse, VulnerabilityView } from '../../api/vulnerabilities'

/** A table column that is off until someone turns it on, from the page's
 *  menu ("Show <label>"). */
export interface OptionalColumn {
  key: string
  label: string
  /** Says what the column holds: the column head's tooltip. */
  tip: string
  /** Track width in the table's grid. */
  track: string
  /** Whether any row of the answer has a value for it. A column with none
   *  (an older Radar, an older Trivy, a report that leaves the field out)
   *  isn't offered or drawn, so it never shows as a column of dashes. */
  hasData: (data: VulnerabilitiesResponse) => boolean
}

const CVE_RUNNING_SINCE_TIP = 'The longest any affected container has been running this exact build, so the vulnerable code has been in the cluster at least that long. A restart resets it.'
const RUNNING_SINCE_TIP = 'How long this exact image digest has been running: the earliest start of a running container on it. Restarts and rescheduling reset it, so it may have run longer.'

// Each view's optional columns, in the order they appear. Every value is also
// in that view's CSV, whatever is shown. An entry with no track isn't a grid
// column of the view's table (Package URL sits in an expanded image).
export const OPTIONAL_COLUMNS: Partial<Record<VulnerabilityView, OptionalColumn[]>> = {
  image: [
    { key: 'image.os', label: 'OS', tip: "The image's OS release, as Trivy reports it.", track: 'minmax(140px,0.8fr)', hasData: (d) => !!d.images?.some((i) => i.os) },
    { key: 'image.runningSince', label: 'Running since', tip: RUNNING_SINCE_TIP, track: '136px', hasData: (d) => !!d.images?.some((i) => i.runningSince) },
    { key: 'image.scanner', label: 'Scanner', tip: "The scanner and version that wrote the image's newest report.", track: '112px', hasData: (d) => !!d.images?.some((i) => i.scanner) },
    { key: 'image.packageUrl', label: 'Package URL', tip: "The package URL (purl) Trivy reports for each finding, in an expanded image's findings.", track: '', hasData: (d) => !!d.images?.some((i) => i.findings.some((f) => f.packageUrl)) },
  ],
  cve: [
    { key: 'cve.published', label: 'Published', tip: "When the CVE was published, from Trivy's database. Not when it was found in this cluster.", track: '92px', hasData: (d) => !!d.cves?.some((c) => c.publishedAt) },
    { key: 'cve.age', label: 'Age', tip: 'Days since the CVE was published, not since it was found in this cluster.', track: '76px', hasData: (d) => !!d.cves?.some((c) => c.publishedAt) },
    { key: 'cve.runningSince', label: 'Running since', tip: CVE_RUNNING_SINCE_TIP, track: '136px', hasData: (d) => !!d.cves?.some((c) => c.runningSince) },
  ],
  workload: [
    { key: 'workload.runningSince', label: 'Running since', tip: `${RUNNING_SINCE_TIP} For a workload, the earliest of its containers with findings.`, track: '136px', hasData: (d) => !!d.workloads?.some((w) => w.runningSince) },
  ],
}

/** A filter chip someone can add to, or remove from, the chip row from the
 *  page's menu ("Show <label> filter"). It applies in every view. */
export interface OptionalFilter {
  key: string
  label: string
  /** The URL parameter the chip sets: a link that carries it shows the chip
   *  whatever this browser chose, so shared links work. */
  param: string
  /** On until this browser turns it off. */
  defaultOn?: boolean
}

// The chip row's optional filters, in menu order. The view renders each chip.
export const OPTIONAL_FILTERS: OptionalFilter[] = [
  { key: 'eol', label: 'OS end of life', param: 'eol', defaultOn: true },
]

const FILTERS_KEY = 'radar-vulnerability-filters'

function readFilterChoices(): Record<string, boolean> {
  try {
    const parsed: unknown = JSON.parse(localStorage.getItem(FILTERS_KEY) ?? '{}')
    if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) return {}
    return Object.fromEntries(Object.entries(parsed).filter((e): e is [string, boolean] => typeof e[1] === 'boolean'))
  } catch {
    return {}
  }
}

/** Which optional filters this browser shows in the chip row: its own
 *  choice, else the filter's default. */
export function useOptionalFilters() {
  const [choices, setChoices] = useState(readFilterChoices)
  const shown = (key: string) => choices[key] ?? !!OPTIONAL_FILTERS.find((f) => f.key === key)?.defaultOn
  const toggle = (key: string) => {
    const next = { ...choices, [key]: !shown(key) }
    setChoices(next)
    try {
      localStorage.setItem(FILTERS_KEY, JSON.stringify(next))
    } catch {
      // Private windows and blocked storage: the choice lasts this visit.
    }
  }
  return { shown, toggle }
}

const STORAGE_KEY = 'radar-vulnerability-columns'

function readShown(): Set<string> {
  try {
    const parsed: unknown = JSON.parse(localStorage.getItem(STORAGE_KEY) ?? '[]')
    return new Set(Array.isArray(parsed) ? parsed.filter((k): k is string => typeof k === 'string') : [])
  } catch {
    return new Set()
  }
}

/** The optional columns this browser shows, across views; none by default. */
export function useOptionalColumns() {
  const [shown, setShown] = useState(readShown)
  const toggle = (key: string) => {
    const next = new Set(shown)
    if (next.has(key)) next.delete(key)
    else next.add(key)
    setShown(next)
    try {
      localStorage.setItem(STORAGE_KEY, JSON.stringify([...next]))
    } catch {
      // Private windows and blocked storage: the choice lasts this visit.
    }
  }
  return { shown, toggle }
}

/** The view's optional columns this answer has data for. */
export function availableColumns(view: VulnerabilityView, data: VulnerabilitiesResponse): OptionalColumn[] {
  return (OPTIONAL_COLUMNS[view] ?? []).filter((c) => c.hasData(data))
}

/** The view's optional columns, each with whether it is drawn: on in this
 *  browser, and with data in this answer. */
export function columnsFor(view: VulnerabilityView, shown: ReadonlySet<string>, data: VulnerabilitiesResponse): Record<string, boolean> {
  return Object.fromEntries((OPTIONAL_COLUMNS[view] ?? []).map((c) => [c.key.split('.')[1], shown.has(c.key) && c.hasData(data)]))
}

/** A column head with its tooltip, underlined to show there is one. */
export function OptionalHead({ column, align }: { column?: OptionalColumn; align?: 'right' }) {
  if (!column) return null
  return (
    <WithTooltip tip={column.tip}>
      <span className={clsx('underline decoration-dotted underline-offset-2', align === 'right' && 'text-right')}>{column.label}</span>
    </WithTooltip>
  )
}

export function optionalColumn(view: VulnerabilityView, name: string): OptionalColumn | undefined {
  return OPTIONAL_COLUMNS[view]?.find((c) => c.key === `${view}.${name}`)
}

/** Whole days since a timestamp, never negative. */
export function daysSince(iso: string, now = Date.now()): number | undefined {
  const t = Date.parse(iso)
  return Number.isFinite(t) ? Math.max(0, Math.floor((now - t) / 86_400_000)) : undefined
}

/** A timestamp's calendar date, as YYYY-MM-DD in UTC. */
export function isoDate(iso: string): string {
  const t = Date.parse(iso)
  return Number.isFinite(t) ? new Date(t).toISOString().slice(0, 10) : iso
}

/**
 * A Running since cell: a lower bound, so it reads "at least" a date, and
 * nothing when no running container reports the scanned digest.
 */
export function RunningSince({ since, cve = false }: { since?: string; cve?: boolean }) {
  if (!since) return <span />
  const date = isoDate(since)
  const tip = cve
    ? `Running since at least ${date}: the longest any affected container has been running this exact build, so the vulnerable code has been in the cluster at least that long. A container restart or a rescheduled Pod resets it.`
    : `Running since at least ${date}, the earliest start of a running container on this exact image digest. A container restart or a rescheduled Pod resets it, so it may have run longer.`
  return (
    <WithTooltip tip={tip}>
      <span className="truncate font-mono text-xs tabular-nums text-theme-text-secondary">
        <span className="font-sans text-theme-text-tertiary">at least </span>{date}
      </span>
    </WithTooltip>
  )
}

/** Trivy's end-of-life word for an OS, as a small badge. */
export function EndOfLifeBadge() {
  return (
    <WithTooltip tip="Trivy reports this OS release as end of life.">
      <span className="inline-flex shrink-0 font-sans"><Badge severity="warning" size="sm">OS end of life</Badge></span>
    </WithTooltip>
  )
}
