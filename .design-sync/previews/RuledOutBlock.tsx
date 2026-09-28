import { RuledOutBlock } from '@skyhook-io/k8s-ui'

const wrap = { width: 560, padding: 8 }
const noop = () => {}

// Each entry is a hypothesis the agent excluded plus the case item (a card-
// or revision-placed observation) whose result contradicted it.
const item = (index: number, title: string, placement: 'card' | 'revision' = 'card') => ({
  index, role: 'rules_out', claim: '', placement, groupId: `evidence-${index}`,
  source: { id: `turn-0-step-${index}`, tool: 'get_resource' },
  observation: { title },
}) as any

export function OneHypothesis() {
  return (
    <div style={wrap}>
      <RuledOutBlock onReveal={noop} entries={[{ hypothesis: 'Node memory pressure is evicting the pods', item: item(3, 'Node ip-10-0-3-41.ec2.internal') }]} />
    </div>
  )
}

export function SeveralHypotheses() {
  return (
    <div style={wrap}>
      <RuledOutBlock
        onReveal={noop}
        entries={[
          { hypothesis: 'Node memory pressure is evicting the pods', item: item(3, 'Node ip-10-0-3-41.ec2.internal') },
          { hypothesis: 'A bad payments-db credential makes the app exit', item: item(4, 'Secret payments/payments-db') },
          { hypothesis: 'The readiness probe kills healthy containers', item: item(5, 'Deployment payments/checkout-api', 'revision') },
        ]}
      />
    </div>
  )
}
