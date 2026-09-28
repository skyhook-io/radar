import { ApplyOutcomeCard } from '@skyhook-io/k8s-ui'

const wrap = { width: 480, padding: 8 }
const noop = () => {}

export function Confirmed() {
  return (
    <div style={wrap}>
      <ApplyOutcomeCard
        animate={false}
        applyOutcome="confirmed"
        onCheckStatus={noop}
        diagnosis={{
          rootCause: 'Memory limit raised',
          report: 'Patched `deployment/checkout-api` in `payments`: memory limit on container `checkout-api` is now **512Mi**. A new ReplicaSet `checkout-api-6b7f9d8c4` is rolling out.',
          remediation: [],
        }}
      />
    </div>
  )
}

export function Failed() {
  return (
    <div style={wrap}>
      <ApplyOutcomeCard
        animate={false}
        applyOutcome="failed"
        error={'deployments.apps "checkout-api" is forbidden: User "dev@acme.io" cannot patch resource "deployments" in API group "apps" in the namespace "payments"'}
        diagnosis={{ rootCause: '', report: '', remediation: [] }}
      />
    </div>
  )
}

export function Unknown() {
  return (
    <div style={wrap}>
      <ApplyOutcomeCard
        animate={false}
        applyOutcome="unknown"
        onCheckStatus={noop}
        diagnosis={{ rootCause: '', report: '', remediation: [] }}
      />
    </div>
  )
}
