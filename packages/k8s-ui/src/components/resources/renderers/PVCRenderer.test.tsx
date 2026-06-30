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
})
