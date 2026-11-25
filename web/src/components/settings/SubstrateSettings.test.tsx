import { renderToStaticMarkup } from 'react-dom/server'
import { MemoryRouter } from 'react-router-dom'
import { describe, expect, it, vi } from 'vitest'
import { SubstrateSettings } from './SubstrateSettings'
import { apiDisabledTier, connection, managedConnection, mintDeniedTier, sharedTier, status } from '../substrate/substrate.fixtures'

const state = vi.hoisted(() => ({
  connection: undefined as unknown,
  status: undefined as unknown,
  upgrade: false,
}))

vi.mock('../../api/client', async (importOriginal) => ({
  ...(await importOriginal<typeof import('../../api/client')>()),
  usePrometheusStatus: () => ({ data: { connected: true } }),
}))
vi.mock('../../api/substrateWorkspace', async (importOriginal) => ({
  ...(await importOriginal<typeof import('../../api/substrateWorkspace')>()),
  useSubstrateConnection: () => ({ data: state.connection, isError: false, upgrade: state.upgrade, blockedReason: state.upgrade ? 'Agent Substrate needs a newer Radar on this cluster.' : undefined }),
  useSubstrateStatus: () => ({ data: state.status, isError: false, upgrade: false }),
  useUpdateSubstrateConnection: () => ({ mutate: vi.fn(), reset: vi.fn(), isPending: false, error: null }),
}))

const text = (html: string) => html.replace(/&#x27;/g, "'").replace(/&quot;/g, '"').replace(/&amp;/g, '&')

// Status polls the same access the view was read with.
function render(conn: { access?: unknown }, over: Parameters<typeof status>[0] = {}) {
  state.connection = conn
  state.status = status({ apiTier: conn.access as never, ...over })
  return text(renderToStaticMarkup(
    <MemoryRouter>
      <SubstrateSettings active onDirtyChange={() => {}} onBusyChange={() => {}} />
    </MemoryRouter>,
  ))
}

describe('Settings → Agent Substrate on a local Radar', () => {
  it('leads with who Radar reads as, recommends a token file, and saves only after an edit', () => {
    const html = render(connection())
    expect(html).toContain('Who Radar reads Substrate as')
    expect(html).toContain('A token file')
    expect(html).toContain('Recommended')
    expect(html).toContain('Radar saves only the path, and never sends the token to the browser.')
    expect(html).toContain('Substrate’s shared client')
    expect(html).toContain('This identity may be a global owner')
    expect(html).toContain('Radar found Service ')
    expect(html).toContain('Advanced: API server address, TLS and atespaces')
    // The fallback atespaces are never offered as a limit on access.
    expect(html).toContain('These names don’t limit what it can read.')
    expect(html.indexOf('Who Radar reads Substrate as')).toBeLessThan(html.indexOf('Advanced: API server address'))
    // Nothing edited: nothing to save, and nothing saved to disconnect.
    expect(html).toMatch(/<button type="button" disabled=""[^>]*>Save changes<\/button>/)
    expect(html).not.toContain('Disconnect API')
  })

  it('offers Disconnect apart from saving, in the destructive tone, once a connection is saved', async () => {
    const { toneTextClass } = await import('@skyhook-io/k8s-ui')
    const html = render(connection({ saved: true, settings: { identity: 'tokenFile', tokenFile: '/Users/me/.radar/viewer.token', caFile: '/Users/me/.radar/a-long-ca-bundle.pem' } }))
    const at = html.indexOf('>Disconnect API</button>')
    expect(at).toBeGreaterThan(html.indexOf('Save changes'))
    expect(html.slice(html.lastIndexOf('<button', at), at)).toContain(toneTextClass('unhealthy'))
    // A long path is inspectable whole, on hover.
    expect(html).toContain('title="/Users/me/.radar/a-long-ca-bundle.pem"')
  })

  it('unfolds the shared identity’s warning once that identity is chosen', () => {
    const html = render(connection({ saved: true, settings: { identity: 'shared' } }))
    const at = html.indexOf('This identity may be a global owner')
    const fold = html.lastIndexOf('<div class="relative overflow-hidden"', at)
    expect(html.slice(fold, at)).not.toContain('inert')
  })

  it('asks to review a connection saved for another cluster before using it here', () => {
    const html = render(connection({ saved: true, targetChanged: true, settings: { identity: 'shared' }, access: { state: 'failed', code: 'targetChanged' } }))
    expect(html).toContain('Saved for another cluster')
    expect(html).toContain('>Use it for this cluster</button>')
    expect(html).toContain('Disconnect API instead')
    expect(html).toMatch(/<fieldset disabled=""/)
  })

  it('says when Substrate isn’t installed instead of offering a form', () => {
    const html = render(connection(), { installed: false })
    expect(html).toContain('Agent Substrate isn’t installed on this cluster')
    expect(html).not.toContain('Who Radar reads Substrate as')
  })

  it('says an older Radar can’t serve these settings', () => {
    state.upgrade = true
    const html = render(connection())
    state.upgrade = false
    expect(html).toContain('needs a newer Radar')
    expect(html).not.toContain('Who Radar reads Substrate as')
  })
})

describe('Settings → Agent Substrate on an in-cluster Radar or Radar Hub', () => {
  it('shows how the installation reads Substrate as text, never as inputs, with the Helm values that set it', () => {
    const html = render(managedConnection(), { canConfigure: false })
    expect(html).toContain('Your access')
    expect(html).toContain('Connected')
    expect(html).toContain('For each signed-in person, with a token created with their own Kubernetes access.')
    expect(html).toContain('api.ate-system.svc:443')
    expect(html).toContain('The cluster’s trust bundle for Substrate')
    expect(html).toContain('Managed by your operator')
    expect(html).toContain('substrate.api')
    expect(html).not.toContain('<input')
    expect(html).not.toContain('Who Radar reads Substrate as')
  })

  it('tells a person whose access can’t mint the token what to ask for', () => {
    const html = render(managedConnection({ access: mintDeniedTier }), { canConfigure: false })
    expect(html).toContain('Your access can’t create the shared identity’s token')
    expect(html).toContain('kubectl auth can-i create serviceaccounts/ate-client --subresource=token -n ate-system')
  })

  it('says the operator turned the API off, or that a Radar without sign-in can’t read it', () => {
    const off = render(managedConnection({ access: apiDisabledTier, managed: { ...managedConnection().managed, enabled: false, readsAs: 'none' } }), { canConfigure: false })
    expect(off).toMatch(/Reads Substrate’s API<\/dt><dd[^>]*>No/)
    expect(off).toContain('Turned off for this Radar')
    expect(off).not.toContain('For each signed-in person')
    const noSignIn = render(managedConnection({ access: sharedTier, managed: { ...managedConnection().managed, enabled: false, readsAs: 'none' } }), { canConfigure: false })
    expect(noSignIn).toContain('Needs sign-in')
  })

  it('names fallback atespaces only to a connected reader', () => {
    const connected = render(managedConnection({ managed: { ...managedConnection().managed, atespaces: ['research'] } }), { canConfigure: false })
    expect(connected).toContain('research')
    const refused = render(managedConnection({ access: mintDeniedTier }), { canConfigure: false })
    expect(refused).not.toMatch(/Atespaces<\/span>/)
  })
})
