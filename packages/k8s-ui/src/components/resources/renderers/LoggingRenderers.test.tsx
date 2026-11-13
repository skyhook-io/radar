import { describe, expect, it } from 'vitest'
import { renderToString } from 'react-dom/server'
import { LoggingFlowRenderer, LoggingOutputRenderer, LoggingRenderer } from './LoggingRenderers'

const text = (h: string) => h.replace(/<!-- -->/g, '')
const output = {
  apiVersion: 'logging.banzaicloud.io/v1beta1', kind: 'ClusterOutput', metadata: { name: 'loki', namespace: 'logging-system' },
  spec: { loki: { url: 'https://user:pw@loki.example/push', password: { value: 's3cret' }, tenant: 'tenant-a' } },
  status: { active: true, problems: ['secret not found'], problemsCount: 1 },
}
const flow = {
  apiVersion: 'logging.banzaicloud.io/v1beta1', kind: 'ClusterFlow', metadata: { name: 'optin', namespace: 'logging-system' },
  spec: { match: [{ select: { labels: { 'example.com/loki-logs': 'true' } } }], globalOutputRefs: ['loki'], filters: [{ dedot: {} }] },
  status: { active: true },
}

describe('Logging operator drawers', () => {
  it('Output drawer shows type and redacted address only', () => {
    const html = text(renderToString(<LoggingOutputRenderer data={output} />))
    expect(html).toContain('loki')
    expect(html).toContain('https://loki.example/push')
    for (const leak of ['s3cret', 'user:pw', 'tenant-a']) expect(html).not.toContain(leak)
  })
  it('Output drawer lists problems verbatim', () => {
    expect(text(renderToString(<LoggingOutputRenderer data={output} />))).toContain('secret not found')
  })
  it('Flow drawer lists rules and output refs', () => {
    const html = text(renderToString(<LoggingFlowRenderer data={flow} />))
    expect(html).toContain('label example.com/loki-logs=true')
    expect(html).toContain('loki')
    expect(html).toContain('All namespaces')
  })
  it('ClusterFlow outside the control namespace is marked', () => {
    const html = text(renderToString(<LoggingFlowRenderer data={{ ...flow, metadata: { name: 'stray', namespace: 'team' } }} controlNamespaces={['logging-system']} />))
    expect(html).toContain('Not counted: outside the Logging control namespace')
    expect(text(renderToString(<LoggingFlowRenderer data={flow} controlNamespaces={['logging-system']} />))).not.toContain('Not counted')
  })
  it('Logging drawer shows the control namespace', () => {
    const html = text(renderToString(<LoggingRenderer data={{ apiVersion: 'logging.banzaicloud.io/v1beta1', kind: 'Logging', metadata: { name: 'root' }, spec: { controlNamespace: 'logging-system', fluentd: {} } }} />))
    expect(html).toContain('logging-system')
  })
})
