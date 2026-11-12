import { expect, it } from 'vitest'
import { sealedSecretsQueryKey, sealedSecretsRefetchInterval } from './sealedSecrets'

it('polls while the sealing keys are loading and stops once they are read', () => {
  expect(sealedSecretsRefetchInterval({ installed: true, controllerState: 'ok', keysState: 'loading' })).toBe(3000)
  expect(sealedSecretsRefetchInterval({ installed: true, controllerState: 'ok', keysState: 'ok' })).toBe(false)
  expect(sealedSecretsRefetchInterval(undefined)).toBe(false)
})

it('polls while the controller lookup is loading too', () => {
  expect(sealedSecretsRefetchInterval({ installed: true, controllerState: 'loading', keysState: 'unknown' })).toBe(3000)
})

it('asks again once support is confirmed, so an early failure does not stick', () => {
  expect(sealedSecretsQueryKey('unknown')).not.toEqual(sealedSecretsQueryKey('supported'))
})
