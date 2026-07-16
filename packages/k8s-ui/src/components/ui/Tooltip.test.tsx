import { describe, expect, it } from 'vitest'
import { renderToString } from 'react-dom/server'
import { Tooltip } from './Tooltip'

function render(preserveWrapperWhenDisabled = false) {
  return renderToString(
    <Tooltip content="Full value" disabled preserveWrapperWhenDisabled={preserveWrapperWhenDisabled}>
      <span>Visible value</span>
    </Tooltip>,
  )
}

describe('Tooltip', () => {
  it('omits its wrapper when disabled by default', () => {
    expect(render()).not.toContain('inline-flex max-w-full')
  })

  it('can preserve its wrapper while disabled for layout-sensitive children', () => {
    expect(render(true)).toContain('inline-flex max-w-full')
  })
})
