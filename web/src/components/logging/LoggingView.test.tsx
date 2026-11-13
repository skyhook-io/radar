import { describe, expect, it, vi } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'
import type { LogPipeline } from '@skyhook-io/k8s-ui/types/core'
import { RadarFeatureUnsupportedError } from '../../api/radarFeatures'

const state = vi.hoisted(() => ({ data: undefined as unknown, error: null as unknown, isLoading: false }))
vi.mock('../../api/loggingPipeline', () => ({ useLoggingPipeline: () => ({ ...state, support: 'supported' }) }))

import { LoggingView } from './LoggingView'

const pipeline = (patch: Partial<LogPipeline> = {}): LogPipeline => ({
  providers: [{
    provider: 'logging-operator',
    instances: [{ name: 'default', controlNamespace: 'logging', collectors: [{ kind: 'DaemonSet', namespace: 'logging', name: 'default-fluentbit', ready: 2, desired: 3 }], aggregators: [{ kind: 'StatefulSet', namespace: 'logging', name: 'default-fluentd', ready: 1, desired: 1 }] }],
    routes: [{ provider: 'logging-operator', kind: 'ClusterFlow', group: 'logging.banzaicloud.io', namespace: 'logging', name: 'optin', instance: 'default', ignored: false, hasFilters: false, active: true,
      destinations: [{ kind: 'ClusterOutput', namespace: 'logging', name: 'loki', type: 'loki', found: true, active: true }], selectedPods: { count: 12, lowerBound: false } }],
    destinations: [{ kind: 'ClusterOutput', namespace: 'logging', name: 'loki', type: 'loki', address: 'https://loki.example.com', active: true, problems: [] }],
  }],
  unrouted: { count: 1, lowerBound: false, pods: [{ namespace: 'jobs', name: 'cron-1' }], truncated: false },
  coverage: { gaps: [], namespaces: null },
  ...patch,
})

const render = () => renderToStaticMarkup(<LoggingView namespaces={[]} selectedResource={null} onOpenResource={() => {}} onCloseResource={() => {}} onClearNamespaces={() => {}} />)

describe('LoggingView', () => {
  it('draws collectors → aggregator → routes → destinations and the unrouted pods', () => {
    Object.assign(state, { data: pipeline(), error: null })
    const html = render()
    for (const text of ['default-fluentbit', '2/3', 'default-fluentd', 'ClusterFlow optin', '12', 'loki', 'https://loki.example.com', 'cron-1']) {
      expect(html).toContain(text)
    }
    expect(html.indexOf('default-fluentbit')).toBeLessThan(html.indexOf('default-fluentd'))
    expect(html.indexOf('default-fluentd')).toBeLessThan(html.indexOf('optin'))
  })
  it('says what it could not read, and shows lower bounds', () => {
    Object.assign(state, { data: pipeline({ coverage: { gaps: [{ group: 'logging.banzaicloud.io', resource: 'flows' }], namespaces: null }, unrouted: { count: 0, lowerBound: true, pods: [], truncated: false } }), error: null })
    const html = render()
    expect(html).toContain('Radar could not read Flows')
    expect(html).toMatch(/Running pods nothing routes: (<[^>]+>)*\?</)
  })
  it('says when no shipper Radar understands is installed', () => {
    Object.assign(state, { data: pipeline({ providers: [] }), error: null })
    expect(render()).toContain('No log shipper Radar understands is installed')
  })
  it('shows an older Radar as an upgrade note, not an error', () => {
    Object.assign(state, { data: undefined, error: new RadarFeatureUnsupportedError('loggingPipeline', {}) })
    expect(render()).toContain('need a newer Radar')
  })
  it('shows a destination a route names but nothing backs, instead of hiding it', () => {
    const base = pipeline()
    base.providers[0].routes[0].destinations.push({ kind: 'ClusterOutput', namespace: 'logging', name: 'gone', found: false })
    Object.assign(state, { data: base, error: null })
    expect(render()).toMatch(/gone(<[^>]+>)*[^<]*(<[^>]+>)*not found/)
    const unread = pipeline({ coverage: { gaps: [{ group: 'logging.banzaicloud.io', resource: 'clusteroutputs' }], namespaces: null } })
    unread.providers[0].routes[0].destinations.push({ kind: 'ClusterOutput', namespace: 'logging', name: 'gone', found: false })
    Object.assign(state, { data: unread, error: null })
    expect(render()).toMatch(/gone(<[^>]+>)*[^<]*(<[^>]+>)*not read/)
  })
  it('does not call routes unprocessed when the Loggings were not read', () => {
    const data = pipeline({ coverage: { gaps: [{ group: 'logging.banzaicloud.io', resource: 'loggings' }], namespaces: null } })
    data.providers[0].instances = []
    data.providers[0].routes[0].instance = ''
    Object.assign(state, { data, error: null })
    const html = render()
    expect(html).not.toContain('Not processed')
    expect(html).toContain('Loggings could not be read')
    expect(html).toContain('ClusterFlow optin')
  })
})
