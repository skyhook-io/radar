import { useState } from 'react'
import { clsx } from 'clsx'
import { ListChecks, Workflow, Boxes, FileWarning } from 'lucide-react'
import { Section, PropertyList, Property, AlertBanner, ResourceLink } from '../../ui/drawer-components'
import { HEALTH_BADGE_COLORS, SEVERITY_BADGE } from '../../../utils/badge-colors'
import { TimeValue } from '../../ui/ScheduleValue'
import { formatRelativeAgeTime } from '../../../utils/format'
import {
  getKyvernoRequestType,
  getKyvernoRequestTriggers,
  getKyvernoRequestGenerated,
  getKyvernoRequestState,
  getKyvernoRequestPolicy,
  getKyvernoRequestMessage,
  getKyvernoReportSubject,
  getKyvernoReportSource,
  getKyvernoReportSummary,
  getKyvernoReportResults,
  kyvernoReportTimestamp,
} from '../resource-utils-kyverno-queue'

type Nav = (ref: { kind: string; namespace: string; name: string; group?: string }) => void

/** Long trigger lists fold at the same point as the house relationship lists. */
const FOLD = 10

function RefList({
  refs,
  onNavigate,
  empty,
}: {
  refs: Array<{ apiVersion?: string; kind?: string; name?: string; namespace?: string }>
  onNavigate?: Nav
  empty: string
}) {
  const [expanded, setExpanded] = useState(false)
  if (refs.length === 0) return <div className="text-sm text-theme-text-tertiary">{empty}</div>

  const shown = expanded ? refs : refs.slice(0, FOLD)
  return (
    <div className="space-y-1.5">
      <div className="flex flex-wrap gap-1.5">
        {shown.map((r, i) => (
          <ResourceLink
            key={`${r.kind}/${r.namespace}/${r.name}/${i}`}
            name={r.name ?? ''}
            kind={r.kind ?? ''}
            namespace={r.namespace ?? ''}
            group={r.apiVersion?.includes('/') ? r.apiVersion.split('/')[0] : undefined}
            onNavigate={onNavigate}
          />
        ))}
      </div>
      {refs.length > FOLD && (
        <button
          className="text-xs text-theme-text-secondary hover:text-theme-text-primary"
          onClick={() => setExpanded(!expanded)}
        >
          {expanded ? 'Show fewer' : `Show all ${refs.length}`}
        </button>
      )}
    </div>
  )
}

/**
 * UpdateRequest — the record of work Kyverno queued rather than did inline.
 *
 * It is where a generation or a mutate-existing that silently stopped is
 * diagnosed: the policy looks Ready, the target never appears, and the only
 * evidence is a request sitting in Pending.
 *
 * The two request types are shaped so differently that one renderer reading a
 * single field set would be blank for half of them — see the accessors.
 */
export function KyvernoUpdateRequestRenderer({ data, onNavigate }: { data: any; onNavigate?: Nav }) {
  const type = getKyvernoRequestType(data)
  const state = getKyvernoRequestState(data)
  const triggers = getKyvernoRequestTriggers(data)
  const generated = getKyvernoRequestGenerated(data)
  const policy = getKyvernoRequestPolicy(data)
  const message = getKyvernoRequestMessage(data)
  // Every trigger in one request comes from the same rule; a generate request
  // repeats it per entry, a mutate request states it once.
  const rule = data?.spec?.rule || triggers.find((t) => t.rule)?.rule
  const deletesDownstream = data?.spec?.deleteDownstream === true || triggers.some((t) => t.deleteDownstream)

  return (
    <>
      {state.level === 'unhealthy' && (
        <AlertBanner
          variant="error"
          title="This request failed"
          message={message || 'Kyverno could not complete this request. The resources below were not updated.'}
        />
      )}
      {state.level === 'degraded' && (
        <AlertBanner
          variant="warning"
          title="Still queued"
          // Pending is normal for seconds and a symptom for longer, and this
          // object carries no timestamp of its own beyond creation.
          message="Kyverno has not processed this request yet. If it stays here, the background controller is not running or cannot reach what it needs."
        />
      )}

      <Section title="Request" icon={Workflow} defaultExpanded>
        <PropertyList>
          <Property
            label="State"
            value={
              <span className={clsx('badge', HEALTH_BADGE_COLORS[state.level as keyof typeof HEALTH_BADGE_COLORS])}>
                {state.text}
              </span>
            }
          />
          <Property label="Type" value={type === 'unknown' ? '-' : type} />
          {policy && (
            <Property
              label="Policy"
              value={
                // `spec.policy` is a bare name with no kind and no namespace, and
                // a namespaced Policy may share it with a ClusterPolicy. A
                // qualified name is the one case that identifies itself; anything
                // else links to a page that may not be the policy that queued
                // this, so it stays plain text rather than guessing.
                policy.includes('/') ? (
                  <ResourceLink
                    name={policy.split('/')[1]}
                    // `policies` is what the API serves and what a URL can be
                    // built from; `kyvernopolicies` is an internal display alias
                    // and 400s when asked for. The group is what disambiguates.
                    kind="policies"
                    namespace={policy.split('/')[0]}
                    group="kyverno.io"
                    label={policy}
                    onNavigate={onNavigate}
                  />
                ) : (
                  policy
                )
              }
            />
          )}
          {rule && <Property label="Rule" value={rule} />}
          <Property
            label="On trigger delete"
            value={
              deletesDownstream ? (
                <span className="text-warning-text">
                  Removes what this rule created for it
                </span>
              ) : (
                'Leaves what this rule created in place'
              )
            }
          />
          {data?.metadata?.creationTimestamp && (
            <Property label="Queued" value={<TimeValue timestamp={data.metadata.creationTimestamp} />} />
          )}
        </PropertyList>
        {message && state.level !== 'unhealthy' && (
          <div className="mt-2 pt-2 border-t border-theme-border text-xs text-theme-text-secondary">{message}</div>
        )}
      </Section>

      <Section title={`Triggered By (${triggers.length})`} icon={ListChecks} defaultExpanded>
        <RefList
          refs={triggers}
          onNavigate={onNavigate}
          // A queued generate request has not been given its triggers yet, so
          // the empty list means "not written down", not "acts on nothing".
          empty={
            state.level === 'degraded'
              ? 'Kyverno has not recorded what this request is for yet.'
              : 'This request names no trigger, so there is nothing for it to act on.'
          }
        />
      </Section>

      {type === 'generate' && (
        <Section title={`Created (${generated.length})`} icon={Boxes} defaultExpanded>
          <RefList
            refs={generated}
            onNavigate={onNavigate}
            empty={
              state.level === 'healthy'
                ? 'Kyverno records nothing created for this request.'
                : 'Nothing has been created yet.'
            }
          />
        </Section>
      )}
    </>
  )
}

