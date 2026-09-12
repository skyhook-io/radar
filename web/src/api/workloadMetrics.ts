import { useQuery } from "@tanstack/react-query";
import type { TimeSeries } from "@skyhook-io/k8s-ui/components/charts";
import { ApiError, fetchJSON, useClusterInfo, type PrometheusTimeRange } from "./client";
import { getApiBase } from "./config";

export type WorkloadMetricState =
  "available" | "partial" | "stale" | "unavailable" | "error";
export type WorkloadRequestSource = "beyla" | "istio";

export interface WorkloadMetricPanel {
  state: WorkloadMetricState;
  reason?: string;
  unit: string;
  series: TimeSeries[];
}

export interface WorkloadMetrics {
	setupRequired: boolean;
  state: WorkloadMetricState;
  reason?: string;
  source?: WorkloadRequestSource;
  sources: {
    id: WorkloadRequestSource;
    label: string;
    state: WorkloadMetricState;
  }[];
  pods: number;
  podsTotal: number;
  end: number;
  start: number;
  stepSeconds: number;
  rateWindowSeconds: number;
  panels: Partial<
    Record<
      "requests" | "errors" | "p50" | "p95" | "throttling" | "cpu" | "memory" | "observedPods",
      WorkloadMetricPanel
    >
  >;
}

export function useWorkloadMetrics(
  kind: string,
  namespace: string,
  name: string,
  range: PrometheusTimeRange,
  source: WorkloadRequestSource | "",
  enabled: boolean,
) {
  const { data: cluster } = useClusterInfo();
  return useQuery({
    queryKey: [
      "workload-metrics",
      getApiBase(),
      cluster?.context,
      kind,
      namespace,
      name,
      range,
      source,
    ],
    queryFn: ({ signal }) =>
      fetchJSON<WorkloadMetrics>(
        `/prometheus/workload/${encodeURIComponent(kind)}/${encodeURIComponent(namespace)}/${encodeURIComponent(name)}?${new URLSearchParams({ range, source })}`,
        signal,
      ),
    enabled,
    staleTime: 30_000,
    refetchInterval: (query) => query.state.data?.setupRequired ? false : 60_000,
    retry: (failureCount, error) => failureCount < 1 && error instanceof ApiError && error.status === 409,
  });
}
