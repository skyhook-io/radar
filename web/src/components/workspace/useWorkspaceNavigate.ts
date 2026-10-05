import { useCallback } from 'react'
import {
  useLocation,
  useNavigate,
  type NavigateOptions,
  type To,
} from 'react-router-dom'
import { withCrossViewParams } from '../../utils/navigation'

export function useWorkspaceNavigate() {
  const navigate = useNavigate()
  const { search } = useLocation()
  return useCallback(
    (to: To | number, options?: NavigateOptions) => {
      if (typeof to === 'number') return navigate(to)
      return navigate(
        typeof to === 'string' ? withCrossViewParams(to, search) : to,
        options,
      )
    },
    [navigate, search],
  )
}
