import { ArgoResourceDiff } from '@skyhook-io/k8s-ui'
import type { GitOpsDrift, GitOpsInsightRef } from '@skyhook-io/k8s-ui'
import { useArgoResourceDiff } from '../../api/client'

interface ArgoResourceDiffLoaderProps {
  appNamespace: string
  appName: string
  resourceRef: GitOpsInsightRef
  fallbackDrift?: GitOpsDrift
}

// Host wrapper: fetches the Argo CD resource diff and hands the data to the
// pure k8s-ui presentation component. Mirrors the data-in-web / render-in-
// k8s-ui split the resource renderers use for their host-wired data.
export function ArgoResourceDiffLoader({ appNamespace, appName, resourceRef, fallbackDrift }: ArgoResourceDiffLoaderProps) {
  const { data, isLoading, error } = useArgoResourceDiff(appNamespace, appName, resourceRef)
  return (
    <ArgoResourceDiff
      diff={data ?? null}
      loading={isLoading}
      error={(error as Error | null)?.message ?? null}
      fallbackDrift={fallbackDrift}
    />
  )
}
