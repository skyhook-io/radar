import { describe, expect, it } from 'vitest'
import { renderToString } from 'react-dom/server'
import { IngressRenderer } from './IngressRenderer'

describe('IngressRenderer default backend', () => {
  it('shows the default backend and no "not routed" warning', () => {
    const html = renderToString(
      <IngressRenderer data={{
        metadata: { name: 'catch-all', namespace: 'team' },
        spec: { ingressClassName: 'nginx', defaultBackend: { service: { name: 'fallback', port: { number: 80 } } } },
      }} />,
    )
    expect(html).toContain('Default Backend')
    expect(html.replace(/<!-- -->/g, '')).toContain('fallback:80')
    expect(html).not.toContain('Traffic will not be routed')
  })

  it('still warns when an Ingress routes nowhere', () => {
    const html = renderToString(<IngressRenderer data={{ metadata: { name: 'empty', namespace: 'team' }, spec: { ingressClassName: 'nginx' } }} />)
    expect(html).toContain('Traffic will not be routed')
  })

  it('shows a resource default backend', () => {
    const html = renderToString(
      <IngressRenderer data={{
        metadata: { name: 'static', namespace: 'team' },
        spec: { ingressClassName: 'nginx', defaultBackend: { resource: { apiGroup: 'k8s.example.com', kind: 'StorageBucket', name: 'static-assets' } } },
      }} />,
    )
    expect(html).toContain('Default Backend')
    expect(html).toContain('StorageBucket/static-assets')
  })
})
