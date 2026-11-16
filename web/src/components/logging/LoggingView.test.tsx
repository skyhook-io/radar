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
    instances: [{ name: 'default', controlNamespace: 'logging', collectorsUnread: false, aggregatorsUnread: false, collectors: [{ kind: 'DaemonSet', namespace: 'logging', name: 'default-fluentbit', ready: 2, desired: 3 }], aggregators: [{ kind: 'StatefulSet', namespace: 'logging', name: 'default-fluentd', ready: 1, desired: 1 }] }],
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
    expect(html).toMatch(/Running pods no Logging operator route selects: (<[^>]+>)*\?</)
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
  it('does not claim no collectors when FluentbitAgents were not read', () => {
    const data = pipeline({ coverage: { gaps: [{ group: 'logging.banzaicloud.io', resource: 'fluentbitagents' }], namespaces: null } })
    data.providers[0].instances[0].collectors = []
    data.providers[0].instances[0].collectorsUnread = true // the server marks it, FluentbitAgents being unread
    Object.assign(state, { data, error: null })
    const html = render()
    expect(html).toMatch(/Collectors(<[^>]+>)*Not read/)
  })
  it('says not read per Logging, not because another Logging\'s namespace was unread', () => {
    const base = pipeline({ coverage: { gaps: [{ group: 'apps', resource: 'daemonsets' }], namespaces: null } })
    base.providers[0].instances[0].collectors = []
    Object.assign(state, { data: base, error: null })
    expect(render()).toContain('Not found in logging')
    base.providers[0].instances[0].collectorsUnread = true
    expect(render()).toContain('Not read')
  })
  it('names the default flow as a route and never says unrouted logs are lost', () => {
    const data = pipeline({ unrouted: { count: 0, lowerBound: false, pods: [], truncated: false } })
    data.providers[0].routes.push({ provider: 'logging-operator', kind: 'Logging', group: 'logging.banzaicloud.io', namespace: '', name: 'default', instance: 'default', ignored: false, fallback: true, hasFilters: false,
      destinations: [{ kind: 'ClusterOutput', namespace: 'logging', name: 'loki', found: true, active: true }], selectedPods: { count: 4, lowerBound: false } })
    Object.assign(state, { data, error: null })
    const html = render()
    expect(html).toContain('default flow')
    expect(html).not.toContain('lost with the pod')
  })
  it('says where it looked for collectors', () => {
    const data = pipeline()
    data.providers[0].instances[0].collectors = []
    data.providers[0].instances[0].collectorNamespace = 'logging-agents'
    Object.assign(state, { data, error: null })
    expect(render()).toContain('Not found in logging-agents')
  })
  it('says a route is invalid, with the reason', () => {
    const data = pipeline()
    Object.assign(data.providers[0].routes[0], { ignored: true, invalid: 'it names Output gone, which Radar didn\'t find for its Logging' })
    Object.assign(state, { data, error: null })
    const html = render()
    expect(html).toContain('>invalid<')
    expect(html).toContain('it names Output gone')
    expect(html).not.toContain('rejected')
  })
  it('says when an invalid route stops its Logging rendering', () => {
    const data = pipeline()
    Object.assign(data.providers[0].routes[0], { renderBlocked: true, selectedPods: { count: 0, lowerBound: true } })
    Object.assign(state, { data, error: null })
    expect(render()).toContain('skipInvalidResources')
  })
  it('does not show a destination the route could not resolve as healthy', () => {
    const data = pipeline()
    data.providers[0].routes[0].destinations = [{ kind: 'ClusterOutput', namespace: 'logging', name: 'loki', found: false }]
    Object.assign(state, { data, error: null })
    const html = render()
    expect(html).toContain('not found')
    expect(html).not.toContain('https://loki.example.com')
  })
})
