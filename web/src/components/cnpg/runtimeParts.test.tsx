import { renderToStaticMarkup } from 'react-dom/server'
import { expect, it } from 'vitest'
import { Denied, ProxyDenied } from './runtimeParts'

it('uses the same full-width padding and grant layout for proxy and exec denials', () => {
  const proxy = renderToStaticMarkup(<ProxyDenied what="Sessions" grant="get pods/proxy in namespace db" />)
  const exec = renderToStaticMarkup(<Denied title="No access to blocking detail" grant="create pods/exec in namespace db">Blocking is unavailable.</Denied>)
  const shell = (html: string) => html.match(/^<div class="([^"]+)"/)?.[1]
  expect(shell(proxy)).toBe(shell(exec))
  expect(shell(proxy)).toContain('w-full')
  expect(shell(proxy)).toContain('p-4')
  for (const html of [proxy, exec]) {
    expect(html).not.toContain('max-w-2xl')
    expect(html).toContain('requires:')
    expect(html).toContain('break-words')
  }
})
