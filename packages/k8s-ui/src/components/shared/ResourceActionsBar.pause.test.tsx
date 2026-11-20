// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { renderToString } from 'react-dom/server'
import { describe, expect, it, vi } from 'vitest'
import { ResourceActionsBar } from './ResourceActionsBar'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

const noop = () => {}
const deployment = (spec: Record<string, unknown> = {}, metadata: Record<string, unknown> = {}) => ({
  apiVersion: 'apps/v1',
  kind: 'Deployment',
  metadata: { name: 'web', namespace: 'prod', ...metadata },
  spec,
})

function render(data: unknown, kind = 'deployments', callbacks: Record<string, unknown> = { onPauseDeployment: noop, onResumeDeployment: noop }) {
  return renderToString(
    <ResourceActionsBar resource={{ kind, name: 'web', namespace: 'prod' }} data={data} {...callbacks} />,
  )
}

describe('Deployment pause button', () => {
  it('reads the action from spec.paused', () => {
    expect(render(deployment())).toContain('Pause rollout')
    expect(render(deployment())).not.toContain('Resume rollout')
    expect(render(deployment({ paused: false }))).toContain('Pause rollout')
    expect(render(deployment({ paused: true }))).toContain('Resume rollout')
    expect(render(deployment({ paused: true }))).not.toContain('Pause rollout')
  })

  it('is withheld when the host passes no callbacks', () => {
    const html = render(deployment(), 'deployments', {})
    expect(html).not.toContain('Pause rollout')
    expect(html).not.toContain('Resume rollout')
  })

  it.each([
    ['statefulsets', 'apps/v1'],
    ['daemonsets', 'apps/v1'],
    ['deployments', 'vendor.io/v1'],
    ['deployments', ''],
  ])('is withheld for %s in %s', (kind, apiVersion) => {
    expect(render({ ...deployment(), apiVersion }, kind)).not.toContain('Pause rollout')
  })

  it('is withheld while the Deployment is terminating', () => {
    expect(render(deployment({}, { deletionTimestamp: '2026-11-04T00:00:00Z' }))).not.toContain('Pause rollout')
  })

  it.each([
    [false, 'onPauseDeployment', 'onResumeDeployment'],
    [true, 'onResumeDeployment', 'onPauseDeployment'],
  ])('with spec.paused=%s a click calls %s only', (paused, called, notCalled) => {
    const callbacks = { onPauseDeployment: vi.fn(), onResumeDeployment: vi.fn() }
    const container = document.createElement('div')
    document.body.appendChild(container)
    const root = createRoot(container)
    act(() => {
      root.render(<ResourceActionsBar resource={{ kind: 'deployments', name: 'web', namespace: 'prod' }} data={deployment({ paused })} {...callbacks} />)
    })

    const button = Array.from(container.querySelectorAll('button')).find(b => /(Pause|Resume) rollout/.test(b.textContent ?? ''))
    act(() => button?.click())

    expect(callbacks[called as keyof typeof callbacks]).toHaveBeenCalledWith({ kind: 'deployments', namespace: 'prod', name: 'web' })
    expect(callbacks[notCalled as keyof typeof callbacks]).not.toHaveBeenCalled()
    act(() => root.unmount())
    container.remove()
  })

  it('is disabled while either request is running', () => {
    expect(render(deployment(), 'deployments', { onPauseDeployment: noop, onResumeDeployment: noop, isPausingDeployment: true })).toMatch(/<button[^>]*disabled[^>]*>(?:(?!<\/button>).)*Pause rollout/)
    expect(render(deployment({ paused: true }), 'deployments', { onPauseDeployment: noop, onResumeDeployment: noop, isResumingDeployment: true })).toMatch(/<button[^>]*disabled[^>]*>(?:(?!<\/button>).)*Resume rollout/)
  })
})
