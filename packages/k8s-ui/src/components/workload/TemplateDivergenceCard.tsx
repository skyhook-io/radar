import { useId, useState } from 'react'
import { ChevronRight, GitCompare } from 'lucide-react'
import type { ResourceRef, TemplateDivergence } from '../../types'
import { ResourceRefBadge } from '../ui/drawer-components'
import { formatRelativeAgeTime } from '../../utils/format'

const RESOURCE_PATH = /^(?:containers|initContainers)\[[^\]]+\]\.resources\.(limits|requests)\.(\S+) is ([^;]+); (?:template specifies ([^;]+)|the template leaves it unset)$/
const UNSET_PATH = /^(?:containers|initContainers)\[[^\]]+\]\.resources\.(limits|requests)\.(\S+) is unset; template specifies ([^;]+)$/
const IMAGE_PATH = /^(?:containers|initContainers)\[[^\]]+\]\.image is "([^"]+)"; template specifies "([^"]+)"$/
const MORE_DIFFERENCES = /^(\d+) more differences not listed$/

// Short form of one difference for the collapsed line, e.g.
// "memory limit 16Mi (template 256Mi)". Unrecognised shapes stay verbatim.
export function shortDifference(difference: string): string {
  let m = UNSET_PATH.exec(difference)
  if (m) return `${m[2]} ${m[1] === 'limits' ? 'limit' : 'request'} unset (template ${m[3]})`
  m = RESOURCE_PATH.exec(difference)
  if (m) return `${m[2]} ${m[1] === 'limits' ? 'limit' : 'request'} ${m[3]} (template ${m[4] ?? 'unset'})`
  m = IMAGE_PATH.exec(difference)
  if (m) return `image ${m[1]} (template ${m[2]})`
  return difference
}

// The collapsed line: the first difference, how many more, and how many
// objects could have caused it.
export function divergenceSummary(divergence: TemplateDivergence): string {
  const listed = divergence.differences.filter((d) => !MORE_DIFFERENCES.test(d))
  const unlisted = divergence.differences.reduce((n, d) => n + Number(MORE_DIFFERENCES.exec(d)?.[1] ?? 0), 0)
  const parts = [shortDifference(listed[0] ?? divergence.differences[0])]
  const more = listed.length - 1 + unlisted
  // Non-breaking spaces keep each part on one line when the summary wraps.
  if (more > 0) parts.push(`+${more}\u00a0more`)
  const sources = (divergence.sourceRefs?.length ?? 0) + (divergence.sourcesNotNamed ?? 0)
  if (sources > 0) parts.push(`${sources}\u00a0possible\u00a0${sources === 1 ? 'source' : 'sources'}`)
  return parts.join(' · ')
}

const STORAGE_PREFIX = 'radar.templateDivergence.expanded:'

function readExpanded(key: string | undefined): boolean {
  if (!key) return false
  try {
    return sessionStorage.getItem(STORAGE_PREFIX + key) === '1'
  } catch {
    return false
  }
}

function writeExpanded(key: string | undefined, expanded: boolean) {
  if (!key) return
  try {
    if (expanded) sessionStorage.setItem(STORAGE_PREFIX + key, '1')
    else sessionStorage.removeItem(STORAGE_PREFIX + key)
  } catch {
    // Storage blocked: the state still holds while the card is mounted.
  }
}

// Shows how a Pod (or a workload's Pods) differs from the template that
// created it, collapsed to one line until opened. The server only sends the
// field when something differs, so an older Radar renders nothing here.
// resourceKey identifies the open resource, so an opened card stays open for
// it within the browser session.
export function TemplateDivergenceCard({
  divergence,
  namespace,
  onNavigate,
  resourceKey,
}: {
  divergence?: TemplateDivergence
  namespace: string
  onNavigate?: (ref: ResourceRef) => void
  resourceKey?: string
}) {
  const [expanded, setExpanded] = useState(() => readExpanded(resourceKey))
  const bodyId = useId()
  if (!divergence || !divergence.differences?.length) return null
  const workload = Boolean(divergence.examplePod)
  const sources = divergence.sourceRefs ?? []
  const gaps = divergence.sourceGaps ?? []
  const toggle = () => {
    setExpanded((open) => {
      writeExpanded(resourceKey, !open)
      return !open
    })
  }

  return (
    <section className="rounded-lg border border-theme-border bg-theme-surface px-4 py-3 shadow-theme-sm" aria-label="Live spec differs from template">
      <button
        type="button"
        onClick={toggle}
        aria-expanded={expanded}
        aria-controls={bodyId}
        className="flex w-full min-w-0 items-center gap-2 rounded text-left focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent"
      >
        <GitCompare className="h-4 w-4 shrink-0 text-theme-text-secondary" aria-hidden />
        <span className="min-w-0 flex-1 text-xs">
          <span className="text-[11px] font-semibold uppercase tracking-wide text-theme-text-secondary">Live spec differs from template</span>
          <span className="text-theme-text-tertiary"> · {divergenceSummary(divergence)}</span>
        </span>
        <ChevronRight className={`h-4 w-4 shrink-0 text-theme-text-tertiary transition-transform ${expanded ? 'rotate-90' : ''}`} aria-hidden />
      </button>
      {expanded && (
      <div id={bodyId} className="mt-3 flex flex-col gap-3 text-xs">
        <p className="text-theme-text-secondary">
          {workload ? (
            <>
              {divergence.podsDiffering} of {divergence.podsCompared} {divergence.podsCapped ? 'most recent ' : ''}Pods don&apos;t match their template
              {divergence.podsNotCompared ? `, and ${divergence.podsNotCompared} could not be compared` : ''}.
            </>
          ) : (
            <>This Pod&apos;s running spec doesn&apos;t match its template{divergence.baseline ? ` in ${divergence.baseline}` : ''}.</>
          )}
        </p>
        {workload && divergence.examplePod && (
          <div className="flex flex-wrap items-center gap-1.5 text-theme-text-tertiary">
            <span>Differences for</span>
            <ResourceRefBadge resourceRef={{ kind: 'Pod', namespace, name: divergence.examplePod }} onClick={onNavigate} wrapAtSeparator />
            {divergence.baseline && <span>against {divergence.baseline}</span>}
          </div>
        )}
        <ul className="flex flex-col gap-1 rounded-md border border-theme-border/70 px-2.5 py-2">
          {divergence.differences.map((difference) => (
            <li key={difference} className="break-words font-mono text-[11px] leading-relaxed text-theme-text-secondary">{difference}</li>
          ))}
        </ul>
        {sources.length > 0 && (
          <div className="flex flex-col gap-1.5">
            <span className="text-[11px] font-semibold uppercase tracking-[0.06em] text-theme-text-tertiary">Could have changed it</span>
            <div className="flex flex-col items-start gap-1.5">
              {sources.map((ref) => (
                <span key={`${ref.group ?? ''}/${ref.kind}/${ref.namespace ?? ''}/${ref.name}`} className="inline-flex max-w-full items-center gap-1.5">
                  <ResourceRefBadge
                    resourceRef={{ kind: ref.kind, group: ref.group, namespace: ref.namespace ?? '', name: ref.name }}
                    onClick={onNavigate}
                    wrapAtSeparator
                  />
                  {ref.created && (
                    <span className="shrink-0 text-[11px] text-theme-text-tertiary" title={ref.created}>
                      created {formatRelativeAgeTime(ref.created)}
                    </span>
                  )}
                </span>
              ))}
              {divergence.sourcesNotNamed ? <span className="self-center text-theme-text-tertiary">+{divergence.sourcesNotNamed} more</span> : null}
            </div>
          </div>
        )}
        {gaps.map((gap) => (
          <p key={gap} className="text-theme-text-tertiary">{gap}</p>
        ))}
        <p className="text-theme-text-tertiary">
          An admission webhook, a LimitRange default, an in-place resize or a direct edit can cause this. Radar can&apos;t see which one did; a source listed here only matches this Pod.
        </p>
      </div>
      )}
    </section>
  )
}
