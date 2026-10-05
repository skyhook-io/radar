import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it } from 'vitest'
import { CNPGClusterHASection } from './CNPGClusterHASection'
import type { CNPGClusterHA } from './ha'

const noInstances: CNPGClusterHA = {
  cluster: { namespace: 'db', name: 'pg', uid: 'u' },
  sampledAt: '2026-09-30T12:00:00Z',
  desiredImage: 'pg:17',
  instances: [],
  pods: { state: 'ok' },
  nodes: { state: 'ok' },
  quorum: { enabled: false, object: { state: 'notFound' } },
  pdbs: { state: 'ok', enabled: true, items: [] },
  primaryLease: { state: 'notFound' },
  operatorLease: { state: 'unavailable' },
  jobs: { state: 'ok', items: [] },
  rwEndpoints: { state: 'ok', service: 'pg-rw', pods: [] },
  certificates: [],
  maintenance: { declared: false, inProgress: false, reusePVC: true },
}

describe('CNPGClusterHASection', () => {
  it('says there is nothing to place rather than showing only the zone source', () => {
    const html = renderToStaticMarkup(<CNPGClusterHASection ha={noInstances} showCertificates={false} />)
    expect(html).toContain('No instance Pods to place')
    expect(html).not.toContain('topology.kubernetes.io/zone of each instance')
  })
})

it('shows the read-write Service, its Pods and primary mismatch beside Reachability', () => {
  const ha: CNPGClusterHA = { ...noInstances, rwEndpoints: { state: 'ok', service: 'pg-rw', pods: ['pg-2'] } }
  const html = renderToStaticMarkup(<CNPGClusterHASection ha={ha} currentPrimary="pg-1" onOpenReachability={() => {}} />)
  expect(html).toContain('Read-write Service')
  expect(html).toContain('pg-rw')
  expect(html).toContain('pg-2')
  expect(html).toContain('not on the reported primary pg-1')
  expect(html).toContain('Reachability')
  expect(html).toContain('aria-expanded="true"')
})
it('distinguishes absent endpoints from denied endpoint reads', () => {
  expect(renderToStaticMarkup(<CNPGClusterHASection ha={noInstances} />)).toContain('No ready endpoints')
  const denied: CNPGClusterHA = { ...noInstances, rwEndpoints: { state: 'denied', service: 'pg-rw', pods: [], grant: { verb: 'list', resource: 'endpointslices', group: 'discovery.k8s.io', namespace: 'db' } } }
  const html = renderToStaticMarkup(<CNPGClusterHASection ha={denied} />)
  expect(html).toContain('endpointslices')
  expect(html).not.toContain('No ready endpoints')
})
it('does not flag deliberately absent read-write endpoints while hibernated', () => {
  const html = renderToStaticMarkup(<CNPGClusterHASection ha={noInstances} currentPrimary="pg-1" hibernated />)
  expect(html).toContain('None expected while hibernated')
  expect(html).not.toContain('No ready endpoints')
})
