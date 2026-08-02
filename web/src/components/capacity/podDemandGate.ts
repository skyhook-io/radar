interface PodLike {
  spec?: { nodeName?: string };
  status?: { phase?: string };
}

/**
 * Whether Capacity Demand would have anything to say about this pod. Demand
 * only admits pods the scheduler has not placed, so a Pending pod that already
 * holds a node (ContainerCreating, ImagePullBackOff) must not be offered a link
 * that lands on a screen it is absent from.
 */
export function podAwaitsScheduling(pod: PodLike | undefined | null): boolean {
  return pod?.status?.phase === "Pending" && !pod?.spec?.nodeName;
}
