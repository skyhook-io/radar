import { describe, expect, it } from 'vitest'
import { renderToString } from 'react-dom/server'
import { Collapse, CollapseChevron, disclosurePanelId } from './Collapse'
import {
  DURATION_DISCLOSURE,
  TRANSITION_CHEVRON,
  TRANSITION_DISCLOSURE,
} from '../../utils/animation'

// The primitive is the app-wide standard for expand/collapse; these pin the
// contract other components rely on rather than the visuals.
describe('Collapse', () => {
  it('animates grid-template-rows with the shared disclosure timing', () => {
    const html = renderToString(<Collapse open={false}>x</Collapse>)
    expect(html).toContain('grid-template-rows:0fr')
    expect(html).toContain(`duration-${DURATION_DISCLOSURE}`)
    expect(TRANSITION_DISCLOSURE).toContain(`duration-${DURATION_DISCLOSURE}`)
    expect(TRANSITION_DISCLOSURE).toContain('motion-reduce:transition-none')
  })

  it('keeps closed content mounted but inert, and clipped', () => {
    const html = renderToString(<Collapse open={false}>hidden-content</Collapse>)
    expect(html).toContain('hidden-content')
    expect(html).toMatch(/inert=""|inert>/)
    expect(html).toContain('overflow-hidden')
    const opened = renderToString(<Collapse open>shown</Collapse>)
    expect(opened).toContain('grid-template-rows:1fr')
    expect(opened).not.toMatch(/inert=""|inert>/)
  })

  it('mountLazily renders nothing until first open', () => {
    expect(renderToString(<Collapse open={false} mountLazily>lazy</Collapse>)).not.toContain('lazy')
    expect(renderToString(<Collapse open mountLazily>lazy</Collapse>)).toContain('lazy')
  })

  it('unmountOnExit renders nothing when initially closed', () => {
    // Initial-closed mount never had a transition to wait for.
    expect(renderToString(<Collapse open={false} unmountOnExit>gone</Collapse>)).not.toContain('gone')
    expect(renderToString(<Collapse open unmountOnExit>here</Collapse>)).toContain('here')
  })

  it('exposes the panel id for aria-controls', () => {
    expect(renderToString(<Collapse open id="panel-1">x</Collapse>)).toContain('id="panel-1"')
  })

  it('disclosurePanelId never yields whitespace (aria-controls is a space-separated list)', () => {
    expect(disclosurePanelId(':r1:', 'Access Control')).toBe(':r1:-Access%20Control')
    expect(disclosurePanelId(':r1:', 7)).toBe(':r1:-7')
    // Injective: keys that only differ by the separator stay distinct.
    expect(disclosurePanelId('p', 'a b')).not.toBe(disclosurePanelId('p', 'a_b'))
  })
})

describe('CollapseChevron', () => {
  it('runs the same clock as the panel', () => {
    expect(TRANSITION_CHEVRON).toContain(`duration-${DURATION_DISCLOSURE}`)
    const html = renderToString(<CollapseChevron open />)
    expect(html).toContain('rotate-90')
    expect(html).toContain(`duration-${DURATION_DISCLOSURE}`)
  })
})
