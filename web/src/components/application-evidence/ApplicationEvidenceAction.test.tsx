import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it } from 'vitest'
import type { ApplicationEvidenceResult } from '../../api/application-evidence'
import { ApplicationEvidenceObservation } from './ApplicationEvidenceAction'

const result: ApplicationEvidenceResult = {
  adapter: 'rabbitmq', target: { namespace: 'demo', pod: 'rabbit-0', uid: 'uid', container: 'rabbitmq' },
  observedAt: '2026-09-22T10:00:00Z', source: 'selected_pod_endpoint', outcome: 'observed',
  limitations: ['One Pod only; other replicas were not checked.'],
}

describe('application observations', () => {
  it('shows absent alarms as source observations, not a health verdict', () => {
    const html = renderToStaticMarkup(<ApplicationEvidenceObservation result={{ ...result, facts: { rabbitmq: { diskAlarm: false, memoryAlarm: true } } }} />)
    expect(html).toContain('Not reported')
    expect(html).toContain('Reported')
    expect(html).toContain('demo/rabbit-0')
    expect(html).toContain('other replicas were not checked')
    expect(html).not.toContain('Healthy')
  })
  it('does not display facts when collection is unavailable', () => {
    const html = renderToStaticMarkup(<ApplicationEvidenceObservation result={{ ...result, outcome: 'unavailable', reason: 'endpoint_denied', facts: { rabbitmq: { diskAlarm: false, memoryAlarm: false } } }} />)
    expect(html).toContain('Evidence unavailable')
    expect(html).toContain('requires authorization')
    expect(html).not.toContain('Disk alarm')
  })
  it('keeps NATS partial coverage and account identity visible', () => {
    const html = renderToStaticMarkup(<ApplicationEvidenceObservation result={{ ...result, adapter: 'nats', facts: { nats: { jetStreamEnabled: true, coverage: 'partial', returnedAccounts: 1, returnedStreams: 1, returnedConsumers: 1, truncated: true, consumers: [{ account: '$G', stream: 'events', name: 'worker', pending: 5, ackPending: 0, redelivered: 0 }] } } }} />)
    expect(html).toContain('partial')
    expect(html).toContain('Truncated')
    expect(html).toContain('$G / events / worker')
    expect(html).not.toContain('stuck')
  })
})

it.each(['pod_not_running', 'container_not_running', 'endpoint_port_not_declared', 'unsupported_image', 'ambiguous_target', 'host_network_unsupported', 'cluster_unavailable', 'invalid_target'])('explains %s without exposing a raw code', reason => {
  const html = renderToStaticMarkup(<ApplicationEvidenceObservation result={{ ...result, outcome: 'unavailable', reason }} />)
  expect(html).not.toContain(reason)
  expect(html).toContain('No conclusion about application health')
})
