import { renderToStaticMarkup } from 'react-dom/server'
import { expect, it } from 'vitest'
import { ServicePortCards } from '@skyhook-io/k8s-ui/components/resources/renderers/ServiceRenderer'
import { CurlButton, isHttpishPort } from './ServiceCurlButton'

// The UI keeps Curl for every Service shape, including headless, ExternalName
// and selector-less ones; only MCP's curl_service is narrower.
it.each([
  ['ClusterIP', '10.0.0.8', { app: 'demo' }],
  ['LoadBalancer', '10.0.0.8', { app: 'demo' }],
  ['ClusterIP', 'None', { app: 'demo' }],
  ['ClusterIP', undefined, { app: 'demo' }],
  ['ClusterIP', '10.0.0.8', {}],
  ['ExternalName', undefined, {}],
] as const)('preserves UI Service requests through the shared workload port cards: %s %s', (type, clusterIP, selector) => {
  const service = { metadata: { namespace: 'team', name: 'app' }, spec: { type, clusterIP, selector, ports: [{ port: 80, protocol: 'TCP' }] } }
  const html = renderToStaticMarkup(<ServicePortCards service={service} renderPortAction={() => <CurlButton active={false} onClick={() => {}} />} />)
  expect(html).toContain('aria-expanded')
  // Port information remains available even when the HTTP action is unsupported.
  expect(html).toContain('80')
})

it.each([
  ['TCP', true],
  [undefined, true],
  ['tcp', true],
  ['UDP', false],
  ['SCTP', false],
] as const)('offers Curl only on TCP ports (HTTP rides TCP): protocol %s', (protocol, offered) => {
  // Port 8080 and name http would otherwise read as HTTP.
  expect(isHttpishPort(8080, 'http', undefined, protocol)).toBe(offered)
})
