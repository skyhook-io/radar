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
