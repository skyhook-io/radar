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

it('shows the desired image without claiming any instance runs it', () => {
  const html = renderToStaticMarkup(<CNPGClusterHASection ha={noInstances} />)
  expect(html).toContain('Desired image pg:17; no instance running')
  expect(html).not.toContain('every instance runs it')
})

it('describes matching Pod images without claiming the Pods are running', () => {
  const ha: CNPGClusterHA = { ...noInstances, instances: [{ pod: 'pg-1', podUID: 'p1', role: 'primary', ready: false, restartCount: 0, image: 'pg:17', imageMatches: true }] }
  const html = renderToStaticMarkup(<CNPGClusterHASection ha={ha} />)
  expect(html).toContain('observed Pod images match')
  expect(html).not.toContain('instances run it')
})

it('keeps the readiness statement inside expanded HA and lists missing expected instances without an observed role', () => {
  const ha: CNPGClusterHA = { ...noInstances, declaredInstances: 2, expectedInstances: ['orders-1', 'orders-2'], instances: [{ pod: 'orders-1', podUID: 'a', role: 'primary', ready: true, restartCount: 0 }], jobs: { state: 'ok', items: [{ name: 'orders-2-join', role: 'join', phase: 'pending', reason: 'Pod cannot be scheduled: Unschedulable: 0/2 nodes are available: 2 Too many pods. preemption: no victims.' }] } }
  const html = renderToStaticMarkup(<CNPGClusterHASection ha={ha} showInstances={false} />)
  expect(html).toContain('1 of 2 declared instances ready; no instance Pod observed for orders-2')
  expect(html).toContain('orders-1')
  expect(html).toContain('orders-2</span> · not running')
  expect(html).toContain('Cannot be scheduled: both nodes have reached their Pod limit')
  expect(html).toContain('Scheduler message')
  expect(html).toContain('aria-expanded="false"')
  expect(html).toContain('preemption: no victims')
})
