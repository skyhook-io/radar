import { expect, it } from 'vitest'
import { middlewareUsers } from './TraefikMiddlewareRenderer'

const answer = (state: string) => ({ installed: true, truncated: false, middlewares: [], coverage: { ingressroutes: { state } }, routes: [{ ref: { group: 'traefik.io', namespace: 'shop', name: 'ui' } } as any] })

it('maps rows to routes and reads partial coverage', () => {
  expect(middlewareUsers({ data: answer('full'), isError: false, support: 'supported' })).toEqual({ routes: [{ namespace: 'shop', name: 'ui' }], truncated: false, partial: false })
  expect(middlewareUsers({ data: answer('partial'), isError: false, support: 'supported' })?.partial).toBe(true)
})
it('keeps the drawer unchanged on an older Radar and says when the request failed', () => {
  expect(middlewareUsers({ isError: true, support: 'unsupported' })).toBeUndefined()
  expect(middlewareUsers({ isError: true, support: 'supported' })).toBeNull()
})
