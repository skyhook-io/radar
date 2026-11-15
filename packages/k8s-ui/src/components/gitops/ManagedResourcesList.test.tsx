import { describe, expect, it } from 'vitest'
import { renderToString } from 'react-dom/server'
import { ManagedResourcesList } from './ManagedResourcesList'
import type { ManagedResource } from '../../types/gitops'

function render(element: Parameters<typeof renderToString>[0]): string {
  return renderToString(element).replace(/<!-- -->/g, '')
}

function list(configMaps: number): ManagedResource[] {
  return [
    ...Array.from({ length: configMaps }, (_, i) => ({ group: '', kind: 'ConfigMap', namespace: 'shop', name: `cm-${i}` })),
    { group: 'apps', kind: 'Deployment', namespace: 'shop', name: 'checkout' },
    { group: '', kind: 'Service', namespace: 'shop', name: 'checkout' },
  ]
}

describe('ManagedResourcesList', () => {
  it('shows every entry of an uneven list under the cap', () => {
    const html = render(<ManagedResourcesList resources={list(8)} />)
    expect(html).toContain('ConfigMap</span><span class="text-theme-text-tertiary">(8)')
    expect(html).not.toContain('Show all')
  })

  it('offers Show all whenever a kind is cut', () => {
    const html = render(<ManagedResourcesList resources={list(6)} maxVisible={6} />)
    expect(html).toContain('ConfigMap</span><span class="text-theme-text-tertiary">(2 of 6)')
    expect(html).toContain('Show all 8 resources')
  })

  it('offers no Show all over the cap when no kind is cut', () => {
    const html = render(<ManagedResourcesList resources={list(1)} maxVisible={2} />)
    expect(html).not.toContain('Show all')
  })
})
