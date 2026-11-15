import { describe, expect, it } from 'vitest'

import { aboutVersionText } from './AboutDialog'

describe('aboutVersionText', () => {
  const info = { version: 'v1.20.4', platform: 'Windows · amd64' }
  it('names the version, channel and platform for bug reports', () => {
    expect(aboutVersionText(info, 'stable')).toBe('Radar 1.20.4\nWindows · amd64 · desktop app')
    expect(aboutVersionText(info, 'development')).toBe('Radar 1.20.4 (development build)\nWindows · amd64 · desktop app')
    expect(aboutVersionText(info)).toBe('Radar 1.20.4\nWindows · amd64 · desktop app')
  })
})
