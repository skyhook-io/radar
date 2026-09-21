import { describe, expect, it } from 'vitest'
import { phaseOnlyHealth } from './TopologyGraph'

// Only reached when the server sent no per-pod status — an older Radar behind a
// newer embedding host. The point of the fallback is that it must not repeat the
// bug it stands in for.
describe('phaseOnlyHealth', () => {
  it('does not call a Running pod healthy', () => {
    // A crash-looping pod sits at Phase=Running with its container restarting,
    // so Running is the one phase that hides a fault.
    expect(phaseOnlyHealth('Running')).toBe('unknown')
  })

  it('reads the phases that are honest on their own', () => {
    expect(phaseOnlyHealth('Failed')).toBe('unhealthy')
    expect(phaseOnlyHealth('Pending')).toBe('degraded')
    expect(phaseOnlyHealth('Succeeded')).toBe('neutral')
  })

  it('says nothing for a phase it does not recognise', () => {
    expect(phaseOnlyHealth(undefined)).toBe('unknown')
    expect(phaseOnlyHealth('SomeFuturePhase')).toBe('unknown')
  })
})
