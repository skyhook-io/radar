// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it, vi } from 'vitest'
import { PodRenderer } from './PodRenderer'

describe('PodRenderer nominated Node', () => {
  it('links the nominated Node of an unbound Pod', async () => {
    Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })
    const container = document.createElement('div')
    const root = createRoot(container)
    const onNavigate = vi.fn()
    try {
      await act(async () => root.render(
        <PodRenderer
          data={{ metadata: { name: 'pending', namespace: 'app' }, spec: { containers: [] }, status: { phase: 'Pending', nominatedNodeName: 'candidate' } }}
          onCopy={() => {}}
          copied={null}
          onNavigate={onNavigate}
        />,
      ))
      expect(container.textContent).toContain('Nominated Node')
      const button = [...container.querySelectorAll('button')].find(button => button.textContent === 'candidate')
      expect(button).toBeDefined()
      await act(async () => button!.click())
      expect(onNavigate).toHaveBeenCalledWith({ kind: 'nodes', namespace: '', name: 'candidate', group: undefined })
    } finally {
      await act(async () => root.unmount())
      Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: false })
    }
  })

  it('hides a nomination on a terminal or terminating unbound Pod', () => {
    for (const data of [
      { metadata: { name: 'failed', namespace: 'app' }, spec: { containers: [] }, status: { phase: 'Failed', nominatedNodeName: 'stale' } },
      { metadata: { name: 'deleting', namespace: 'app', deletionTimestamp: '2026-10-07T00:00:00Z' }, spec: { containers: [] }, status: { phase: 'Pending', nominatedNodeName: 'stale' } },
    ]) {
      const html = renderToStaticMarkup(<PodRenderer data={data} onCopy={() => {}} copied={null} />)
      expect(html).not.toContain('Nominated Node')
      expect(html).not.toContain('stale')
    }
  })

  it('hides a nomination once the Pod is bound, even when it names another node', () => {
    const html = renderToStaticMarkup(
      <PodRenderer
        data={{ metadata: { name: 'running', namespace: 'app' }, spec: { nodeName: 'assigned', containers: [] }, status: { phase: 'Running', nominatedNodeName: 'stale' } }}
        onCopy={() => {}}
        copied={null}
      />,
    )
    expect(html).not.toContain('Nominated Node')
    expect(html).not.toContain('stale')
    expect(html).toContain('assigned')
  })
})
