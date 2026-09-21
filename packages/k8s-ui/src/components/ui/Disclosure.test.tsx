import { describe, expect, it } from 'vitest'
import { renderToString } from 'react-dom/server'
import { Disclosure } from './Disclosure'

describe('Disclosure', () => {
  it('wires the header to the panel and keeps closed content mounted', () => {
    const html = renderToString(<Disclosure summary="More">hidden-copy</Disclosure>)
    expect(html).toContain('aria-expanded="false"')
    const id = html.match(/aria-controls="([^"]+)"/)?.[1]
    expect(id).toBeTruthy()
    expect(html).toContain(`id="${id}"`)
    expect(html).toContain('hidden-copy')
    expect(html).toMatch(/inert=""|inert>/)
  })

  it('honours defaultOpen and a controlled open', () => {
    expect(renderToString(<Disclosure summary="s" defaultOpen>x</Disclosure>)).toContain('aria-expanded="true"')
    expect(renderToString(<Disclosure summary="s" open onOpenChange={() => {}}>x</Disclosure>)).toContain('aria-expanded="true"')
    expect(renderToString(<Disclosure summary="s" open={false} defaultOpen>x</Disclosure>)).toContain('aria-expanded="false"')
  })

  it('caret follows the header color unless pinned', () => {
    expect(renderToString(<Disclosure summary="s">x</Disclosure>)).not.toContain('text-theme-text-tertiary')
    expect(renderToString(<Disclosure summary="s" inheritColor={false}>x</Disclosure>)).toContain('text-theme-text-tertiary')
  })
})
