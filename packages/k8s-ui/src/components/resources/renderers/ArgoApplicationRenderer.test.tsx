import { describe, expect, it } from 'vitest'
import { renderToStaticMarkup, renderToString } from 'react-dom/server'
import { ArgoApplicationRenderer } from './ArgoApplicationRenderer'

// SSR splits interpolated text with <!-- --> markers; strip so assertions read as prose.
function render(element: Parameters<typeof renderToString>[0]): string {
  return renderToString(element).replace(/<!-- -->/g, '')
}

function app(automated: Record<string, unknown> | undefined) {
  return {
    metadata: { name: 'guestbook', namespace: 'argocd' },
    spec: {
      project: 'default',
      source: { repoURL: 'https://github.com/argoproj/argocd-example-apps', path: 'guestbook', targetRevision: 'HEAD' },
      destination: { server: 'https://kubernetes.default.svc', namespace: 'guestbook' },
      syncPolicy: automated ? { automated } : {},
    },
    status: { sync: { status: 'Synced' }, health: { status: 'Healthy' } },
  }
}

function automatedSyncBadge(html: string): string | undefined {
  return html.match(/Automated Sync<\/span>.*?<span class="badge[^"]*">([^<]+)<\/span>/)?.[1]
}

describe('ArgoApplicationRenderer sync policy', () => {
  it('reads an automated block as auto-sync on', () => {
    const html = render(<ArgoApplicationRenderer data={app({ prune: true, selfHeal: true })} />)
    expect(automatedSyncBadge(html)).toBe('Enabled')
    expect(html).toContain('Self Heal')
  })

  // Argo CD 3.x switches automation off in place: the block and its
  // prune/selfHeal stay, but the controller does not auto-sync.
  it('reads automated.enabled=false as auto-sync off', () => {
    const html = render(<ArgoApplicationRenderer data={app({ enabled: false, prune: true, selfHeal: true })} />)
    expect(automatedSyncBadge(html)).toBe('Disabled')
    expect(html).not.toContain('Self Heal')
  })

  it('reads a missing automated block as auto-sync off', () => {
    const html = render(<ArgoApplicationRenderer data={app(undefined)} />)
    expect(automatedSyncBadge(html)).toBe('Disabled')
  })
})

// ManagedResourcesList appends the count to its title itself.
describe('ArgoApplicationRenderer managed resources', () => {
  it('shows the count once', () => {
    const data = app(undefined)
    const html = render(<ArgoApplicationRenderer data={{ ...data, status: { ...data.status, resources: [{ kind: 'Service', name: 'guestbook-ui', namespace: 'guestbook', status: 'Synced' }] } }} />)
    expect(html).toContain('Managed Resources (1)')
    expect(html).not.toContain('(1) (1)')
  })
})

describe('Argo Helm value files', () => {
  it.each([true, false, undefined])('shows missing-file behavior for single and multiple sources: %s', optional => {
    const source = { chart: 'cluster', repoURL: 'https://cloudnative-pg.github.io/charts', helm: { valueFiles: ['values.yaml', 'overrides.yaml'], ignoreMissingValueFiles: optional } }
    for (const spec of [{ source }, { sources: [source] }]) {
      const html = renderToStaticMarkup(<ArgoApplicationRenderer data={{ metadata: { name: 'app' }, spec }} />)
      expect(html).toContain('values.yaml, overrides.yaml')
      expect(html).toContain('If a Value File Is Missing')
      expect(html).toContain(optional ? 'Skipped' : 'Render fails')
    }
  })
})
