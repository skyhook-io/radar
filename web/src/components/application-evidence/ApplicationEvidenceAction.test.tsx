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
  it('shows reported resource values without inferring alarms or percentages', () => {
    const html = renderToStaticMarkup(<ApplicationEvidenceObservation result={{ ...result, facts: { rabbitmq: {
      diskAlarm: false, memoryAlarm: true, diskAvailableBytes: 0, diskWatermarkBytes: 50 * 1024 * 1024,
      memoryUsedBytes: 128 * 1024 * 1024, memoryWatermarkBytes: 512 * 1024 * 1024,
    } } }} />)
    expect(html).toContain('Free disk</dt><dd class="break-words"><span class="block">0 B')
    expect(html).toContain('Disk alarm below</dt><dd class="break-words"><span class="block">50 MiB')
    expect(html).toContain('Memory used (RabbitMQ)</dt><dd class="break-words"><span class="block">128 MiB')
    expect(html).toContain('Memory alarm above</dt><dd class="break-words"><span class="block">512 MiB')
    expect(html).toContain('Disk alarm</dt><dd>Not reported')
    expect(html).toContain('Memory alarm</dt><dd>Reported')
    expect(html).toContain('can lag the alarm state')
    expect(html).not.toContain('%')
  })
  it('keeps absent measurements unknown while preserving alarms and handles large exact values', () => {
    const html = renderToStaticMarkup(<ApplicationEvidenceObservation result={{ ...result, facts: { rabbitmq: {
      diskAlarm: false, memoryAlarm: false, diskAvailableBytes: Number.MAX_SAFE_INTEGER,
    } } }} />)
    expect(html).toContain('GiB')
    expect(html.match(/Unknown<\/dd>/g)).toHaveLength(3)
    expect(html).toContain('Disk alarm</dt><dd>Not reported')
    expect(html).not.toContain('undefined')
    expect(html).not.toContain('NaN')
  })
  it.each([
    [49900000, 50000000, 2415000000, 2412409651],
    [1048500, 1048576, 1073700000, 1073741824],
  ])('keeps exact measurements visible near thresholds and unit boundaries (%d, %d, %d, %d)', (diskAvailableBytes, diskWatermarkBytes, memoryUsedBytes, memoryWatermarkBytes) => {
    const html = renderToStaticMarkup(<ApplicationEvidenceObservation result={{ ...result, facts: { rabbitmq: {
      diskAlarm: true, memoryAlarm: true, diskAvailableBytes, diskWatermarkBytes, memoryUsedBytes, memoryWatermarkBytes,
    } } }} />)
    for (const value of [diskAvailableBytes, diskWatermarkBytes, memoryUsedBytes, memoryWatermarkBytes]) {
      expect(html).toContain(`>${value.toLocaleString()} bytes</span>`)
    }
    expect(html).toContain('can lag the alarm state')
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
  it('preserves exact NATS positions and shows missing activity as unavailable', () => {
    const facts: ApplicationEvidenceResult['facts'] = { nats: { jetStreamEnabled: true, coverage: 'node_reported', returnedAccounts: 1, returnedStreams: 1, returnedConsumers: 1, truncated: false, consumers: [{ account: '$G', stream: 'events', name: 'worker', pending: 5, ackPending: 0, redelivered: 0, delivered: { consumerSequence: '18446744073709551615', streamSequence: '9007199254740993' }, ackFloor: { consumerSequence: '0', streamSequence: '0', lastActive: '2026-11-03T01:44:18.902568436Z' }, outstandingPullRequests: 0 }] } }
    const html = renderToStaticMarkup(<ApplicationEvidenceObservation result={{ ...result, adapter: 'nats', facts }} />)
    expect(html).toContain('18446744073709551615')
    expect(html).toContain('9007199254740993')
    expect(html).toContain('2026-11-03T01:44:18.902568436Z')
    expect(html).toContain('Last delivery</dt><dd class="break-words">Unavailable')
    expect(html).toContain('Waiting pull requests</dt><dd>0')
    expect(html).toContain('(pull consumers only)')
    expect(html).toContain('One snapshot cannot establish stalled progress')
    expect(html).not.toContain('never active')
    const unavailable = renderToStaticMarkup(<ApplicationEvidenceObservation result={{ ...result, adapter: 'nats', outcome: 'unavailable', reason: 'endpoint_denied', facts }} />)
    expect(unavailable).not.toContain('Consumer progress')
    expect(unavailable).not.toContain('18446744073709551615')
  })
  it.each([
    { initialized: false, sealed: true },
    { initialized: true, sealed: true },
  ])('does not present a Vault role while initialized=$initialized sealed=$sealed', state => {
    const html = renderToStaticMarkup(<ApplicationEvidenceObservation result={{ ...result, adapter: 'vault', facts: { vault: { ...state, standby: true, performanceStandby: false } } }} />)
    expect(html).toContain('Initialized')
    expect(html).toContain('Sealed')
    expect(html).not.toContain('Standby')
    expect(html).not.toContain('Performance standby')
  })
  it('shows reported Vault roles after initialization and unsealing', () => {
    const html = renderToStaticMarkup(<ApplicationEvidenceObservation result={{ ...result, adapter: 'vault', facts: { vault: { initialized: true, sealed: false, standby: true, performanceStandby: false } } }} />)
    expect(html).toContain('Standby')
    expect(html).toContain('Performance standby')
  })
})

