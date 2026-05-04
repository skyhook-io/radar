import { describe, expect, it } from 'vitest'
import { renderToString } from 'react-dom/server'
import { KarpenterNodePoolRenderer } from './KarpenterNodePoolRenderer'

describe('KarpenterNodePoolRenderer', () => {
  it('renders CPU quantities expressed as millicore strings', () => {
    const html = renderToString(
      <KarpenterNodePoolRenderer
        data={{
          spec: { limits: { cpu: '12000m' } },
          status: { resources: { cpu: '6000m' } },
        }}
      />,
    )

    expect(html).toContain('6 / 12')
  })

  it('renders non-string CPU quantities without throwing', () => {
    expect(() =>
      renderToString(
        <KarpenterNodePoolRenderer
          data={{
            spec: { limits: { cpu: 16 } },
            status: { resources: { cpu: 12 } },
          }}
        />,
      ),
    ).not.toThrow()
  })
})
