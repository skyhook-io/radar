import { CardBody, CardSection, renderProse } from '@skyhook-io/k8s-ui'
import { AlertTriangle, Info, Wrench } from 'lucide-react'

const wrap = { width: 480, display: 'flex', flexDirection: 'column', gap: 16 } as const

export function Paragraph() {
  return (
    <div style={{ width: 480 }}>
      <CardBody>
        {renderProse(
          'Containers without a memory limit can consume all memory on the node, triggering OOM kills of unrelated pods. Set `resources.limits.memory` on every container in `checkout-api`.',
        )}
      </CardBody>
    </div>
  )
}

export function InCardSections() {
  return (
    <div style={wrap}>
      <CardSection icon={AlertTriangle} label="What's wrong" tone="warn">
        <CardBody>
          {renderProse('Pod `payments/checkout-api-7d9f8c-x2kqp` restarted 14 times in the last hour with exit code 137.')}
        </CardBody>
      </CardSection>
      <CardSection icon={Info} label="Why it matters" tone="neutral">
        <CardBody>Each restart drops in-flight checkout requests and resets the connection pool to Postgres.</CardBody>
      </CardSection>
      <CardSection icon={Wrench} label="How to fix" tone="fix">
        <CardBody>
          {renderProse('Raise `resources.limits.memory` from `256Mi` to at least `512Mi`, or profile the heap growth after deploy.')}
        </CardBody>
      </CardSection>
    </div>
  )
}
