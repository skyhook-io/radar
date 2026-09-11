import { describe, expect, it } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'
import { curateError, detectLanguage } from './PodFilePreview'

// Every backend error code becomes a curated screen. What matters here is
// that each code carries the right action affordances (Download vs Retry)
// and never leaks a raw stderr line as the primary description.

describe('curateError — action affordances per code', () => {
  it('offers Download for file_too_large and names the actual size', () => {
    const shape = curateError({
      ok: false,
      code: 'file_too_large',
      message: 'File is 12582912 bytes; preview is limited to 1048576 bytes.',
      size: 12 * 1024 * 1024,
    })
    expect(shape.actions).toEqual({ download: true, retry: false })
    expect(shape.description).toMatch(/MiB|MB/i)
    // The raw byte-count message must not be the primary description.
    expect(shape.description).not.toContain('12582912')
  })

  it('offers Download for binary_file and names the detected mime type', () => {
    const shape = curateError({
      ok: false,
      code: 'binary_file',
      message: 'server-side detail',
      mimeType: 'application/x-executable',
    })
    expect(shape.actions).toEqual({ download: true, retry: false })
    expect(shape.description).toContain('application/x-executable')
    expect(shape.description).not.toContain('server-side detail')
  })

  it('offers Retry for not_found — the file may have been rotated', () => {
    const shape = curateError({ ok: false, code: 'not_found', message: 'x' })
    expect(shape.actions.retry).toBe(true)
    expect(shape.actions.download).toBe(false)
  })

  it('offers neither Download nor Retry for permission_denied', () => {
    const shape = curateError({ ok: false, code: 'permission_denied', message: 'x' })
    expect(shape.actions).toEqual({ download: false, retry: false })
  })

  it('offers neither for no_shell — the container cannot be read this way', () => {
    const shape = curateError({ ok: false, code: 'no_shell', message: 'x' })
    expect(shape.actions).toEqual({ download: false, retry: false })
    expect(shape.description).toMatch(/distroless|scratch|no shell/i)
  })

  it('offers Retry for network_error and reveals details for diagnostics', () => {
    const shape = curateError({
      ok: false,
      code: 'network_error',
      message: 'Failed to fetch',
    })
    expect(shape.actions.retry).toBe(true)
    expect(shape.details).toBe('Failed to fetch')
  })

  it('falls unknown/read_failed to a generic curated line with both actions', () => {
    const shape = curateError({ ok: false, code: 'read_failed', message: 'stderr blob' })
    expect(shape.actions).toEqual({ download: true, retry: true })
    // The raw stderr belongs behind the details disclosure, not in the headline.
    expect(shape.description).not.toContain('stderr blob')
    expect(shape.details).toBe('stderr blob')
  })
})

// The primary description is what an operator reads first. It must never be
// the raw server message on any curated code — that was the maintainer's
// specific concern ("curated error message rather than only dumping raw error").
describe('curateError — no raw messages in primary text', () => {
  const rawStderr = 'sh: /etc/shadow: Permission denied — some raw text'
  it.each([
    'file_too_large',
    'binary_file',
    'not_a_regular_file',
    'not_found',
    'permission_denied',
    'no_shell',
    'container_missing_tools',
    'read_failed',
  ] as const)('%s does not leak the raw server message into the title or description', (code) => {
    const shape = curateError({ ok: false, code, message: rawStderr })
    expect(shape.title).not.toContain(rawStderr)
    expect(shape.description).not.toContain(rawStderr)
  })
})

describe('detectLanguage', () => {
  it.each([
    ['nginx.conf', 'ini'],
    ['deployment.yaml', 'yaml'],
    ['deployment.yml', 'yaml'],
    ['schema.json', 'yaml'],
    ['Dockerfile', 'dockerfile'],
    ['index.html', 'html'],
    ['README.md', 'markdown'],
    ['app.log', 'plaintext'],
    ['unknown', 'plaintext'],
    ['UPPERCASE.YAML', 'yaml'],
  ])('%s → %s', (name, want) => {
    expect(detectLanguage(name)).toBe(want)
  })
})

// The empty state and the loading state each render distinct content so an
// operator can tell them apart. These are the two states most likely to look
// like a bug if they collide.
describe('curated states are distinguishable', () => {
  it('an empty file description is not confusable with a failure', () => {
    const shape = curateError({ ok: false, code: 'read_failed', message: '' })
    // The empty-file state is rendered separately (PreviewEmptyState) and does
    // NOT come through curateError — a read_failed with an empty message must
    // therefore never say "empty" in its curated description.
    expect(shape.description.toLowerCase()).not.toContain('empty')
  })
})

// Static markup smoke: the module imports at all and the exported pure helpers
// stay tree-shakeable / server-renderable. If curateError were to reach for
// browser-only globals this would trip.
describe('curated shapes render on the server', () => {
  it('renders a title node without throwing in a server context', () => {
    const shape = curateError({ ok: false, code: 'binary_file', message: 'x', mimeType: 'application/octet-stream' })
    const html = renderToStaticMarkup(<span>{shape.title}</span>)
    expect(html).toContain('Binary')
  })
})
