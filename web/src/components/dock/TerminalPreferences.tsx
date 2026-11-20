import { useCallback, useEffect, useLayoutEffect, useRef, useState, type ReactNode } from 'react'
import { TerminalPreferencesProvider } from '@skyhook-io/k8s-ui'
import { useAuthMe } from '../../api/client'

interface Identity {
  scope?: string
  checking: boolean
  failed?: boolean
}

export function TerminalPreferences({ children }: { children: ReactNode }) {
  const { refetch } = useAuthMe()
  const [identity, setIdentity] = useState<Identity>({ checking: true })
  const current = useRef(false)
  const isScopeCurrent = useCallback(() => current.current, [])

  useLayoutEffect(() => {
    current.current = !identity.checking && !identity.failed && identity.scope !== undefined
  }, [identity])

  useEffect(() => {
    let generation = 0
    let disposed = false
    const invalidate = () => {
      current.current = false
      generation++
      setIdentity(previous => ({ ...previous, checking: true, failed: false }))
    }
    const verify = async () => {
      invalidate()
      const request = generation
      while (!disposed && generation === request) {
        const startedAt = Date.now()
        const result = await refetch()
        if (disposed || generation !== request) return
        if (result.isError && result.fetchStatus === 'idle') {
          setIdentity(previous => ({ ...previous, checking: false, failed: true }))
          return
        }
        if (!result.isSuccess || result.fetchStatus !== 'idle' || result.dataUpdatedAt < startedAt) continue
        const auth = result.data
        const scope = auth.authEnabled === false
          ? 'standalone'
          : auth.username ? `account:${auth.username}` : undefined
        setIdentity({ scope, checking: false })
        return
      }
    }
    const onVisibility = () => {
      if (document.visibilityState === 'visible') void verify()
      else invalidate()
    }
    const onFocus = () => { void verify() }
    window.addEventListener('blur', invalidate)
    window.addEventListener('focus', onFocus)
    document.addEventListener('visibilitychange', onVisibility)
    void verify()
    return () => {
      disposed = true
      generation++
      current.current = false
      window.removeEventListener('blur', invalidate)
      window.removeEventListener('focus', onFocus)
      document.removeEventListener('visibilitychange', onVisibility)
    }
  }, [refetch])

  return <TerminalPreferencesProvider scope={identity.scope} scopePending={identity.checking || identity.failed} isScopeCurrent={isScopeCurrent} unavailableReason={identity.checking
    ? 'Waiting for account information'
    : identity.failed ? 'Could not load account information. Switch to another tab or window and back to retry.' : undefined}>
    {children}
  </TerminalPreferencesProvider>
}
