import { describe, expect, it } from 'vitest'
import { renderToString } from 'react-dom/server'
import { PVCRenderer } from './PVCRenderer'

describe('PVCRenderer', () => {
  it('does not reassure indefinitely pending PVCs as normal', () => {
    const html = renderToString(
      <PVCRenderer
        data={{
          metadata: { name: 'data', namespace: 'demo' },
          spec: { storageClassName: 'standard' },
          status: { phase: 'Pending' },
        }}
      />,
    )

    expect(html).toContain('Pending')
    expect(html).toContain('not yet bound')
    expect(html).toContain('check the StorageClass')
    expect(html).not.toContain('This is normal')
    expect(html).not.toContain('expected indefinitely')
  })

  it('surfaces an unused claim as informational cleanup evidence', () => {
    const html = renderToString(
      <PVCRenderer
        data={{
          metadata: { name: 'archive', namespace: 'demo' },
          status: {
            phase: 'Bound',
            conditions: [
              {
                type: 'Unused',
                status: 'True',
                lastTransitionTime: '2026-08-28T12:00:00Z',
              },
            ],
          },
        }}
      />,
    )

    expect(html).toContain('Unused for about')
    expect(html).toContain('No non-terminal Pod currently references this claim')
    expect(html).toContain('Verify its retention policy and data ownership')
    expect(html).toContain('bg-gray-400/20')
    expect(html).not.toContain('failing')
    expect(html).not.toContain('bg-emerald-500/20')
  })

  it('does not show cleanup guidance while Unused is false or absent', () => {
    for (const conditions of [
      [{ type: 'Unused', status: 'False' }],
      undefined,
    ]) {
      const html = renderToString(
        <PVCRenderer
          data={{
            metadata: { name: 'active', namespace: 'demo' },
            status: { phase: 'Bound', conditions },
          }}
        />,
      )
      expect(html).not.toContain('No non-terminal Pod currently references this claim')
    }
  })
})
