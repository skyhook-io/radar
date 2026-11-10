// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { expect, it, vi } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'
import { CNPGPoolerScheduling } from './CNPGPoolerSummary'
Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })
it('leads with the cause, truncates a secondary Pod link, exposes its full name on hover and navigates once', () => {
  vi.useFakeTimers()
  const pod = 'orders-pooler-rw-6f9e349dec-abcdefghij'
  const navigate = vi.fn()
  const host = document.createElement('div'); document.body.append(host); const root = createRoot(host)
  act(() => root.render(<CNPGPoolerScheduling namespace="db" pods={[{ pod, state: 'unreachable', schedulingReason: '0/2 nodes are available: 2 Too many pods. preemption: no victims.' }]} onNavigate={navigate} />))
  expect(host.firstElementChild!.firstElementChild!.textContent).toBe('Cannot be scheduled: both nodes have reached their Pod limit.')
  const link = [...host.querySelectorAll('button')].find((b) => b.textContent === pod)!
  expect(link.parentElement!.className).toContain('[&_button]:truncate')
  expect(link.parentElement!.parentElement!.className).toContain('text-theme-text-secondary')
  act(() => { link.dispatchEvent(new MouseEvent('mouseover', { bubbles: true })); vi.advanceTimersByTime(350) })
  expect(document.querySelector('[role="tooltip"]')!.textContent).toBe(pod)
  act(() => link.click())
  expect(navigate).toHaveBeenCalledExactlyOnceWith({ kind: 'Pod', group: '', namespace: 'db', name: pod })
  act(() => root.unmount()); host.remove(); vi.useRealTimers()
})

it('groups a shared scheduling cause while retaining every Pod and its full scheduler message', () => {
  const first = '0/2 nodes are available: 2 Too many pods. preemption: no victims.'
  const second = '0/2 nodes are available: 2 Too many pods. preemption: no candidates.'
  const html = renderToStaticMarkup(<CNPGPoolerScheduling namespace="db" pods={[{ pod: 'a', state: 'unreachable', schedulingReason: first }, { pod: 'b', state: 'unreachable', schedulingReason: second }, { pod: 'c', state: 'unreachable', schedulingReason: 'insufficient cpu' }]} onNavigate={() => {}} />)
  expect(html.match(/Cannot be scheduled: both nodes have reached their Pod limit/g)).toHaveLength(1)
  expect(html).toContain('Cannot be scheduled: insufficient cpu')
  const host = document.createElement('div'); host.innerHTML = html
  expect([...host.querySelectorAll('button')].filter((b) => ['a', 'b', 'c'].includes(b.textContent!)).map((b) => b.textContent)).toEqual(['a', 'b', 'c'])
  expect(html).toContain(first)
  expect(html).toContain(second)
  expect(html.match(/Scheduler message/g)).toHaveLength(3)
})
