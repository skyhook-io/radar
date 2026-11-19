// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { expect, it, vi } from 'vitest'
import { VeleroLocations } from './VeleroLocations'
import { location, workspace } from './velero.fixtures'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

it('expands a folded schedule list and opens a schedule that was folded', () => {
  const host = document.createElement('div')
  document.body.append(host)
  const root = createRoot(host)
  const onInspect = vi.fn()
  const schedules = ['a', 'b', 'c', 'd'].map((name) => ({ name }))
  act(() => root.render(
    <VeleroLocations data={workspace({ locations: [location({ schedules })] })} installation={null} onInstallation={() => {}} onInspect={onInspect} inspected={null} />,
  ))
  const button = (label: string) => [...host.querySelectorAll('button')].find((b) => b.textContent === label)
  expect(button('d')).toBeUndefined()
  act(() => button('and 1 more')!.click())
  act(() => button('d')!.click())
  expect(onInspect).toHaveBeenCalledTimes(1)
  expect(onInspect.mock.calls[0][0]).toMatchObject({ kind: 'schedules', name: 'd', namespace: 'velero' })
  act(() => root.unmount())
  host.remove()
})
