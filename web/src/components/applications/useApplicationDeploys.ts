import { useQueries } from "@tanstack/react-query";
import {
  workloadHasRevisions,
  type AppHistory,
  type AppSourceRef,
  type AppWorkload,
  type BuildAppDeploysInput,
} from "@skyhook-io/k8s-ui";
import { workloadRevisionsQuery } from "../../api/client";
import { kindToPluralWithGroup } from "../../utils/navigation";

// Live and Newest are claims about right now.
const REVISIONS_REFRESH_INTERVAL_MS = 20_000;

interface UseApplicationDeploysOptions {
  workloads: AppWorkload[];
  sourceRef?: AppSourceRef;
  history: { data?: AppHistory; isPending: boolean; isError: boolean };
  /** Revisions are only read while the Deploys view is on screen. */
  visible: boolean;
}

export function useApplicationDeploys({
  workloads,
  sourceRef,
  history,
  visible,
}: UseApplicationDeploysOptions) {
  const revisioned = workloads.filter((workload) =>
    workloadHasRevisions(workload.kind),
  );
  const revisionQueries = useQueries({
    queries: revisioned.map((workload) => ({
      ...workloadRevisionsQuery(
        kindToPluralWithGroup(workload.kind, workload.group ?? "").toLowerCase(),
        workload.namespace,
        workload.name,
        visible,
      ),
      refetchInterval: visible ? REVISIONS_REFRESH_INTERVAL_MS : false,
    })),
  });
  const deploysPending =
    !visible ||
    revisionQueries.some((query) => query.isPending) ||
    history.isPending;
  const deploys: BuildAppDeploysInput | null = deploysPending
    ? null
    : {
        workloads: workloads.map((workload) => ({
          ref: {
            kind: workload.kind,
            namespace: workload.namespace,
            name: workload.name,
            group: workload.group,
          },
          revisions: revisionQueries[revisioned.indexOf(workload)]?.data,
          desired: workload.desired,
          stale: revisionQueries[revisioned.indexOf(workload)]?.isError,
        })),
        anchors: history.data?.anchors ?? (history.data ? [] : undefined),
        sourceRef,
        partialSources: history.data?.partialSources,
        sourceStale: history.isError,
      };
  return { deploys, deploysPending };
}
