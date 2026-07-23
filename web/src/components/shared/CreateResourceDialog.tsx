import { type ComponentProps } from 'react'
import { CreateResourceDialog as BaseCreateResourceDialog } from '@skyhook-io/k8s-ui'
import { fetchYamlSchemas, useApplyResource, usePreviewResources } from '../../api/client'

type BaseProps = ComponentProps<typeof BaseCreateResourceDialog>

export function CreateResourceDialog(props: Omit<BaseProps, 'onApply' | 'isApplying' | 'onPreview' | 'isPreviewing' | 'previewError' | 'schemaLoader'>) {
  const applyResource = useApplyResource()
  const previewResources = usePreviewResources()

  return (
    <BaseCreateResourceDialog
      {...props}
      onApply={(params) => applyResource.mutateAsync(params)}
      isApplying={applyResource.isPending}
      onPreview={(params) => previewResources.mutateAsync(params)}
      isPreviewing={previewResources.isPending}
      previewError={previewResources.error?.message ?? null}
      schemaLoader={fetchYamlSchemas}
    />
  )
}
