import { ArgoApplicationRenderer as BaseArgoApplicationRenderer } from '@skyhook-io/k8s-ui/components/resources/renderers/ArgoApplicationRenderer'
import { useArgoTerminate } from '../../../api/client'

interface ArgoApplicationRendererProps {
  data: any
}

export function ArgoApplicationRenderer({ data }: ArgoApplicationRendererProps) {
  const terminateMutation = useArgoTerminate()
  return (
    <BaseArgoApplicationRenderer
      data={data}
      onTerminate={(params) => terminateMutation.mutate(params)}
      isTerminating={terminateMutation.isPending}
    />
  )
}
