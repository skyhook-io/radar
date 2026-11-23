import { expect, it } from 'vitest'
import { finishSwitch, switchFinished } from './contextSwitch'

it('remembers every switch whose outcome the tab presented, not just the last', () => {
  finishSwitch('switch-a')
  finishSwitch('switch-b')
  expect(switchFinished('switch-a')).toBe(true)
  expect(switchFinished('switch-b')).toBe(true)
  expect(switchFinished('switch-c')).toBe(false)
  expect(switchFinished('')).toBe(false)
})
