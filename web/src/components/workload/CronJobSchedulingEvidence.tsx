import { Clock, Info } from 'lucide-react'
import { AlertBanner, CardSection, Disclosure, TimeValue } from '@skyhook-io/k8s-ui'
import type { CronJobSchedulingEvidence } from '../../api/client'

type EvidenceProps = { evidence: CronJobSchedulingEvidence; onViewTimeline: () => void }

export function CronJobSchedulingDetails({ evidence, onViewTimeline }: EvidenceProps) {
  return (
    <CardSection icon={Clock} label="Scheduling evidence">
      <p className="text-sm text-theme-text-secondary">Last reported: <TimeValue timestamp={evidence.last_reported_at} />.</p>
      {evidence.configuration_changed_at && <p className="text-sm text-theme-text-secondary">Scheduling settings changed: <TimeValue timestamp={evidence.configuration_changed_at} />.</p>}
      {evidence.history_incomplete && <p className="text-sm text-theme-text-secondary">Some history is unavailable. The report's current relevance cannot be fully verified.</p>}
      <button type="button" onClick={onViewTimeline} className="self-start text-left text-sm font-medium text-accent-text hover:underline">View timeline</button>
    </CardSection>
  )
}

export function CronJobSchedulingContext({ evidence, onViewTimeline }: { evidence?: CronJobSchedulingEvidence; onViewTimeline: () => void }) {
  if (!evidence || evidence.state === 'reported') return null
  const changed = evidence.state === 'configuration_changed'
  return (
    <AlertBanner
      variant="info"
      icon={Info}
      title={changed ? 'Schedule changed after this error' : 'Scheduling history is incomplete'}
      message={changed ? 'Kubernetes has not recorded a scheduled run since the change.' : 'A scheduling error was reported, but retained history cannot establish whether it still applies.'}
      className="mb-0"
    >
      <Disclosure summary="Earlier report" className="mt-2" summaryClassName="text-xs text-theme-text-secondary">
        <div className="mt-3 space-y-3">
          <pre className="whitespace-pre-wrap break-words rounded bg-theme-elevated p-3 text-xs text-theme-text-secondary">{evidence.message}</pre>
          <CronJobSchedulingDetails evidence={evidence} onViewTimeline={onViewTimeline} />
        </div>
      </Disclosure>
    </AlertBanner>
  )
}
