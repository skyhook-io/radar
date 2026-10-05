import { FactGrid, FactRow, FoldSection } from '@skyhook-io/k8s-ui'

export function CNPGOperatorConditions({ conditions }: { conditions: { type: string; status: string; reason?: string; message?: string; lastTransitionTime?: string }[] }) {
  return (
    <section className="mx-4 mb-4 rounded-xl border border-theme-border bg-theme-surface px-4 py-3 shadow-theme-sm">
      <FoldSection title="Operator conditions" summary={conditions.length > 0 ? `${conditions.length} reported · status.conditions` : 'No conditions reported'} attention={false}>
        <FactGrid>{conditions.map((c) => <FactRow key={c.type} label={c.type}>
          <div>{c.status}</div>
          <div className="text-xs text-theme-text-secondary">Reason: {c.reason || 'Not reported'}</div>
          <div className="break-words text-xs text-theme-text-secondary">{c.message || 'No message reported'}</div>
          <div className="text-xs text-theme-text-tertiary">Last transition: {c.lastTransitionTime ? <time dateTime={c.lastTransitionTime}>{c.lastTransitionTime}</time> : 'Not reported'}</div>
        </FactRow>)}</FactGrid>
        {conditions.length === 0 && <p className="text-sm text-theme-text-tertiary">The operator has not reported status.conditions.</p>}
      </FoldSection>
    </section>
  )
}
