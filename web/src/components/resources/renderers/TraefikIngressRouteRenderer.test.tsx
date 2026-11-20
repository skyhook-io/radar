import { expect, it } from 'vitest'
import { routeFactsProps } from './TraefikIngressRouteRenderer'

const answer = { installed: true, coverage: {}, truncated: false, routes: [{ rule: 0 } as any], middlewares: [{ namespace: 'shop', name: 'redirect', usedBy: 12 }] }

it('passes rule facts and middleware use to the drawer', () => {
  expect(routeFactsProps({ data: answer, isError: false, support: 'supported' })).toEqual({ routeFacts: answer.routes, middlewareUse: { 'shop/redirect': 12 } })
})
it('keeps the drawer unchanged on an older Radar and while loading', () => {
  expect(routeFactsProps({ isError: true, support: 'unsupported' }).routeFacts).toBeUndefined()
  expect(routeFactsProps({ isError: false, support: 'supported' }).routeFacts).toBeUndefined()
})
it('says the facts could not be loaded when the request fails', () => {
  expect(routeFactsProps({ isError: true, support: 'supported' }).routeFacts).toBeNull()
})
