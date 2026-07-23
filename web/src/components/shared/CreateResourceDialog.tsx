import { type ComponentProps } from 'react'
import { CreateResourceDialog as BaseCreateResourceDialog } from '@skyhook-io/k8s-ui'
import { fetchYamlSchemas, useApplyResource, usePreviewResources } from '../../api/client'
import { useCapabilitiesContext } from '../../contexts/CapabilitiesContext'

type BaseProps = ComponentProps<typeof BaseCreateResourceDialog>

export function CreateResourceDialog(props: Omit<BaseProps, 'onApply' | 'isApplying' | 'onPreview' | 'isPreviewing' | 'previewError' | 'schemaLoader'>) {
  const applyResource = useApplyResource()
  const previewResources = usePreviewResources()
  const { features } = useCapabilitiesContext()

  return (
    <BaseCreateResourceDialog
      {...props}
      onApply={(params) => applyResource.mutateAsync(params)}
      isApplying={applyResource.isPending}
      onPreview={features?.yamlReview ? (params) => previewResources.mutateAsync(params) : undefined}
      isPreviewing={previewResources.isPending}
      previewError={previewResources.error?.message ?? null}
      schemaLoader={features?.yamlSchemas ? fetchYamlSchemas : undefined}
    />
  )
}
