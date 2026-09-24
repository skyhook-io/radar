import { describe, expect, it } from 'vitest'
import app from '../../App.tsx?raw'

// @skyhook-io/radar-app ships App, and with it the poll. A host embedding
// Radar must never see it or have it call /api/poll.
describe('poll mounting', () => {
  it('is mounted only when Radar is not embedded', () => {
    const mounts = app.match(/.*<OssPoll\b.*/g) ?? []
    expect(mounts).toHaveLength(1)
    expect(mounts[0]).toContain('!navCustomization.embedded &&')
  })
})
