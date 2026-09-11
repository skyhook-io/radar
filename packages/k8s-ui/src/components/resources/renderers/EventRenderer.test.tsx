import { renderToString } from 'react-dom/server'
import type { ReactElement, ReactNode } from 'react'
import { afterEach, describe, expect, it, vi } from 'vitest'

import { EventRenderer } from './EventRenderer'
import { initNavigationMap, resetNavigationMap } from '../../../utils/navigation'

afterEach(resetNavigationMap)

function findButton(node: ReactNode): ReactElement<{ onClick: () => void }> | undefined {
  if (!node || typeof node !== 'object' || !('props' in node)) return undefined
  const element = node as ReactElement<{ children?: ReactNode; onClick?: () => void }>
  if (element.type === 'button') return element as ReactElement<{ onClick: () => void }>
  const children = element.props.children
  for (const child of Array.isArray(children) ? children : [children]) {
    const button = findButton(child)
    if (button) return button
  }
  return undefined
}

describe('EventRenderer', () => {
  it.each([
    ['Warning', 'status-degraded'],
    ['Normal', 'status-neutral'],
  ])('uses the theme-aware status tone for %s events', (type, statusClass) => {
    const html = renderToString(<EventRenderer data={{
      type,
      reason: 'FailedScheduling',
      message: 'No nodes are available',
    }} />)

    expect(html).toContain(statusClass)
    expect(html).toContain('FailedScheduling')
    expect(html).toContain('No nodes are available')
    expect(html).not.toMatch(/text-(?:amber|blue)-(?:200|300|400)/)
    expect(html).not.toContain('opacity-')
  })

  it('navigates to the group-qualified native PodGroup resource', () => {
    initNavigationMap([{
      group: 'scheduling.k8s.io',
      version: 'v1beta1',
      kind: 'PodGroup',
      name: 'podgroups',
      namespaced: true,
      isCrd: false,
      verbs: ['get', 'list'],
    }])
    const onNavigate = vi.fn()
    const tree = EventRenderer({
      data: {
        involvedObject: {
          apiVersion: 'scheduling.k8s.io/v1beta1',
          kind: 'PodGroup',
          namespace: 'default',
          name: 'batch',
        },
      },
      onNavigate,
    })

    findButton(tree)?.props.onClick()

    expect(onNavigate).toHaveBeenCalledWith({
      kind: 'podgroups',
      namespace: 'default',
      name: 'batch',
      group: 'scheduling.k8s.io',
    })
  })
})
