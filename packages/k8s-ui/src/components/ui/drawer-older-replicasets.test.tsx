// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { renderToString } from 'react-dom/server'
import { afterEach, describe, expect, it } from 'vitest'
import { RelatedResourcesSection } from './drawer-components'
import type { Relationships } from '../../types'

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })

const rs = (name: string) => ({ group: 'apps', kind: 'ReplicaSet', namespace: 'shop', name })
let container: HTMLDivElement | undefined
afterEach(() => {
  container?.remove()
  container = undefined
})

function mount(relationships: Relationships) {
  container = document.createElement('div')
  document.body.appendChild(container)
  const root = createRoot(container)
  act(() => root.render(<RelatedResourcesSection relationships={relationships} />))
  return container
}

describe('older ReplicaSets', () => {
  it('lists the ReplicaSets running Pods, and the old revisions only on request', () => {
    const el = mount({ children: [rs('web-7d4f')], olderRevisions: [rs('web-5c9a'), rs('web-6b1e')] })
    const toggle = Array.from(el.querySelectorAll('button')).find((b) => b.textContent?.includes('older ReplicaSets'))
    expect(toggle?.textContent).toBe('Show 2 older ReplicaSets')
    expect(toggle?.getAttribute('aria-expanded')).toBe('false')
    expect(el.textContent).toContain('web-7d4f')
    expect(el.textContent).not.toContain('web-5c9a')
    act(() => toggle?.click())
    expect(toggle?.getAttribute('aria-expanded')).toBe('true')
    expect(toggle?.textContent).toBe('Hide older ReplicaSets')
    expect(el.textContent).toContain('web-5c9a')
    expect(el.textContent).toContain('web-6b1e')
  })

  it('shows Children for old revisions alone, and nothing extra without them', () => {
    expect(renderToString(<RelatedResourcesSection relationships={{ olderRevisions: [rs('web-5c9a')] }} />)).toContain('Show 1 older ReplicaSet')
    expect(renderToString(<RelatedResourcesSection relationships={{ children: [rs('web-7d4f')] }} />)).not.toContain('older ReplicaSet')
  })
})
