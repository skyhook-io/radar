import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it, vi } from 'vitest'
import {
  LocalConnectionSettings,
  type IntegrationProfile,
  type IntegrationProfiles
} from './LocalConnectionSettings'

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
const sharedConnection: NonNullable<IntegrationProfile['connection']> = {
  id: 'shared',
  type: 'metrics',
  name: 'Development metrics',
  customName: '',
  url: 'https://metrics',
  headerKeys: ['Authorization', 'X-Scope-OrgID'],
  envHeaderKeys: [],
  secretSet: false,
  insecureTls: false,
  uses: ['development', 'staging'].map((context) => ({
    binding: context,
    integration: 'metrics',
    context,
    source: '/configs/team',
    inFileName: context,
    availability: 'available'
  }))
}
const sharedProfile: Partial<IntegrationProfile> = {
  state: 'saved',
  url: 'https://metrics',
  headerKeys: sharedConnection.headerKeys,
  connection: sharedConnection,
  target: { ...profile.target, binding: 'development' }
}

describe('Local saved connections', () => {
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
    expect(html).toContain('Apply now')
    expect(html).toContain('No headers configured')
    expect(html).toContain('Storage and cluster identity')
    expect(html).toContain('aria-expanded="false"')
    expect(html).not.toContain('Connection name')
    expect(html).not.toContain('Previously saved connection')
  })
  it('names common compatible backends without claiming an exhaustive list', () => {
    const html = render({ headerKeys: ['Authorization', 'X-Scope-OrgID'] })
    expect(html).toContain(
      'such as Prometheus, VictoriaMetrics, Thanos or Grafana Mimir'
    )
    expect(html).toContain('Authentication headers')
    expect(html).toContain('Authorization, X-Scope-OrgID')
    expect(html).toContain('Edit headers')
  })
  it('offers explicit adoption and never activates legacy credentials by default', () => {
    const html = render({
      legacy: {
        url: 'https://legacy',
        headerKeys: ['Authorization'],
        secretSet: true,
        revision: 'legacy'
      }
    })
    expect(html).toContain('Use for this cluster')
    expect(html).toContain('Stop offering these older settings')
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
    expect(html).not.toContain('Apply now')
    expect(html).not.toContain('Use saved connection…')
  })
  it('requires review for changed cluster identity', () => {
    const html = render({
      state: 'target_changed',
      url: 'https://saved',
      error: 'Review the changed cluster connection'
    })
    expect(html).toContain('Review changes')
    expect(html).not.toContain('Apply now')
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
      expect(html).not.toContain('Apply now')
    }
  )
  it('requires choosing edit scope before exposing a shared editor', () => {
    const html = render(sharedProfile)
    expect(html).toContain('Edit shared connection')
    expect(html).toContain('Customize for development')
    expect(html).toContain('Shared by 2 contexts')
    expect(html).toContain('development (current)')
    expect(html).toContain('Endpoint')
    expect(html).not.toContain('2 headers configured')
    expect(html).toContain('Authorization, X-Scope-OrgID')
    expect(html).not.toContain('Connection checked')
    expect(html).not.toContain('Apply now')
    expect(html).not.toContain('Reload')
    expect(html.indexOf('Edit shared connection')).toBeLessThan(
      html.indexOf('Endpoint')
    )
    expect(html.split('Source details')[0]).not.toContain('/configs/team')
    expect(html).toContain('/configs/team')
  })
  it('identifies environment-backed headers without displaying their values', () => {
    expect(
      render({ ...sharedProfile, envHeaderKeys: ['Authorization'] })
    ).toContain('Authorization (environment), X-Scope-OrgID')
  })
  it.each(['argocd', 'cost'] as const)(
    'summarizes configured credentials for %s',
    (kind) => {
      const html = render(
        { ...sharedProfile, secretSet: true, insecureTls: true },
        kind
      )
      expect(html).toContain(
        kind === 'argocd' ? 'Token configured' : 'API key configured'
      )
      if (kind === 'argocd') {
        expect(html).toContain('TLS verification')
        expect(html).toContain('>Off<')
      }
    }
  )
  it('disambiguates duplicate context names before opening source details', () => {
    const uses = sharedConnection.uses.map((use, index) => ({
      ...use,
      context: 'development',
      source: `/configs/team-${index}`
    }))
    const visibleSummary = render({
      ...sharedProfile,
      connection: { ...sharedConnection, uses }
    }).split('Source details')[0]
    expect(visibleSummary).toContain('/configs/team-0')
    expect(visibleSummary).toContain('/configs/team-1')
  })
  it('bounds the summary while retaining the current context and unavailable warning', () => {
    const uses = Array.from({ length: 7 }, (_, index) => ({
      ...sharedConnection.uses[0],
      binding: `context-${index}`,
      context: `context-${index}`,
      availability: 'unavailable' as const
    }))
    const html = render({
      ...sharedProfile,
      target: { ...profile.target, binding: 'context-6' },
      connection: { ...sharedConnection, uses }
    })
    const visibleSummary = html.split('All contexts and source details')[0]
    expect(visibleSummary).toContain('context-6 (current)')
    expect(visibleSummary).not.toContain('context-5')
    expect(visibleSummary).toContain('+3 more')
    expect(visibleSummary).toContain(
      '7 contexts are not available in this session'
    )
    expect(html).toContain('context-5')
    expect(html.match(/not available in this session/g)).toHaveLength(1)
  })
})
