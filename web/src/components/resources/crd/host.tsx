import { Braces } from 'lucide-react'
import type { ResourceHost } from '../../../integrations/resourceHost'
import { CustomResourceDefinitionRenderer } from '../renderers/CustomResourceDefinitionRenderer'
import { CRDSchemaTab } from './CRDSchemaTab'

export const crdResourceHost: ResourceHost = {
  id: 'crd',
  resources: [{ name: 'customresourcedefinitions', group: 'apiextensions.k8s.io' }],
  renderers: { CustomResourceDefinitionRenderer },
  detailSlots: resource => ({ extraTabs: [{
    id: 'schema', label: 'Schema', icon: <Braces className="h-4 w-4" />, after: 'overview',
    render: () => <CRDSchemaTab name={resource.name} />,
  }] }),
}
