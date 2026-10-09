import { useCallback } from 'react'
import { useLocation, useNavigate, type NavigateOptions, type To } from 'react-router-dom'
import { withCrossViewParams } from '../../utils/navigation'

/**
 * navigate() for the CloudNativePG screens: a string path keeps the current
 * cross-view params (the namespace filter, an open investigation), as the
 * app's own navigation does. Without them the app reads the missing
 * ?namespaces= as "All namespaces" and saves that as the user's choice.
 */
export function useCNPGNavigate() {
  const navigate = useNavigate()
  const { search } = useLocation()
  return useCallback(
    (to: To | number, options?: NavigateOptions) => {
      if (typeof to === 'number') return navigate(to)
      return navigate(typeof to === 'string' ? withCrossViewParams(to, search) : to, options)
    },
    [navigate, search],
  )
}
