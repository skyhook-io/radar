import { useCallback, useEffect, useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useRadarFeature } from '../api/client'
import { getApiBase } from '../api/config'
import { fetchAgentSignin, type AgentSigninStatus } from '../api/diagnose'

export interface AgentSigninCheck {
  /** The agent's answer; undefined while checking, on failure, or when not checked. */
  status?: AgentSigninStatus
  rechecking: boolean
  recheck: () => void
}

// The server caches its answer for a minute; matching it keeps a reopened
// consent card from asking again.
const SIGNIN_STALE_MS = 60_000

/**
 * Asks whether a local agent CLI is signed in, for the consent step. Never
 * blocks: the card renders at once and the answer arrives within the server's
 * timeout. An error, an older Radar without the endpoint, or an "unknown"
 * answer all leave the consent step as it was without the check.
 */
export function useAgentSignin(agent: string, enabled: boolean): AgentSigninCheck {
  const { support } = useRadarFeature('agentSigninCheck')
  const active = enabled && !!agent && support !== 'unsupported'
  const queryClient = useQueryClient()
  const queryKey = ['agent-signin', getApiBase(), agent]
  const query = useQuery({
    queryKey,
    queryFn: ({ signal }) => fetchAgentSignin(agent, false, signal),
    enabled: active,
    staleTime: SIGNIN_STALE_MS,
    retry: false,
    refetchOnWindowFocus: false,
  })
  const [rechecking, setRechecking] = useState(false)
  const apiBase = getApiBase()
  const recheck = useCallback(() => {
    const key = ['agent-signin', apiBase, agent]
    setRechecking(true)
    fetchAgentSignin(agent, true)
      .then((status) => queryClient.setQueryData(key, status))
      // A failed re-check must not leave an old "signed out" in place.
      .catch(() => queryClient.removeQueries({ queryKey: key, exact: true }))
      .finally(() => setRechecking(false))
  }, [agent, apiBase, queryClient])

  const status = active && !query.isError ? query.data : undefined
  const signedOut = status?.state === 'signed-out'
  // Signing in happens in a terminal; coming back to the window is when to
  // look again, as the agent list does after an install.
  useEffect(() => {
    if (!signedOut) return
    const onFocus = () => recheck()
    window.addEventListener('focus', onFocus)
    return () => window.removeEventListener('focus', onFocus)
  }, [signedOut, recheck])

  return { status, rechecking, recheck }
}
