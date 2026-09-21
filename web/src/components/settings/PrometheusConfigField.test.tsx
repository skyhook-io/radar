import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it, vi } from 'vitest'
import { LocalConnectionSettings, type IntegrationProfile, type IntegrationProfiles } from './LocalConnectionSettings'

const profile: IntegrationProfile = {
  target: { binding: 'opaque', context: 'development', source: '/configs/team', inFileName: 'dev', fingerprint: 'target', clientGeneration: 1, operationGeneration: 1, identity: { server: 'https://kubernetes', user: 'developer', trust: 'trust', insecureTls: false } },
  revision: 'revision', state: 'auto', mode: 'auto', url: '', headerKeys: [], envHeaderKeys: [], headersManaged: false, secretSet: false, insecureTls: false, clusterId: '',
}
const render = (changes: Partial<IntegrationProfile>) => {
  const profiles: IntegrationProfiles = { metrics: { ...profile, ...changes }, argocd: profile, cost: profile }
  return renderToStaticMarkup(<LocalConnectionSettings kind="metrics" profiles={profiles} onChange={vi.fn()} onDirtyChange={vi.fn()} />)
}

describe('Local saved connections', () => {
  it('keeps first setup a form, with no mandatory name or assignment step', () => {
    const html = render({})
    expect(html).toContain('Apply now')
    expect(html).toContain('No headers configured')
    expect(html).toContain('Storage and cluster identity')
    expect(html).toContain('aria-expanded="false"')
    expect(html).not.toContain('Connection name')
    expect(html).not.toContain('Previously saved connection')
  })
  it('names common compatible backends without claiming an exhaustive list', () => {
    const html = render({ headerKeys: ['Authorization', 'X-Scope-OrgID'] })
    expect(html).toContain('such as Prometheus, VictoriaMetrics, Thanos or Grafana Mimir')
    expect(html).toContain('Authentication headers')
    expect(html).toContain('Authorization, X-Scope-OrgID')
    expect(html).toContain('Edit headers')
  })
  it('offers explicit adoption and never activates legacy credentials by default', () => {
    const html = render({ legacy: { url: 'https://legacy', headerKeys: ['Authorization'], secretSet: true, revision: 'legacy' } })
    expect(html).toContain('Use for this cluster')
    expect(html).toContain('Stop offering these older settings')
  })
  it('keeps launch overrides read-only with a way to change their source', () => {
    const html = render({ state: 'launch', url: 'https://temporary', headerKeys: ['Authorization'] })
    expect(html).toContain('Set for this launch')
    expect(html).toContain('Restart without its startup flags or environment configuration')
    expect(html).not.toContain('Apply now')
    expect(html).not.toContain('Use saved connection…')
  })
  it('requires review for changed cluster identity', () => {
    const html = render({ state: 'target_changed', url: 'https://saved', error: 'Review the changed cluster connection' })
    expect(html).toContain('Review changes')
    expect(html).not.toContain('Apply now')
  })
  it('does not offer to overwrite a malformed file', () => {
    const html = render({ state: 'error', error: 'Repair clusters.json' })
    expect(html).toContain('Repair clusters.json')
    expect(html).toContain('Reload settings')
    expect(html).not.toContain('Apply now')
  })
  it('requires choosing edit scope before exposing a shared editor', () => {
    const html = render({ state: 'saved', url: 'https://metrics', connection: {
      id: 'shared', type: 'metrics', name: 'Metrics · metrics', customName: '', url: 'https://metrics', headerKeys: [], envHeaderKeys: [], secretSet: false, insecureTls: false,
      uses: ['development', 'staging'].map(context => ({ binding: context, integration: 'metrics', context, source: '/configs/team', inFileName: context, availability: 'available' })),
    } })
    expect(html).toContain('Edit shared connection')
    expect(html).toContain('Customize for this cluster')
    expect(html).toContain('Used by 2 contexts')
    expect(html).not.toContain('Apply now')
  })
})
