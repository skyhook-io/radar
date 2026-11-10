// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { expect, it, vi } from 'vitest'
import { RadarUpgradeContext, RadarUpgradeNote } from './RadarUpgradeNote'

vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)

it('hands the whole requirement to the host when the user asks to upgrade', async () => {
  const onRequestUpgrade = vi.fn()
  const requirement = { feature: 'Policy results', minimumVersion: 'v1.10.0', currentVersion: 'v1.7.2', latestVersion: 'v1.16.0' }
  const element = document.createElement('div')
  const root = createRoot(element)
  await act(async () => {
    root.render(
      <RadarUpgradeContext.Provider value={{ onRequestUpgrade }}>
        <RadarUpgradeNote requirement={requirement} />
      </RadarUpgradeContext.Provider>,
    )
  })
  await act(async () => { element.querySelector('button')!.click() })
  expect(onRequestUpgrade).toHaveBeenCalledWith(requirement)
  await act(async () => root.unmount())
})