// Same mapping the policy coverage view uses, so a result reads identically
// wherever it appears.
const RESULT_TONE: Record<string, string> = {
  pass: SEVERITY_BADGE.success,
  fail: SEVERITY_BADGE.error,
  warn: SEVERITY_BADGE.warning,
  error: SEVERITY_BADGE.alert,
  skip: SEVERITY_BADGE.neutral,
}

/**
 * EphemeralReport — a background scan's findings for one resource, before they
 * are folded into a PolicyReport.
 *
 * Worth a surface because it is what exists during a scan, and because a
 * cluster mid-migration can show findings here that have not reached any
 * PolicyReport yet. It is also genuinely short-lived: an empty list means the
 * scan has finished, not that nothing was checked.
 */
export function KyvernoEphemeralReportRenderer({ data, onNavigate }: { data: any; onNavigate?: Nav }) {
  const subject = getKyvernoReportSubject(data)
  const summary = getKyvernoReportSummary(data)
  const results = getKyvernoReportResults(data)
  const source = getKyvernoReportSource(data)

  return (
    <>
      <Section title="Scan" icon={FileWarning} defaultExpanded>
        <PropertyList>
          <Property
            label="About"
            value={
              subject?.name ? (
                <ResourceLink
                  name={subject.name}
                  kind={subject.kind ?? ''}
                  namespace={subject.namespace ?? ''}
                  group={subject.apiVersion?.includes('/') ? subject.apiVersion.split('/')[0] : undefined}
                  onNavigate={onNavigate}
                />
              ) : subject?.kind ? (
                // The subject is gone; the labels still say what it was.
                `${subject.kind} (deleted)`
              ) : (
                '-'
              )
            }
          />
          {source && <Property label="Produced by" value={source.replace(/-/g, ' ')} />}
          <Property
            label="Outcome"
            value={
              summary.total === 0
                ? 'Nothing was checked'
                : [
                    summary.fail ? `${summary.fail} fail` : '',
                    summary.error ? `${summary.error} error` : '',
                    summary.warn ? `${summary.warn} warn` : '',
                    summary.pass ? `${summary.pass} pass` : '',
                    summary.skip ? `${summary.skip} skipped` : '',
                  ]
                    .filter(Boolean)
                    .join(' · ')
            }
          />
        </PropertyList>
      </Section>

      <Section title={`Results (${results.length})`} icon={ListChecks} defaultExpanded>
        {results.length === 0 ? (
          <div className="text-sm text-theme-text-tertiary">
            This report carries no results. Kyverno deletes these once the scan is folded into a
            PolicyReport, so an empty one is finished work rather than a problem.
          </div>
        ) : (
          <div className="space-y-1">
            {results.map((r: any, i: number) => {
              const at = kyvernoReportTimestamp(r?.timestamp)
              return (
                <div
                  key={`${r?.policy}/${r?.rule}/${i}`}
                  className="flex items-baseline gap-2 text-xs py-1 border-b border-theme-border/30 last:border-b-0"
                >
                  <span
                    className={clsx('badge badge-sm shrink-0', RESULT_TONE[r?.result] || SEVERITY_BADGE.neutral)}
                  >
                    {r?.result || '-'}
                  </span>
                  <span className="text-theme-text-secondary truncate">{r?.policy || '-'}</span>
                  {r?.rule && <span className="text-theme-text-tertiary truncate">/ {r.rule}</span>}
                  {r?.message && (
                    <span className="text-theme-text-tertiary truncate" title={r.message}>
                      {r.message}
                    </span>
                  )}
                  {at && (
                    <span className="ml-auto shrink-0 text-theme-text-tertiary">{formatRelativeAgeTime(at)}</span>
                  )}
                </div>
              )
            })}
          </div>
        )}
      </Section>
    </>
  )
}
