import { AllClearCard } from '@skyhook-io/k8s-ui'

const wrap = { width: 540, padding: 8 }
const noop = () => {}

export function NoProblemFound() {
  return (
    <div style={wrap}>
      <AllClearCard
        animate={false}
        showDisclaimer
        coverageLimited={false}
        evidenceConflict={false}
        diagnosis={{
          healthy: true,
          rootCause: '',
          report: 'All 3 replicas of `payments/checkout-api` are Ready, no restarts in the last 6h, and recent logs show only request traffic.',
          remediation: [],
        }}
      />
    </div>
  )
}

export function CoverageLimited() {
  return (
    <div style={wrap}>
      <AllClearCard
        animate={false}
        showDisclaimer
        coverageLimited
        evidenceConflict={false}
        diagnosis={{
          healthy: true,
          rootCause: '',
          report: 'Pods are Running and Ready; events show no warnings. Logs could not be read (403 on `pods/log`).',
          remediation: [],
        }}
      />
    </div>
  )
}

export function EvidenceConflict() {
  return (
    <div style={wrap}>
      <AllClearCard
        animate={false}
        showDisclaimer
        coverageLimited={false}
        evidenceConflict
        diagnosis={{
          healthy: true,
          rootCause: '',
          report: 'The Deployment reports 3/3 available replicas and the rollout completed.',
          remediation: [],
        }}
      />
    </div>
  )
}

export function StoryWithSignals() {
  return (
    <div style={wrap}>
      <AllClearCard
        animate={false}
        showDisclaimer
        coverageLimited={false}
        evidenceConflict={false}
        onRevealSource={noop}
        diagnosis={{
          healthy: true,
          summary: 'checkout-api is healthy — the restarts you saw were a one-off node drain this morning.',
          certainty: 'established',
          rootCause: '',
          report: 'All replicas are Ready. [[radar:evidence=1]]',
          remediation: [],
        }}
        healthSignals={[
          { title: 'Pod checkout-api-7d9f8c6b5-x2kqp restarts', status: 'explained', sourceId: 's1', claim: 'the 2 restarts at 06:12 line up with node ip-10-0-3-41 draining; none since.' },
          { title: 'Warning event FailedScheduling', status: 'unaddressed', sourceId: 's2' },
        ]}
      />
    </div>
  )
}
