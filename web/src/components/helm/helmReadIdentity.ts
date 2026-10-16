import { useAuthMe, useCapabilities } from '../../api/client'

export type HelmReadIdentity = 'user' | 'kubeconfig' | 'serviceAccount' | 'pending' | 'unknown'

export const HELM_SECRETS_DOCS_URL = 'https://radarhq.io/docs/configuration/in-cluster#opt-in-permissions'

// value is undefined both while loading and when the lookup settled without
// an answer (a failed request, or an older Radar that doesn't report the
// field); settled tells those apart.
interface Lookup<T> {
  value: T | undefined
  settled: boolean
}

// Helm stores releases as Secrets, so a 403 on the release list means the
// identity Radar read with can't list Secrets, and the fix depends on which
// identity that was. With auth on (always the case in cloud mode) Radar
// impersonates the user, so a chart setting would change nothing. With auth
// off it reads as itself: the kubeconfig identity for a local binary, or the
// ServiceAccount for an in-cluster pod, where the grant is a chart value.
// Both facts must be confirmed: the capabilities context reports 'local' while
// loading or after a failure, and a guess would show the wrong remedy.
export function helmReadIdentity(
  authEnabled: Lookup<boolean>,
  deploymentMode: Lookup<string>,
): HelmReadIdentity {
  if (deploymentMode.value === 'cloud' || authEnabled.value === true) return 'user'
  if (authEnabled.value === undefined || deploymentMode.value === undefined) {
    const answerMissing =
      (authEnabled.value === undefined && authEnabled.settled) ||
      (deploymentMode.value === undefined && deploymentMode.settled)
    return answerMissing ? 'unknown' : 'pending'
  }
  return deploymentMode.value === 'in-cluster' ? 'serviceAccount' : 'kubeconfig'
}

export function useHelmReadIdentity(): HelmReadIdentity {
  const authMe = useAuthMe()
  const capabilities = useCapabilities()
  return helmReadIdentity(
    { value: authMe.data?.authEnabled, settled: authMe.data !== undefined || authMe.isError },
    { value: capabilities.data?.deployment?.mode, settled: capabilities.data !== undefined || capabilities.isError },
  )
}
