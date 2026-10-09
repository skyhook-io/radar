// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it, vi } from 'vitest'
import { PodRenderer } from './PodRenderer'

const data = {
  metadata: { name: 'worker', namespace: 'app' },
  spec: { containers: [], priority: 1000, priorityClassName: 'urgent', runtimeClassName: 'sandbox' },
  status: { phase: 'Pending' },
}

describe('PodRenderer class references', () => {
  it('links explicit class names with their API groups and cluster scope', async () => {
    Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })
    const container = document.createElement('div')
    const root = createRoot(container)
    const onNavigate = vi.fn()
    try {
      await act(async () => root.render(<PodRenderer data={data} onCopy={() => {}} copied={null} onNavigate={onNavigate} />))
      for (const [name, kind, group] of [
        ['urgent', 'priorityclasses', 'scheduling.k8s.io'],
        ['sandbox', 'runtimeclasses', 'node.k8s.io'],
      ]) {
        const button = [...container.querySelectorAll('button')].find(button => button.textContent === name)
        expect(button).toBeDefined()
        await act(async () => button!.click())
        expect(onNavigate).toHaveBeenLastCalledWith({ kind, group, namespace: '', name })
      }
    } finally {
      await act(async () => root.unmount())
      Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: false })
    }
  })

  it('does not infer a class from numeric priority alone', () => {
    const html = renderToStaticMarkup(<PodRenderer data={{ ...data, spec: { containers: [], priority: 1000 } }} onCopy={() => {}} copied={null} />)
    expect(html).not.toContain('Priority Class')
    expect(html).not.toContain('Runtime Class')
  })
})
