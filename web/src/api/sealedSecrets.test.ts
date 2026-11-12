import { expect, it } from 'vitest'
import { sealedSecretsRefetchInterval } from './sealedSecrets'

it('polls while the sealing keys are loading and stops once they are read', () => {
  expect(sealedSecretsRefetchInterval({ installed: true, controllerState: 'ok', keysState: 'loading' })).toBe(3000)
  expect(sealedSecretsRefetchInterval({ installed: true, controllerState: 'ok', keysState: 'ok' })).toBe(false)
  expect(sealedSecretsRefetchInterval(undefined)).toBe(false)
})
