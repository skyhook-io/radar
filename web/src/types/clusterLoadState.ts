export interface ClusterLoadState {
  loading: boolean
  message: string | null
  pendingKinds: string[]
}

export const idleClusterLoadState: ClusterLoadState = {
  loading: false,
  message: null,
  pendingKinds: [],
}

export function dashboardClusterLoadState(data?: {
  deferredLoading?: boolean
  partialData?: string[]
}): ClusterLoadState {
  const pendingKinds = data?.partialData ?? []
  if (pendingKinds.length > 0) {
    return {
      loading: true,
      message: `Still loading: ${pendingKinds.join(', ')}`,
      pendingKinds,
    }
  }
  if (data?.deferredLoading) {
    return {
      loading: true,
      message: 'Loading remaining resources…',
      pendingKinds: [],
    }
  }
  return idleClusterLoadState
}
