import { useCallback, useEffect, useRef, useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useRadarFeature } from '../api/client'
import { getApiBase } from '../api/config'
import { fetchAgentSignin, type AgentSigninStatus, type ExecutionProfile } from '../api/diagnose'

export interface AgentSigninCheck {
  /** The agent's answer; undefined while checking, on failure, or when not checked. */
  status?: AgentSigninStatus
  /** A check is running and has not answered yet. */
  pending: boolean
  rechecking: boolean
  recheck: () => void
  /** Resolves with the answer once the running check settles (undefined on failure or when not checked). */
  settle: () => Promise<AgentSigninStatus | undefined>
}

// The server caches its answer for a minute; matching it keeps a reopened
// consent card from asking again.
const SIGNIN_STALE_MS = 60_000
// Longest an early Approve waits for the answer: the server's 2s command
// timeout plus the round trip.
const SETTLE_MAX_MS = 2_500

/**
 * Asks whether a local agent CLI is signed in for runs with this profile, for
 * the consent step. Never blocks: the card renders at once and the answer
 * arrives within the server's timeout. An error, an older Radar without the
 * endpoint, or an "unknown" answer all leave the consent step as it was
 * without the check.
 */
export function useAgentSignin(agent: string, profile: ExecutionProfile, enabled: boolean): AgentSigninCheck {
  const { support } = useRadarFeature('agentSigninCheck')
  const active = enabled && !!agent && support !== 'unsupported'
  const queryClient = useQueryClient()
  const apiBase = getApiBase()
  const query = useQuery({
    queryKey: ['agent-signin', apiBase, agent, profile],
    queryFn: ({ signal }) => fetchAgentSignin(agent, profile, false, signal),
    enabled: active,
    staleTime: SIGNIN_STALE_MS,
    retry: false,
    refetchOnWindowFocus: false,
  })

  // A click and a window-focus re-check can overlap; only the newest may
  // apply its answer or clear the spinner.
  const seq = useRef(0)
  const [rechecking, setRechecking] = useState(false)
  const recheck = useCallback(() => {
    const key = ['agent-signin', apiBase, agent, profile]
    const mine = ++seq.current
    setRechecking(true)
    void queryClient.cancelQueries({ queryKey: key, exact: true })
    fetchAgentSignin(agent, profile, true)
      .then((status) => {
        if (mine === seq.current) queryClient.setQueryData(key, status)
      })
      .catch(() => {
        // Unknown, not removed: a removed query refetches and the server's
        // cache would hand back the stale "signed out".
        if (mine === seq.current) queryClient.setQueryData(key, { agent, state: 'unknown' } satisfies AgentSigninStatus)
      })
      .finally(() => {
        if (mine === seq.current) setRechecking(false)
      })
  }, [agent, profile, apiBase, queryClient])

  const settle = useCallback(async () => {
    if (!active) return undefined
    const answer = queryClient
      .fetchQuery({
        queryKey: ['agent-signin', apiBase, agent, profile],
        queryFn: ({ signal }) => fetchAgentSignin(agent, profile, false, signal),
        staleTime: SIGNIN_STALE_MS,
        retry: false,
      })
      .catch(() => undefined)
    // The server answers within its own timeout, but a request can still sit
    // in the browser's queue behind other connections; Approve must not.
    let timer: ReturnType<typeof setTimeout> | undefined
    const giveUp = new Promise<undefined>((resolve) => {
      timer = setTimeout(() => resolve(undefined), SETTLE_MAX_MS)
    })
    try {
      return await Promise.race([answer, giveUp])
    } finally {
      clearTimeout(timer)
    }
  }, [active, agent, profile, apiBase, queryClient])

  const status = active && !query.isError ? query.data : undefined
  const pending = active && query.isFetching && !query.data
  const signedOut = status?.state === 'signed-out'
  // Signing in happens in a terminal; coming back to the window is when to
  // look again, as the agent list does after an install.
  useEffect(() => {
    if (!signedOut) return
    const onFocus = () => recheck()
    window.addEventListener('focus', onFocus)
    return () => window.removeEventListener('focus', onFocus)
  }, [signedOut, recheck])

  return { status, pending, rechecking, recheck, settle }
}
