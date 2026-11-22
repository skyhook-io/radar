import { expect, it } from 'vitest'
import { RadarFeatureUnsupportedError } from '../../../api/radarFeatures'
import { routeFactsProps } from './TraefikIngressRouteRenderer'

const answer = { installed: true, coverage: {}, truncated: false, routes: [{ rule: 0 } as any], middlewares: [{ namespace: 'shop', name: 'redirect', usedBy: 12 }] }

it('passes rule facts and middleware use to the drawer', () => {
  expect(routeFactsProps({ data: answer, isError: false, support: 'supported' })).toMatchObject({ routeFacts: answer.routes, middlewareUse: { 'shop/redirect': 12 } })
})
it('keeps the drawer unchanged on an older Radar and while loading', () => {
  expect(routeFactsProps({ isError: true, support: 'unsupported' }).routeFacts).toBeUndefined()
  expect(routeFactsProps({ isError: false, support: 'supported' }).routeFacts).toBeUndefined()
})
it('says the facts could not be loaded when the request fails', () => {
  expect(routeFactsProps({ isError: true, support: 'supported' }).routeFacts).toBeNull()
})
it('hides the section when a Radar of unknown version turns out not to have the route', () => {
  expect(routeFactsProps({ isError: true, error: new RadarFeatureUnsupportedError('traefikRoutes', {}), support: 'unknown' }).routeFacts).toBeUndefined()
})
it('marks middleware counts partial unless the routes were read in full', () => {
  expect(routeFactsProps({ data: { ...answer, coverage: { ingressroutes: { state: 'full' } } } as any, isError: false, support: 'supported' }).middlewareUsePartial).toBe(false)
  expect(routeFactsProps({ data: { ...answer, coverage: { ingressroutes: { state: 'partial' } } } as any, isError: false, support: 'supported' }).middlewareUsePartial).toBe(true)
})
