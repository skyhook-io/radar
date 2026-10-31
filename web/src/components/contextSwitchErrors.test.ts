import { describe, expect, it } from 'vitest'
import { contextSwitchErrorText } from './contextSwitchErrors'

describe('contextSwitchErrorText', () => {
  it('names the cluster and the likely fix for network failures', () => {
    expect(contextSwitchErrorText('network', 'staging-eu')).toBe("Can't reach staging-eu. Check your VPN or network.")
  })

  it('treats rejected and missing credentials alike', () => {
    expect(contextSwitchErrorText('auth-rejected', 'prod')).toBe(contextSwitchErrorText('auth', 'prod'))
  })

  it('falls back to a generic sentence for types it does not know', () => {
    expect(contextSwitchErrorText('something-new', 'prod')).toBe("Couldn't connect to prod.")
  })
})
