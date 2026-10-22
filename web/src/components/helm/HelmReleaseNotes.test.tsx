// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it, vi } from 'vitest'
import { HelmReleaseNotes } from './HelmReleaseNotes'

describe('Helm release notes', () => {
  it('preserves terminal artwork, blank lines, indentation and literal Markdown', () => {
    const notes = '   ███   ███\n  ░██   ░██\n\n# Installed\n\n- **Keep backups**\n\t[docs](https://example.com)\n'
    const container = document.createElement('div')
    container.innerHTML = renderToStaticMarkup(<HelmReleaseNotes notes={notes} copied={false} onCopy={() => {}} />)

    expect(container.querySelector('pre')?.textContent).toBe(notes)
    expect(container.querySelector('.markdown-content, h1, a, ul, strong')).toBeNull()
  })

  it('renders HTML as text', () => {
    const notes = '<script>alert(1)</script>\n<img src=x onerror=alert(1)>'
    const container = document.createElement('div')
    container.innerHTML = renderToStaticMarkup(<HelmReleaseNotes notes={notes} copied={false} onCopy={() => {}} />)

    expect(container.querySelector('pre')?.textContent).toBe(notes)
    expect(container.querySelector('script, img')).toBeNull()
  })

  it('displays and copies the full text without terminal escape sequences', async () => {
    const notes = '\u001b[34m   ███\n  ░██\u001b[0m\n\n# Ready\n'
    const clean = '   ███\n  ░██\n\n# Ready\n'
    const container = document.createElement('div')
    const root = createRoot(container)
    const copy = vi.fn()
    try {
      await act(async () => root.render(<HelmReleaseNotes notes={notes} copied={false} onCopy={copy} />))
      expect(container.querySelector('pre')?.textContent).toBe(clean)
      await act(async () => container.querySelector('button')?.click())
      expect(copy).toHaveBeenCalledExactlyOnceWith(clean)
    } finally {
      await act(async () => root.unmount())
    }
  })
})
