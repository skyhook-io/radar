// @vitest-environment jsdom
import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it } from 'vitest'
import { TrafficData } from './TrafficData'
import { dataByProtocol } from './trafficFilters'

describe('TrafficData', () => {
  it('keeps single-protocol and released aggregate figures unlabeled', () => {
    for (const bytesByProtocol of [undefined, { http: { bytesSent: 100, bytesRecv: 200 } }]) {
      expect(renderToStaticMarkup(<TrafficData entries={dataByProtocol({ bytesSent: 100, bytesRecv: 200, bytesByProtocol })} directional explainOverlap />)).toBe('300 B')
    }
  })

  it('keeps unmeasured data absent even with zero-byte buckets', () => {
    expect(renderToStaticMarkup(<TrafficData entries={[{ protocol: 'tcp', sent: 0, recv: 0 }]} />)).toBe('—')
  })

  for (const protocols of [['http', 'tcp'], ['grpc', 'tcp'], ['tcp', 'udp'], ['http', 'grpc']]) {
    it(`explains overlapping measurement types only for ${protocols.join('+')}`, () => {
      const entries = protocols.map(protocol => ({ protocol, sent: 100, recv: 200 }))
      const html = renderToStaticMarkup(<TrafficData entries={entries} directional explainOverlap />)
      expect(html).toContain('100 B sent · 200 B received')
      expect(html).not.toContain('600 B')
      if (protocols.includes('tcp') && protocols.some(p => p === 'http' || p === 'grpc')) {
        expect(html).toContain(`${protocols[0] === 'grpc' ? 'gRPC' : 'HTTP'} body bytes and TCP connection bytes can describe the same calls; shown separately.`)
      } else expect(html).not.toContain('body bytes')
    })
  }
})


it('keeps an observed zero-byte protocol beside a measured bucket', () => {
  const html = renderToStaticMarkup(<TrafficData entries={[{ protocol: 'http', sent: 0, recv: 0 }, { protocol: 'tcp', sent: 100, recv: 200 }]} compact />)
  expect(html).toBe('HTTP 0 B · TCP 300 B')
})
