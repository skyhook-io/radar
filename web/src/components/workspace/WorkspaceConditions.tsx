import { Badge, FactGrid, FactRow, FoldSection, Tooltip, formatAge, WorkspaceCard } from '@skyhook-io/k8s-ui'

export function WorkspaceConditions({
  conditions,
  title = 'Operator conditions',
  className,
}: {
  title?: string
  className?: string
  conditions: { type: string; status: string; reason?: string; message?: string; lastTransitionTime?: string }[]
}) {
  return (
    <WorkspaceCard className={className}>
      <FoldSection
        title={title}
        summary={conditions.length > 0 ? `${conditions.length} reported · status.conditions` : 'No conditions reported'}
        attention={false}
      >
        <FactGrid>
          {conditions.map((c) => (
            <FactRow
              key={c.type}
              label={
                <span className="inline-flex flex-wrap items-center gap-2">
                  {c.type}
                  <Badge severity="neutral" size="sm">
                    {c.status}
                  </Badge>
                </span>
              }
            >
              <div className="text-xs text-theme-text-secondary">Reason: {c.reason || 'Not reported'}</div>
              <div className="break-words text-xs text-theme-text-secondary">{c.message || 'No message reported'}</div>
              <div className="text-xs text-theme-text-tertiary">
                Last transition:{' '}
                {c.lastTransitionTime ? (
                  <Tooltip content={c.lastTransitionTime}>
                    <time dateTime={c.lastTransitionTime}>{formatAge(c.lastTransitionTime)} ago</time>
                  </Tooltip>
                ) : (
                  'Not reported'
                )}
              </div>
            </FactRow>
          ))}
        </FactGrid>
        {conditions.length === 0 && <p className="text-sm text-theme-text-tertiary">No status.conditions reported.</p>}
      </FoldSection>
    </WorkspaceCard>
  )
}
