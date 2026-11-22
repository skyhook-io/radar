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
    expect(html).toContain('Outside every Logging&#x27;s control namespace')
    expect(html).not.toContain('forwards nothing')
    expect(text(renderToString(<LoggingFlowRenderer data={flow} controlNamespaces={['logging-system']} />))).not.toContain('Outside every Logging')
  })
  it('links a namespaced Flow to a ClusterOutput only when its namespace is known', () => {
    const nsFlow = { ...flow, kind: 'Flow', metadata: { name: 'app', namespace: 'team' } }
    const navigated: any[] = []
    const html = text(renderToString(<LoggingFlowRenderer data={nsFlow} onNavigate={(r) => navigated.push(r)} />))
    expect(html).toContain('ClusterOutput loki')
    expect(html).not.toMatch(/<button[^>]*>[^<]*ClusterOutput loki/)
    const linked = text(renderToString(<LoggingFlowRenderer data={nsFlow} onNavigate={() => {}} controlNamespaces={['logging-system']} />))
    expect(linked).toMatch(/<button[^>]*>[^<]*ClusterOutput loki/)
  })
  it('says when Loggings could not be read', () => {
    const html = text(renderToString(<LoggingFlowRenderer data={flow} loggingsUnreadable />))
    expect(html).toContain('Could not read Loggings')
    expect(html).not.toContain('Outside every Logging')
  })
  it('marks deprecated outputRefs as ignored, not as outputs', () => {
    const html = text(renderToString(<LoggingFlowRenderer data={{ ...flow, kind: 'Flow', metadata: { name: 'app', namespace: 'team' }, spec: { outputRefs: ['old'] } }} />))
    expect(html).toContain('outputRefs old: deprecated, ignored by the operator.')
    expect(html).toContain('No outputs: selected logs go nowhere.')
    expect(html).not.toContain('Output old')
  })
  it('says a Flow with match and selectors is invalid', () => {
    const html = text(renderToString(<LoggingFlowRenderer data={{ ...flow, spec: { ...flow.spec, selectors: { app: 'x' } } }} />))
    expect(html).toContain('Invalid ClusterFlow')
    expect(html).toContain('match and selectors are both set')
  })
  it('says a Flow naming an output Radar did not find is invalid', () => {
    const html = text(renderToString(<LoggingFlowRenderer data={flow} missingOutputs={['ClusterOutput gone']} />))
    expect(html).toContain('Invalid ClusterFlow')
    expect(html).toContain('ClusterOutput gone')
  })
  it('says a Flow naming a protected ClusterOutput is invalid', () => {
    const html = text(renderToString(<LoggingFlowRenderer data={{ ...flow, kind: 'Flow', metadata: { name: 'app', namespace: 'team' } }} protectedOutputs={['audit']} />))
    expect(html).toContain('Invalid Flow')
    expect(html).toContain('protected ClusterOutput audit')
    expect(html).not.toContain('Rejected')
  })
  it('tells an empty match list from no match', () => {
    expect(text(renderToString(<LoggingFlowRenderer data={{ ...flow, spec: { match: [] } }} />))).toContain('Empty match: the operator selects no pod.')
    expect(text(renderToString(<LoggingFlowRenderer data={{ ...flow, spec: {} }} />))).toContain('No rules: every pod in scope is selected.')
  })
  it('lists namespaces_regex conditions', () => {
    expect(text(renderToString(<LoggingFlowRenderer data={{ ...flow, spec: { match: [{ select: { namespaces_regex: ['^team-'] } }] } }} />))).toContain('namespace matching [^team-]')
  })
  it('Logging drawer shows the control namespace', () => {
    const html = text(renderToString(<LoggingRenderer data={{ apiVersion: 'logging.banzaicloud.io/v1beta1', kind: 'Logging', metadata: { name: 'root' }, spec: { controlNamespace: 'logging-system', fluentd: {} } }} />))
    expect(html).toContain('logging-system')
  })
  it('never says the aggregator is not configured, since it may come from another resource', () => {
    const logging = (spec: any) => text(renderToString(<LoggingRenderer data={{ apiVersion: 'logging.banzaicloud.io/v1beta1', kind: 'Logging', metadata: { name: 'root' }, spec }} />))
    expect(logging({ fluentd: {} })).toContain('Fluentd, configured in this Logging')
    expect(logging({ syslogNG: {} })).toContain('syslog-ng, configured in this Logging')
    const unset = logging({})
    expect(unset).toContain('Not set in this Logging (may come from a FluentdConfig or SyslogNGConfig)')
    expect(unset).not.toContain('Not configured')
  })
})
