import type { TimeSeries } from "@skyhook-io/k8s-ui/components/charts";

export function latestWorkloadValue(
  series: TimeSeries,
  end: number,
  stepSeconds: number,
): number | undefined {
  const points = series.dataPoints;
  const last = points[points.length - 1];
  if (
    !last ||
    last.value == null ||
    !Number.isFinite(last.value) ||
    end - last.timestamp > Math.max(90, 2 * stepSeconds)
  )
    return undefined;
  return last.value;
}

export function workloadPodValues(
  series: TimeSeries[],
  end: number,
  stepSeconds: number,
): Map<string, number | undefined> {
  return new Map(
    series
      .filter((s) => s.labels.pod)
      .map((s) => [s.labels.pod, latestWorkloadValue(s, end, stepSeconds)]),
  );
}
