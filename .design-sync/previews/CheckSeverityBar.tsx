import { CheckSeverityBar } from '@skyhook-io/k8s-ui'

const box = { width: 340, padding: '16px 10px' }

export function MixedFindings() {
  return (
    <div style={box}>
      <CheckSeverityBar totals={{ critical: 2, high: 6, medium: 11, low: 9 }} />
    </div>
  )
}

export function MostlyLow() {
  return (
    <div style={box}>
      <CheckSeverityBar totals={{ critical: 0, high: 1, medium: 4, low: 23 }} />
    </div>
  )
}
