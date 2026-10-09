import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import type { CNPGSchedulePreview } from '@skyhook-io/k8s-ui'
import { fetchJSON, useRadarFeature } from './client'
import type { ActionRequest } from './actions'
import { cnpgPath, type CNPGActionResult } from './cnpg'

export interface CNPGArchivingParams {
  objectStore: string
  serverName: string
  acknowledgeArchive: boolean
}
export interface CNPGArchivingFacts {
  clusterConfig: string
  objectStoreUID: string
  objectStoreConfig: string
  objectStore: string
  serverName: string
}
export interface CNPGArchivingPreview {
  context: string
  uid: string
  facts: CNPGArchivingFacts
  destination: string
  endpoint?: string
  plugin: Record<string, unknown>
  warnings: string[]
  unchanged: boolean
}
export interface CNPGScheduleMethodFacts {
  clusterUID: string
  clusterConfig: string
  scheduleConfig: string
}
export interface CNPGScheduleMethodPreview {
  context: string
  uid: string
  cluster: string
  previousMethod: string
  facts: CNPGScheduleMethodFacts
  unchanged: boolean
}

export function useCNPGArchivingPreview(namespace: string, name: string) {
  const { guard } = useRadarFeature('cnpgProtectionSetup')
  return useMutation<CNPGArchivingPreview, Error, { reviewedContext: string; objectStore: string; serverName: string }>(
    {
      mutationFn: (request) =>
        guard(() =>
          fetchJSON(`${cnpgPath('clusters', namespace, name)}/protection/preview`, {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify(request),
          }),
        ),
    },
  )
}

export function useCNPGScheduleMethodPreview(namespace: string, name: string) {
  const { guard, gatedKey } = useRadarFeature('cnpgProtectionSetup')
  return useQuery<CNPGScheduleMethodPreview>({
    queryKey: ['cnpg', 'method-preview', namespace, name, ...gatedKey],
    queryFn: ({ signal }) =>
      guard(() => fetchJSON(`${cnpgPath('scheduledbackups', namespace, name)}/method-preview`, signal)),
    enabled: !!name,
    retry: false,
    staleTime: 5_000,
  })
}

export function useCNPGDraftSchedulePreview(namespace: string, name: string, schedule: string, enabled = true) {
  const { guard, gatedKey } = useRadarFeature('cnpgProtectionSetup')
  return useQuery<CNPGSchedulePreview>({
    queryKey: ['cnpg', 'draft-schedule-preview', namespace, name, schedule, ...gatedKey],
    queryFn: ({ signal }) =>
      guard(() =>
        fetchJSON(
          `${cnpgPath('clusters', namespace, name)}/schedule-preview?schedule=${encodeURIComponent(schedule)}`,
          signal,
        ),
      ),
    enabled: enabled && !!name,
    retry: false,
    staleTime: 30_000,
    placeholderData: (previous) => previous,
  })
}

type ProtectionAction =
  | { kind: 'clusters'; action: 'configureArchiving'; request: ActionRequest<CNPGArchivingFacts, CNPGArchivingParams> }
  | { kind: 'scheduledbackups'; action: 'repairMethod'; request: ActionRequest<CNPGScheduleMethodFacts> }

export function useCNPGProtectionAction(namespace: string, name: string) {
  const { guard } = useRadarFeature('cnpgProtectionSetup')
  const client = useQueryClient()
  return useMutation<CNPGActionResult, Error, ProtectionAction>({
    mutationFn: ({ kind, action, request }) =>
      guard(() =>
        fetchJSON(`${cnpgPath(kind, namespace, name)}/actions/${action}`, {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify(request),
        }),
      ),
    onSettled: () => {
      client.invalidateQueries({ queryKey: ['cnpg'] })
      client.invalidateQueries({ queryKey: ['resource'] })
    },
  })
}
