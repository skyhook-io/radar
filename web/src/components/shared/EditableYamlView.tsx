import { type ComponentProps } from 'react'
import {
  EditableYamlView as BaseEditableYamlView,
  SaveSuccessAnimation,
} from '@skyhook-io/k8s-ui'
import { useUpdateResource } from '../../api/client'

// Re-export SaveSuccessAnimation as-is (pure component, no wrapper needed)
export { SaveSuccessAnimation }

type BaseProps = ComponentProps<typeof BaseEditableYamlView>

export function EditableYamlView(props: Omit<BaseProps, 'onSave' | 'isSaving' | 'saveError'>) {
  const updateResource = useUpdateResource()

  return (
    <BaseEditableYamlView
      {...props}
      onSave={(params) => updateResource.mutateAsync(params)}
      isSaving={updateResource.isPending}
      saveError={updateResource.error?.message ?? null}
    />
  )
}
