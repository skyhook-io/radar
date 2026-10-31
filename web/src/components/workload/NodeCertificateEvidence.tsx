import { Clock, Info } from 'lucide-react'
import { AlertBanner, CardSection, Disclosure, TimeValue } from '@skyhook-io/k8s-ui'
import type { NodeCertificateReport } from '../../api/client'

type EvidenceProps = { evidence: NodeCertificateReport; onViewTimeline: () => void; includeOriginalReport?: boolean }

export function NodeCertificateDetails({ evidence, onViewTimeline, includeOriginalReport = true }: EvidenceProps) {
  return (
    <CardSection icon={Clock} label="Certificate evidence">
      <p className="text-sm text-theme-text-secondary">Last reported: <TimeValue timestamp={evidence.last_reported_at} />.</p>
      {evidence.expires_at && <p className="text-sm text-theme-text-secondary">Reported expiry: <TimeValue timestamp={evidence.expires_at} />.</p>}
      {evidence.not_before && <p className="text-sm text-theme-text-secondary">Reported valid from: <TimeValue timestamp={evidence.not_before} />.</p>}
      {evidence.state === 'unverified' && <p className="text-sm text-theme-text-secondary">The current certificate state is unverified; this is the last retained report.</p>}
      {evidence.history_incomplete && <p className="text-sm text-theme-text-secondary">Some history is unavailable; a later report may be missing.</p>}
      {evidence.validity_unknown && <p className="text-sm text-theme-text-secondary">Radar could not read the validity dates from this report.</p>}
      {includeOriginalReport && <Disclosure summary="Original report" summaryClassName="text-xs text-theme-text-secondary">
        <pre className="mt-2 max-h-64 overflow-auto whitespace-pre-wrap break-words rounded bg-theme-elevated p-3 text-xs text-theme-text-secondary">{evidence.message}</pre>
      </Disclosure>}
      <button type="button" onClick={onViewTimeline} className="self-start text-left text-sm font-medium text-accent-text hover:underline">View timeline</button>
    </CardSection>
  )
}

export function nodeCertificateContextVisible(evidence: NodeCertificateReport | undefined, hasIssue: boolean): boolean {
  return Boolean(evidence && evidence.reason !== 'CertificateExpirationOK' && evidence.state !== 'ok' && !hasIssue)
}

export function NodeCertificateContext({ evidence, hasIssue, onViewTimeline }: { evidence?: NodeCertificateReport; hasIssue: boolean; onViewTimeline: () => void }) {
  if (!evidence || !nodeCertificateContextVisible(evidence, hasIssue)) return null
  const unverified = evidence.state === 'unverified'
  return (
    <AlertBanner
      variant="info"
      icon={Info}
      title={unverified ? 'Node certificate status unverified' : 'Node certificate warning reported'}
      message={unverified ? 'The earlier warning is retained, but the current certificate state is unverified.' : evidence.validity_unknown ? "Review the original monitor report and the distribution's certificate guidance." : "Review the reported validity dates and the distribution's certificate guidance."}
      className="mb-0"
    >
      <div className="mt-3">
        <NodeCertificateDetails evidence={evidence} onViewTimeline={onViewTimeline} />
      </div>
    </AlertBanner>
  )
}
