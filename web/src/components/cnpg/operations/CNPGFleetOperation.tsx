import { StatusDot, Tooltip, formatAge } from '@skyhook-io/k8s-ui'
import { CNPG_OP_STATE_TEXT, CNPG_OP_STATE_TONE } from './presentation'
import type { CNPGTrackedOperation } from './model'

export function CNPGFleetOperation({ op, onFollow }: { op: CNPGTrackedOperation; onFollow: () => void }) {
  return (
    <div className="mt-1 text-[11px]">
      <Tooltip
        content={`${op.label}: ${CNPG_OP_STATE_TEXT[op.state]}. ${op.detail || ''} ${op.clusterUID ? '' : 'Cluster UID was not recorded; verify identity.'} The fleet does not poll this operation. Open the Cluster to follow it.`}
      >
        <button
          type="button"
          onClick={(event) => {
            event.stopPropagation()
            onFollow()
          }}
          className="inline-flex max-w-full items-center gap-1 rounded text-accent-text hover:underline focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent"
        >
          <StatusDot tone={CNPG_OP_STATE_TONE[op.state]} size="xs" />
          <span className="truncate">{op.label}</span>
          <span className="shrink-0">· {CNPG_OP_STATE_TEXT[op.state]}</span>
        </button>
      </Tooltip>
      <div className="text-theme-text-tertiary">
        This tab ·{' '}
        {op.lastCheckedAt ? `checked ${formatAge(new Date(op.lastCheckedAt).toISOString())} ago` : 'not yet checked'}
      </div>
    </div>
  )
}
