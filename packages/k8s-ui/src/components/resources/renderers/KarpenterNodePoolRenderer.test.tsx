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

    expect(html).toContain('6 cores provisioned / 12 cores limit')
    expect(html).toContain('This is not live workload usage')
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

  it('renders disruption reasons and lifecycle safety settings', () => {
    const html = renderToString(
      <KarpenterNodePoolRenderer
        data={{
          spec: {
            template: {
              spec: {
                terminationGracePeriod: '24h',
                requirements: [{ key: 'node.kubernetes.io/instance-family', operator: 'In', values: ['m6i'], minValues: 2 }],
              },
            },
            disruption: { budgets: [{ nodes: '10%', reasons: ['Drifted', 'Underutilized'] }] },
          },
        }}
      />,
    )

    expect(html).toContain('Termination Grace Period')
    expect(html).toContain('24h')
    expect(html).toContain('Drifted')
    expect(html).toContain('Underutilized')
    expect(html).toContain('min <!-- -->2')
  })
})
