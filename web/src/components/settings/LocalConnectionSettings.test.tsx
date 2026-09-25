import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it, vi } from 'vitest'
import {
  LocalConnectionSettings,
  type IntegrationProfile,
  type IntegrationProfiles
} from './LocalConnectionSettings'
import { PreviousIntegrationSettingsNotice } from './LocalConfigurationDetails'

const profile: IntegrationProfile = {
  target: {
    binding: 'opaque',
    context: 'development',
    source: '/configs/team',
    inFileName: 'dev',
    fingerprint: 'target',
    clientGeneration: 1,
    operationGeneration: 1,
    identity: {
      server: 'https://kubernetes',
      user: 'developer',
      trust: 'trust',
      insecureTls: false
    }
  },
  revision: 'revision',
  state: 'auto',
  mode: 'auto',
  url: '',
  headerKeys: [],
  envHeaderKeys: [],
  headersManaged: false,
  secretSet: false,
  insecureTls: false,
  clusterId: ''
}
const render = (
  changes: Partial<IntegrationProfile>,
  kind: keyof IntegrationProfiles = 'metrics'
) => {
  const profiles: IntegrationProfiles = {
    metrics: profile,
    argocd: profile,
    cost: profile,
    [kind]: { ...profile, ...changes }
  }
  return renderToStaticMarkup(
    <LocalConnectionSettings
      kind={kind}
      profiles={profiles}
      onChange={vi.fn()}
      onDirtyChange={vi.fn()}
    />
  )
}
const savedProfile: Partial<IntegrationProfile> = {
  state: 'saved',
  url: 'https://metrics',
  headerKeys: ['Authorization', 'X-Scope-OrgID'],
  target: { ...profile.target, binding: 'development' }
}

describe('Local saved connections', () => {
  it('only advertises previous settings for unconfigured integrations', () => {
    const legacy = { url: 'https://previous', headerKeys: [], envHeaderKeys: [], secretSet: false, insecureTls: false, mode: 'auto', clusterId: '', revision: 'previous' }
    const profiles: IntegrationProfiles = {
      metrics: { ...profile, legacy },
      argocd: { ...profile, state: 'saved', legacy },
      cost: { ...profile, state: 'launch', legacy },
    }
    const html = renderToStaticMarkup(<PreviousIntegrationSettingsNotice profiles={profiles} onNavigate={vi.fn()} />)
    expect(html).toContain('Review Metrics')
    expect(html).not.toContain('Review Argo CD')
    expect(html).not.toContain('Review Cost')
    delete profiles.metrics.legacy
    expect(renderToStaticMarkup(<PreviousIntegrationSettingsNotice profiles={profiles} onNavigate={vi.fn()} />)).toBe('')
  })
  it.each(['metrics', 'argocd', 'cost'] as const)(
    'does not show a routine reload action for %s',
    (kind) => {
      for (const state of [
        'auto',
        'saved',
        'launch',
        'target_changed'
      ] as const) {
        expect(render({ state }, kind)).not.toContain('Reload')
      }
    }
  )
  it('keeps first setup a form, with no mandatory name or assignment step', () => {
    const html = render({})
    expect(html).toContain('Save changes')
    expect(html).toContain('Add header')
    expect(html).not.toContain('Copy from another cluster')
    expect(html).toContain('Cluster settings identity')
    expect(html).not.toContain('Previously saved connection')
  })
  it('names common compatible backends without claiming an exhaustive list', () => {
    const html = render({ headerKeys: ['Authorization', 'X-Scope-OrgID'] })
    expect(html).toContain(
      'such as Prometheus, VictoriaMetrics, Thanos or Grafana Mimir'
    )
    expect(html).toContain('Authentication headers')
    expect(html).toContain('Authorization value')
    expect(html).toContain('X-Scope-OrgID value')
    expect(html).toContain('Saved value')
    expect(html).not.toContain('Edit headers')
    expect(html).not.toContain('<select')
  })
  it('offers explicit adoption and never activates legacy credentials by default', () => {
    const html = render({
      legacy: {
        url: 'https://legacy',
        headerKeys: ['Authorization'],
        envHeaderKeys: [],
        insecureTls: false,
        mode: 'connection',
        clusterId: '',
        secretSet: true,
        revision: 'legacy'
      }
    })
    expect(html).toContain('Use previous settings')
    expect(html).toContain('Dismiss for this cluster')
  })
  it('keeps launch overrides read-only with a way to change their source', () => {
    const html = render({
      state: 'launch',
      url: 'https://temporary',
      headerKeys: ['Authorization']
    })
    expect(html).toContain('Set for this launch')
    expect(html).toContain(
      'Restart without its startup flags or environment configuration'
    )
    expect(html).not.toContain('Save changes')
  })
  it('requires review for changed cluster identity', () => {
    const html = render({
      state: 'target_changed',
      url: 'https://saved',
      error: 'Review the changed cluster connection'
    })
    expect(html).toContain('Review changes')
    expect(html).not.toContain('Save changes')
  })
  it.each(['metrics', 'argocd', 'cost'] as const)(
    'offers contextual recovery rather than overwriting a malformed file for %s',
    (kind) => {
      const html = render(
        { state: 'error', error: 'Repair clusters.json' },
        kind
      )
      expect(html).toContain('Repair clusters.json')
      expect(html).toContain('Reload latest settings')
      expect(html).not.toContain('Save changes')
    }
  )
  it.each(['metrics', 'argocd', 'cost'] as const)(
    'edits only this cluster for %s',
    (kind) => {
      const html = render({ ...savedProfile, secretSet: true }, kind)
      expect(html).toContain('Save changes')
      expect(html).not.toContain('Copy from another cluster')
      expect(html).not.toContain('Reload')
    }
  )
  it('keeps file details and saved-entry cleanup out of integration tabs', () => {
    const html = render(savedProfile)
    expect(html).not.toContain('clusters.json')
  })
})
