import { expect, it } from 'vitest'
import { parseInterval } from './SyncCountdown'

it('parses Go durations as Flux writes them', () => {
  expect(parseInterval('5m')).toBe(300)
  expect(parseInterval('1h30m')).toBe(5400)
  expect(parseInterval('30s')).toBe(30)
  expect(parseInterval('1.5m')).toBe(90)
  expect(parseInterval('1500ms')).toBe(1.5)
  expect(parseInterval('1m30.5s')).toBe(90.5)
})

it('refuses what is not a duration', () => {
  expect(parseInterval('')).toBeNull()
  expect(parseInterval('0s')).toBeNull()
  expect(parseInterval('10')).toBeNull()
  expect(parseInterval('5d')).toBeNull()
  expect(parseInterval('m5')).toBeNull()
})
