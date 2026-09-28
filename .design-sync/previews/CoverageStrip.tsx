import { useState } from 'react'
import { CoverageStrip } from '@skyhook-io/k8s-ui'

const wrap = { width: 620, padding: 8 }
const noop = () => {}

// Limitations from an investigation of payments/checkout-api stuck on
// ImagePullBackOff: the diagnose bundle sampled logs/events/changes, and an
// events read in another namespace was denied.
const diagnoseCall = { id: 'turn-0-step-diag-2', turnIndex: 0, timelineIndex: 0, stepId: 'diag-2', tool: 'diagnose', order: 0, phase: 'initial', confirmedSuccess: true, primaryGroupId: 'evidence-startup-imagepull' }
const eventsCall = { id: 'turn-0-step-ev-2', turnIndex: 0, timelineIndex: 1, stepId: 'ev-2', tool: 'get_events', order: 1, phase: 'initial', confirmedSuccess: true }
const limit = (source: string, message: string, kind: 'truncated' | 'error' | 'unknown', from: any, extra = {}) =>
  ({ source, message, kind, firstOrder: from.order, sources: [from], ...extra }) as any
const group = (label: string, summary: string, limitations: any[], extra = {}) =>
  ({ label, summary, limitations, hasError: limitations.some((l) => l.kind === 'error'), historyOnly: false, bookkeepingOnly: false, ...extra })

const denied = limit('Events', 'Events in Pod resources in payments-ci are not readable with your permissions.', 'error', eventsCall)
const sampled = [
  group('Events', denied.message, [denied, limit('Events', 'Radar received 2 of 5 event groups.', 'truncated', diagnoseCall)]),
  group('Log pod coverage', 'Log collection selected 1 of 4 pods.', [limit('Log pod coverage', 'Log collection selected 1 of 4 pods.', 'truncated', diagnoseCall)], { bookkeepingOnly: true }),
  group('Log excerpt coverage', "The response includes 1 of 200 selected log lines after the size limit; these are excerpts, not the container's full log history.", [limit('Log excerpt coverage', "The response includes 1 of 200 selected log lines after the size limit; these are excerpts, not the container's full log history.", 'truncated', diagnoseCall)], { bookkeepingOnly: true }),
  group('Recent changes', 'Some changes may be missing; change history is incomplete.', [limit('Recent changes', 'The recent-change result limit was reached; additional changes may exist in the requested window.', 'truncated', diagnoseCall)], { bookkeepingOnly: true }),
]
const summaryOf = (groups: any[]) => groups.map((g) => `${g.label}: ${g.summary}`).join(' · ')

function Strip({ groups, initiallyOpen = false }: { groups: any[]; initiallyOpen?: boolean }) {
  const [open, setOpen] = useState(initiallyOpen)
  return (
    <div style={wrap}>
      <CoverageStrip groups={groups} visibleGroupIds={new Set()} summary={summaryOf(groups)} onViewSource={noop} open={open} onOpenChange={setOpen} />
    </div>
  )
}

export function PermissionDenied() {
  return <Strip groups={sampled} />
}

export function PermissionDeniedOpen() {
  return <Strip groups={sampled} initiallyOpen />
}

export function SamplingOnly() {
  return <Strip groups={sampled.slice(1)} />
}

export function HistoryLimited() {
  const history = limit('Recent changes', 'Radar keeps change history for this session only; changes before 08:14 are not recorded.', 'unknown', diagnoseCall, { presentation: 'history' })
  return <Strip groups={[group('Recent changes', 'Change history is incomplete.', [history], { historyOnly: true, bookkeepingOnly: true })]} />
}
