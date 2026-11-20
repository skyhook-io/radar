import { PaneLoader } from '@skyhook-io/k8s-ui'
import { AlertBanner } from '@skyhook-io/k8s-ui/components/ui/drawer-components'
import { CRDSchemaBrowser } from '@skyhook-io/k8s-ui/components/resources/renderers/CRDSchemaBrowser'
import { useResource } from '../../../api/client'
import { useTheme } from '../../../context/ThemeContext'

export function CRDSchemaTab({ name }: { name: string }) {
  const { data, isLoading, error } = useResource<any>('customresourcedefinitions', '', name, 'apiextensions.k8s.io')
  const { theme } = useTheme()
  if (isLoading) return <PaneLoader label="Loading schema…" />
  if (error) return <AlertBanner variant="error" title="Couldn't load schema" message={error.message} />
  return data ? <CRDSchemaBrowser data={data} theme={theme} /> : null
}
