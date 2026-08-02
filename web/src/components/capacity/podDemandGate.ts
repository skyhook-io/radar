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

// The workload page's pods arrive as the flattened WorkloadPodInfo wire shape
// rather than full Pod objects — same rule, different field paths.
export function workloadPodAwaitsScheduling(
  pod: { phase?: string; nodeName?: string } | undefined | null,
): boolean {
  return pod?.phase === "Pending" && !pod?.nodeName;
}
