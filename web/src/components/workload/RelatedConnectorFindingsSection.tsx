import { gapReasonText, ResourceIssuesSection, type IssueResourceRef, type RelatedApplicationFindings } from '@skyhook-io/k8s-ui'

export function RelatedConnectorFindingsSection({ data, onResourceClick }: {
  data: RelatedApplicationFindings | undefined
  onResourceClick?: (ref: IssueResourceRef) => void
}) {
  if (!data || (!data.findings?.length && !data.truncated && data.unknown === 0 && data.gaps.length === 0)) return null

  return (
    <div className="space-y-2">
      {data.findings?.length ? (
        <ResourceIssuesSection heading="Related connector findings" issues={data.findings} onResourceClick={onResourceClick} />
      ) : (
        <h3 className="text-xs font-semibold uppercase tracking-wide text-theme-text-secondary">Related connector findings</h3>
      )}
      <p className="text-xs text-theme-text-secondary">
        Associated with {data.source.kind} {data.source.name}; these are connector-level findings. This resource's status is reported separately.
        {' '}{data.coverage}
        {data.truncated && ' Additional connector evidence may be omitted.'}
      </p>
      {data.gaps.length > 0 && !data.truncated && (
        <ul className="text-xs text-theme-text-secondary">
          {data.gaps.map(gap => <li key={gap.code}>Connector inventory: {gapReasonText(gap.code)}.</li>)}
        </ul>
      )}
    </div>
  )
}
