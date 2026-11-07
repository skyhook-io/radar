import { useQueries } from "@tanstack/react-query";
import { isRolloutActivityVisible } from "@skyhook-io/k8s-ui/utils/workload-rollout";
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

// Each read lists every ReplicaSet or ControllerRevision in the workload's
// namespace, so a large app can't afford one per workload on every refresh.
export const MAX_WORKLOADS_READ = 12;

function rollingOut(workload: AppWorkload): boolean {
  return workload.rollout ? isRolloutActivityVisible(workload.rollout) : false;
}

// A paused Deployment makes no ReplicaSet for a new template, so none of its
// pods are on the newest template and no revision holds it.
function hasUnrolledChange({ kind, rollout }: AppWorkload): boolean {
  return kind === "Deployment" && rollout?.phase === "paused" && rollout.updated === 0 && rollout.desired > 0;
}

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
}: UseApplicationDeploysOptions): BuildAppDeploysInput | null {
  const read = workloads
    .filter((workload) => workloadHasRevisions(workload.kind))
    .sort((a, b) => Number(rollingOut(b)) - Number(rollingOut(a)))
    .slice(0, MAX_WORKLOADS_READ);
  const revisionQueries = useQueries({
    queries: read.map((workload) => ({
      ...workloadRevisionsQuery(
        kindToPluralWithGroup(workload.kind, workload.group ?? "").toLowerCase(),
        workload.namespace,
        workload.name,
        visible,
      ),
      refetchInterval: visible ? REVISIONS_REFRESH_INTERVAL_MS : false,
      refetchIntervalInBackground: false,
    })),
  });
  if (!visible) return null;
  return {
    workloads: workloads.map((workload) => {
      const query = revisionQueries[read.indexOf(workload)];
      return {
        ref: {
          kind: workload.kind,
          namespace: workload.namespace,
          name: workload.name,
          group: workload.group,
        },
        revisions: query?.data,
        desired: workload.desired,
        stale: query?.isError,
        loading: query?.isPending,
        notRead: !query,
        unrolledChange: hasUnrolledChange(workload),
      };
    }),
    anchors: history.data?.anchors ?? (history.data ? [] : undefined),
    sourceRef,
    partialSources: history.data?.partialSources,
    sourceStale: history.isError,
    sourceLoading: history.isPending,
  };
}
