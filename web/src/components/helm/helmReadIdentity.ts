import { useAuthMe, useCapabilities } from '../../api/client'

export type HelmReadIdentity = 'user' | 'cloudViewer' | 'cloudUser' | 'kubeconfig' | 'serviceAccount' | 'pending' | 'unknown'

// value is undefined both while loading and when the lookup settled without
// an answer (a failed request, or an older Radar that doesn't report the
// field); settled tells those apart.
interface Lookup<T> {
  value: T | undefined
  settled: boolean
}

// Helm stores releases as Secrets, so a 403 on the release list means the
// identity Radar read with can't list Secrets, and the fix depends on which
// identity that was. With auth on Radar impersonates the user, so a chart
// setting would change nothing; cloud mode always runs with auth, and there the
// user's access comes from their Radar Cloud role. With auth
// off it reads as itself: the kubeconfig identity for a local binary, or the
// ServiceAccount for an in-cluster pod, where the grant is a chart value.
// Both facts must be confirmed: the capabilities context reports 'local' while
// loading or after a failure, and a guess would show the wrong remedy.
export function helmReadIdentity(
  authEnabled: Lookup<boolean>,
  deploymentMode: Lookup<string>,
  cloudRole?: string,
): HelmReadIdentity {
  // An older agent may not report its mode, but a Cloud role means Cloud.
  if (deploymentMode.value === 'cloud' || cloudRole) {
    return cloudRole === 'viewer' ? 'cloudViewer' : 'cloudUser'
  }
  // Auth status alone can't pick the copy: cloud users need different advice.
  if (deploymentMode.value === undefined && !deploymentMode.settled) return 'pending'
  if (authEnabled.value === true) return 'user'
  if (authEnabled.value === undefined) return authEnabled.settled ? 'unknown' : 'pending'
  if (deploymentMode.value === undefined) return 'unknown'
  return deploymentMode.value === 'in-cluster' ? 'serviceAccount' : 'kubeconfig'
}

export function useHelmReadIdentity(): HelmReadIdentity {
  const authMe = useAuthMe()
  const capabilities = useCapabilities()
  return helmReadIdentity(
    { value: authMe.data?.authEnabled, settled: authMe.data !== undefined || authMe.isError },
    { value: capabilities.data?.deployment?.mode, settled: capabilities.data !== undefined || capabilities.isError },
    authMe.data?.cloudRole,
  )
}
