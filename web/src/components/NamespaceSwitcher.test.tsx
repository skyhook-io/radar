import { renderToStaticMarkup } from 'react-dom/server'
import type { ReactNode } from 'react'
import { beforeEach, describe, expect, it, vi } from 'vitest'

const state = vi.hoisted(() => ({
  mode: 'local' as string | undefined,
  authEnabled: false as boolean | undefined,
}))

vi.mock('@skyhook-io/k8s-ui', () => ({
  NamespacePicker: ({ limitedListHelp }: { limitedListHelp?: ReactNode }) => <div>{limitedListHelp ?? 'no-help'}</div>,
}))
vi.mock('../api/client', () => ({
  useNamespaceScope: () => ({ data: undefined, isLoading: false }),
  useSetActiveNamespace: () => ({ isPending: false, mutate: vi.fn() }),
  useCapabilities: () => ({ data: state.mode === undefined ? undefined : { deployment: { mode: state.mode } } }),
  useAuthMe: () => ({ data: state.authEnabled === undefined ? undefined : { authEnabled: state.authEnabled } }),
}))

import { NamespaceSwitcher } from './NamespaceSwitcher'

describe('NamespaceSwitcher namespace help', () => {
  beforeEach(() => {
    state.mode = 'local'
    state.authEnabled = false
  })

  it('links to the namespace docs in local Radar without auth', () => {
    const html = renderToStaticMarkup(<NamespaceSwitcher />)
    expect(html).toContain('How to add namespaces')
    expect(html).toContain('#namespaces-missing-from-the-picker')
  })

  // --namespaces and config.json belong to whoever runs the server; on a shared
  // install the viewer can't use them.
  it.each([
    ['in-cluster Radar', 'in-cluster', false],
    ['Radar Cloud', 'cloud', true],
    ['auth-enabled local Radar', 'local', true],
    ['capabilities still loading', undefined, false],
    ['auth state still loading', 'local', undefined],
  ] as const)('offers no help for %s', (_label, mode, authEnabled) => {
    state.mode = mode
    state.authEnabled = authEnabled
    expect(renderToStaticMarkup(<NamespaceSwitcher />)).toContain('no-help')
  })
})
