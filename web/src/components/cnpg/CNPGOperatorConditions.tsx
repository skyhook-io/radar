import { Badge, FactGrid, FactRow, FoldSection, Tooltip, formatAge } from '@skyhook-io/k8s-ui'

export function CNPGOperatorConditions({ conditions }: { conditions: { type: string; status: string; reason?: string; message?: string; lastTransitionTime?: string }[] }) {
  return (
    <section className="mx-4 mb-4 rounded-xl border border-theme-border bg-theme-surface px-4 py-3 shadow-theme-sm">
      <FoldSection title="Operator conditions" summary={conditions.length > 0 ? `${conditions.length} reported · status.conditions` : 'No conditions reported'} attention={false}>
        <FactGrid>{conditions.map((c) => <FactRow key={c.type} label={<span className="inline-flex flex-wrap items-center gap-2">{c.type}<Badge severity="neutral" size="sm">{c.status}</Badge></span>}>
          <div className="text-xs text-theme-text-secondary">Reason: {c.reason || 'Not reported'}</div>
          <div className="break-words text-xs text-theme-text-secondary">{c.message || 'No message reported'}</div>
          <div className="text-xs text-theme-text-tertiary">Last transition: {c.lastTransitionTime ? <Tooltip content={c.lastTransitionTime}><time dateTime={c.lastTransitionTime}>{formatAge(c.lastTransitionTime)} ago</time></Tooltip> : 'Not reported'}</div>
        </FactRow>)}</FactGrid>
        {conditions.length === 0 && <p className="text-sm text-theme-text-tertiary">The operator has not reported status.conditions.</p>}
      </FoldSection>
    </section>
  )
}
