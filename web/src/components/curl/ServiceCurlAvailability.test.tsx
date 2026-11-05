import { renderToStaticMarkup } from 'react-dom/server'
import { expect, it } from 'vitest'
import { ServicePortCards } from '@skyhook-io/k8s-ui/components/resources/renderers/ServiceRenderer'
import { CurlButton } from './ServiceCurlButton'

it.each([
  ['ClusterIP', '10.0.0.8', { app: 'demo' }, true],
  ['LoadBalancer', '10.0.0.8', { app: 'demo' }, true],
  ['ClusterIP', 'None', { app: 'demo' }, false],
  ['ClusterIP', undefined, { app: 'demo' }, false],
  ['ClusterIP', '10.0.0.8', {}, false],
  ['ExternalName', undefined, {}, false],
] as const)('only offers supported Service requests through the shared workload port cards: %s %s', (type, clusterIP, selector, offered) => {
  const service = { metadata: { namespace: 'team', name: 'app' }, spec: { type, clusterIP, selector, ports: [{ port: 80, protocol: 'TCP' }] } }
  const html = renderToStaticMarkup(<ServicePortCards service={service} renderPortAction={(props) => <CurlButton service={props} active={false} onClick={() => {}} />} />)
  expect(html.includes('aria-expanded')).toBe(offered)
  // Port information remains available even when the HTTP action is unsupported.
  expect(html).toContain('80')
})
