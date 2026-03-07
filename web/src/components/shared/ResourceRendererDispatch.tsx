import { type ComponentProps } from 'react'
import {
  ResourceRendererDispatch as BaseResourceRendererDispatch,
  getResourceStatus,
} from '@skyhook-io/k8s-ui'
import { PrometheusCharts } from '../resource/PrometheusCharts'
import { useResourceEvents } from '../../api/client'

// Re-export getResourceStatus as-is (pure function, no wrapper needed)
export { getResourceStatus }

type BaseProps = ComponentProps<typeof BaseResourceRendererDispatch>

export function ResourceRendererDispatch(props: Omit<BaseProps, 'events' | 'eventsLoading' | 'renderMetrics'>) {
  const { data: events, isLoading: eventsLoading } = useResourceEvents(
    props.resource.kind,
    props.resource.namespace,
    props.resource.name
  )

  return (
    <BaseResourceRendererDispatch
      {...props}
      events={events}
      eventsLoading={eventsLoading}
      renderMetrics={({ kind, namespace, name }) => (
        <PrometheusCharts kind={kind} namespace={namespace} name={name} />
      )}
    />
  )
}