it.each(['pod_not_running', 'container_not_running', 'endpoint_port_not_declared', 'unsupported_image', 'ambiguous_target', 'host_network_unsupported', 'cluster_unavailable', 'invalid_target'])('explains %s without exposing a raw code', reason => {
  const html = renderToStaticMarkup(<ApplicationEvidenceObservation result={{ ...result, outcome: 'unavailable', reason }} />)
  expect(html).not.toContain(reason)
  expect(html).toContain('No conclusion about application health')
})


describe('Vault HA observations', () => {
  it.each([
    { value: true, text: 'Reported healthy' },
    { value: false, text: 'Reported unhealthy' },
    { value: undefined, text: 'Unknown' },
  ])('keeps standby connection $value distinct from unknown', ({ value, text }) => {
    const html = renderToStaticMarkup(<ApplicationEvidenceObservation result={{ ...result, adapter: 'vault', facts: { vault: { initialized: true, sealed: false, standby: true, haConnectionHealthy: value } } }} />)
    expect(html).toContain('Connection to active node')
    expect(html).toContain(text)
    if (value === undefined) expect(html).toContain('Connectivity is unknown')
    else expect(html).not.toContain('Connectivity is unknown')
    expect(html).toContain('other replicas were not checked')
    expect(html).not.toContain('Removal from HA cluster')
  })
  it.each([
    { initialized: false, sealed: true, standby: true },
    { initialized: true, sealed: true, standby: true },
    { initialized: true, sealed: false, standby: false },
  ])('does not present inapplicable HA connection for $initialized/$sealed/$standby', state => {
    const html = renderToStaticMarkup(<ApplicationEvidenceObservation result={{ ...result, adapter: 'vault', facts: { vault: { ...state, haConnectionHealthy: false } } }} />)
    expect(html).not.toContain('Connection to active node')
    expect(html).not.toContain('Reported unhealthy')
  })
  it('keeps performance standby connection evidence applicable', () => {
    const html = renderToStaticMarkup(<ApplicationEvidenceObservation result={{ ...result, adapter: 'vault', facts: { vault: { initialized: true, sealed: false, standby: false, performanceStandby: true, haConnectionHealthy: false } } }} />)
    expect(html).toContain('Connection to active node')
    expect(html).toContain('Reported unhealthy')
  })
  it.each([{ initialized: true, removedFromCluster: true }, { initialized: true, removedFromCluster: false }, { initialized: false, removedFromCluster: false }])('shows removal $removedFromCluster with initialized=$initialized even while sealed', ({ initialized, removedFromCluster }) => {
    const html = renderToStaticMarkup(<ApplicationEvidenceObservation result={{ ...result, adapter: 'vault', facts: { vault: { initialized, sealed: true, standby: true, removedFromCluster } } }} />)
    expect(html).toContain(`<dt>Removal from HA cluster</dt><dd>${removedFromCluster ? 'Reported' : 'Not reported'}</dd>`)
    expect(html).not.toContain('Standby')
    expect(html).not.toContain('Connection to active node')
  })
})
