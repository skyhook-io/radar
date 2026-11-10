// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { expect, it, vi } from 'vitest'
import { UncheckedSourceHelp } from './UncheckedSourceHelp'

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })

it('keeps the coverage warning visible when help closes and prevents duplicate retries', async () => {
  const host = document.createElement('div')
  document.body.append(host)
  const root = createRoot(host)
  const retry = vi.fn()
  const render = (refreshing: boolean) => (
    <div>
      <p>Helm releases couldn't be checked.</p>
      <UncheckedSourceHelp onRetry={retry} isRefreshing={refreshing} />
    </div>
  )
  try {
    await act(async () => root.render(render(false)))
    const disclosure = host.querySelector<HTMLButtonElement>('button[aria-expanded]')
    expect(disclosure).not.toBeNull()
    expect(disclosure?.getAttribute('aria-expanded')).toBe('false')
    await act(async () => disclosure?.click())
    expect(disclosure?.getAttribute('aria-expanded')).toBe('true')
    const button = [...host.querySelectorAll('button')].find((b) => b.textContent === 'Check again')
    expect(button).toBeDefined()
    await act(async () => button?.click())
    expect(retry).toHaveBeenCalledTimes(1)
    await act(async () => root.render(render(true)))
    expect(button?.disabled).toBe(true)
    await act(async () => button?.click())
    expect(retry).toHaveBeenCalledTimes(1)
    await act(async () => disclosure?.click())
    expect(disclosure?.getAttribute('aria-expanded')).toBe('false')
    expect(host.querySelector('p')?.textContent).toBe("Helm releases couldn't be checked.")
  } finally {
    await act(async () => root.unmount())
    host.remove()
  }
})
