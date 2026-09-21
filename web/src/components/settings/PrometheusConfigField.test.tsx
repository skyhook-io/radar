import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it, vi } from 'vitest'
import { PrometheusConfigField, type PrometheusProfileView } from './PrometheusConfigField'

const view: PrometheusProfileView = {
  target: { binding: 'opaque', context: 'development', source: '/configs/team', fingerprint: 'target', clientGeneration: 1, operationGeneration: 1 },
  revision: 'revision', state: 'auto', url: '', headerKeys: [], headersManaged: false,
}
const render = (changes: Partial<PrometheusProfileView>) => renderToStaticMarkup(
  <PrometheusConfigField profile={{ ...view, ...changes }} local value="" configuredHeaderKeys={[]} serverManaged={false} headersManaged={false} urlFromFlag={false} onChange={vi.fn()} onProfileChange={vi.fn()} onReload={vi.fn()} />,
)

describe('Per-cluster metrics settings', () => {
  it('keeps the normal connection form scoped and identity details collapsed', () => {
    const html = render({})
    expect(html).toContain('Saved for development')
    expect(html).toContain('Apply now')
    expect(html).toContain('aria-expanded="false"')
    expect(html).not.toContain('Previously saved connection')
  })
  it('makes compatible backends and authentication distinct from storage details', () => {
    const html = render({ headerKeys: ['Authorization', 'X-Scope-OrgID'] })
    expect(html).toContain('Connect a Prometheus-compatible backend, such as Prometheus, VictoriaMetrics, Thanos or Grafana Mimir.')
    expect(html).toContain('Available charts depend on the metrics collected.')
    expect(html).toMatch(/<h4[^>]*>Authentication headers<\/h4>/)
    expect(html).toContain('Authorization, X-Scope-OrgID')
    expect(html).toContain('Changing servers requires replacing or clearing the saved headers.')
    expect(html).toMatch(/<\/section>[\s\S]*Storage and cluster identity/)
  })
  it('keeps credential replacement guidance out of the empty authentication state', () => {
    const html = render({})
    expect(html).toContain('No headers configured')
    expect(html).not.toContain('Changing servers requires')
  })
  it('directs environment-managed header changes to startup configuration', () => {
    const html = render({ headerKeys: ['Authorization'], headersManaged: true })
    expect(html).toContain('Headers are controlled by startup configuration.')
    expect(html).not.toContain('Changing servers requires')
    expect(html).not.toMatch(/<button[^>]*>Edit headers<\/button>/)
    expect(html).not.toContain('Clear saved headers')
  })
  it('offers explicit legacy association without exposing credential values', () => {
    const html = render({ legacy: { url: 'https://legacy', headerKeys: ['Authorization'], revision: 'legacy' } })
    expect(html).toContain('Save for this cluster')
    expect(html).toContain('Stop offering legacy settings for all clusters')
    expect(html).toContain('Authorization (values hidden)')
  })
  it('makes launch overrides read-only and says how to edit saved settings', () => {
    const html = render({ state: 'launch', url: 'https://temporary', headerKeys: ['Authorization'] })
    expect(html).toContain('Set for this launch')
    expect(html).toContain('Restart without Prometheus flags')
    expect(html).not.toContain('Apply now')
  })
  it('requires explicit reconfirmation for a changed target', () => {
    const html = render({ state: 'target_changed', url: 'https://saved' })
    expect(html).toContain('Use this connection for development')
    expect(html).toContain('saved metrics connection is paused')
    expect(html).not.toContain('Apply now')
  })
  it('does not offer to overwrite malformed files', () => {
    const html = render({ state: 'error', error: 'Repair clusters.json' })
    expect(html).toContain('Repair clusters.json')
    expect(html).toContain('Reload settings')
    expect(html).not.toContain('Apply now')
  })
})
