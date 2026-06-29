import { useCallback, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { CompareResourcePicker, refToParam, type CompareResourceRef } from '@skyhook-io/k8s-ui'
import { useCompareCandidates } from './useCompareCandidates'
import { useNavCustomization } from '../../context/NavCustomization'

interface UseCompareLauncherArgs {
  /** API plural kind (e.g. "deployments") — must match the route segment used by `/api/resources/{kind}`. */
  kind: string
  namespace: string
  name: string
  /** API group for the resource — required for CRDs that collide with core kinds. */
  group?: string
}

interface CompareLauncher {
  /** Wire this to ResourceActionsBar's `onCompareTo` prop. */
  onCompareTo: () => void
  /**
   * Wire this to ResourceActionsBar's `onCompareAcrossClusters` prop. Undefined
   * when the host (NavCustomization.crossClusterCompareHref) hasn't opted in
   * — keeps the standalone Radar experience identical.
   */
  onCompareAcrossClusters?: () => void
  /** Render this anywhere in the same tree to surface the picker dialog. */
  picker: React.ReactNode
}

export function useCompareLauncher({ kind, namespace, name, group }: UseCompareLauncherArgs): CompareLauncher {
  const navigate = useNavigate()
  const [open, setOpen] = useState(false)
  const kindLower = kind.toLowerCase()
  const { candidates, isPending, error } = useCompareCandidates(kindLower, group, open)
  const { crossClusterCompareHref, onHostNavigate } = useNavCustomization()

  const onCompareTo = useCallback(() => setOpen(true), [])

  const onCompareAcrossClusters = useCallback(() => {
    if (!crossClusterCompareHref) return
    const href = crossClusterCompareHref({ kind: kindLower, namespace, name, group })
    // Smooth same-document hand-off when the host supports it (Radar Cloud's
    // cross-tree swap), else a hard nav — same contract as App.tsx's goHost.
    if (onHostNavigate) onHostNavigate(href)
    else window.location.assign(href)
  }, [crossClusterCompareHref, onHostNavigate, kindLower, namespace, name, group])

  const handlePick = useCallback(
    (picked: CompareResourceRef) => {
      setOpen(false)
      const params = new URLSearchParams()
      params.set('kind', kindLower)
      if (group) params.set('apiGroup', group)
      params.set('a', refToParam({ namespace, name }))
      params.set('b', refToParam({ namespace: picked.namespace, name: picked.name }))
      navigate({ pathname: '/compare', search: params.toString() })
    },
    [navigate, kindLower, group, namespace, name],
  )

  const source: CompareResourceRef = { kind: kindLower, namespace, name, group }

  const picker = (
    <CompareResourcePicker
      open={open}
      onClose={() => setOpen(false)}
      source={source}
      candidates={candidates}
      loading={open && isPending}
      error={open ? error : null}
      onPick={handlePick}
    />
  )

  return {
    onCompareTo,
    onCompareAcrossClusters: crossClusterCompareHref ? onCompareAcrossClusters : undefined,
    picker,
  }
}
