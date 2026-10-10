import { ArgoApplicationRenderer as BaseArgoApplicationRenderer } from '@skyhook-io/k8s-ui/components/resources/renderers/ArgoApplicationRenderer'
import { useArgoTerminate, useGitOpsActionCapabilities } from '../../../api/client'

interface ArgoApplicationRendererProps {
  data: any
}

export function ArgoApplicationRenderer({ data }: ArgoApplicationRendererProps) {
  const { disabledReasons } = useGitOpsActionCapabilities('applications', data.metadata.namespace, data.metadata.name)
  const terminateMutation = useArgoTerminate()
  return (
    <BaseArgoApplicationRenderer
      data={data}
      onTerminate={(params) => terminateMutation.mutate(params)}
      terminateDisabledReason={disabledReasons.terminate}
      isTerminating={terminateMutation.isPending}
    />
  )
}
