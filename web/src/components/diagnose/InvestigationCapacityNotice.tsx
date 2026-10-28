import { useState } from "react";
import { formatRelativeAgeTime } from "@skyhook-io/k8s-ui/utils/format";
import { stopRun, type RunSummary } from "../../api/diagnose";
import { useContexts } from "../../api/client";
import {
  useDiagnose,
  type StartCapacity,
  type Target,
} from "./DiagnoseContext";
import { formatInvestigationTarget } from "./target";

// The runs holding a slot, oldest first: the order they were started in.
export function runsHoldingSlots(runs: RunSummary[]): RunSummary[] {
  return runs
    .filter((r) => r.status === "running" || r.status === "stopping")
    .sort(
      (a, b) =>
        new Date(a.createdAt).getTime() - new Date(b.createdAt).getTime(),
    );
}

function runLabel(run: RunSummary): string {
  return run.name
    ? formatInvestigationTarget(run)
    : run.preview || "Investigation";
}

function targetLabel(target: Target): string {
  return target.name ? formatInvestigationTarget(target) : "this investigation";
}

// A start refused without a cluster name (a host that sends none) has
// nothing to compare, so it is taken as this one.
export function capacityCluster(
  refusedIn: string | undefined,
  current: string | undefined,
): "same" | "changed" | "unknown" {
  if (!refusedIn || current === refusedIn) return "same";
  return current ? "changed" : "unknown";
}

function without(set: ReadonlySet<string>, id: string): ReadonlySet<string> {
  const next = new Set(set);
  next.delete(id);
  return next;
}

const SECONDARY_BUTTON =
  "rounded-lg border border-theme-border px-3 py-1.5 text-sm text-theme-text-secondary hover:bg-theme-hover disabled:cursor-not-allowed disabled:opacity-50";

/**
 * The start the user asked for was refused because every investigation slot
 * is taken. Lists what holds them, with a way to open or stop each, and
 * starts the same request again once a slot is free. Starting stays a click:
 * it spends an agent run and takes over the panel, and by the time a slot
 * frees the user may be reading one of the others.
 *
 * runsKnown is false until the runs list has loaded, or while it fails: an
 * empty list then says nothing about free slots, so Start stays available
 * and the server decides.
 *
 * cluster compares the cluster the start was refused in with the current
 * one. A retry resolves the same namespace and name against whatever Radar
 * is connected to, so it waits for a confirmed match.
 */
export function InvestigationCapacityNotice({
  capacity,
  runs,
  runsKnown,
  cluster,
  onOpenRun,
  onStop,
  onStart,
  onDismiss,
}: {
  capacity: StartCapacity;
  runs: RunSummary[];
  runsKnown: boolean;
  cluster: "same" | "changed" | "unknown";
  onOpenRun: (id: string) => void;
  onStop: (id: string) => Promise<void>;
  onStart: () => void;
  onDismiss: () => void;
}) {
  // Runs whose stop request is in flight, then the ones it settled for. The
  // server marks a run stopped before answering, so a settled run the
  // refreshed list still shows running did not stop.
  const [stopping, setStopping] = useState<ReadonlySet<string>>(new Set());
  const [stopSettled, setStopSettled] = useState<ReadonlySet<string>>(
    new Set(),
  );
  const holding = runsHoldingSlots(runs);
  const slotFree = runsKnown && holding.length < capacity.limit;
  const canStart = cluster === "same" && (!runsKnown || slotFree);
  const target = targetLabel(capacity.target);

  const stop = (id: string) => {
    setStopping((prev) => new Set(prev).add(id));
    setStopSettled((prev) => without(prev, id));
    const settle = () => {
      setStopping((prev) => without(prev, id));
      setStopSettled((prev) => new Set(prev).add(id));
    };
    onStop(id).then(settle, settle);
  };

  if (cluster === "changed") {
    return (
      <div className="flex flex-1 flex-col items-center justify-center gap-3 px-6 text-center">
        <p className="text-sm text-theme-text-secondary">
          {`The cluster changed since you asked to investigate ${target}, so Radar won’t start it here. Open the resource in this cluster to investigate it.`}
        </p>
        <button type="button" onClick={onDismiss} className={SECONDARY_BUTTON}>
          Dismiss
        </button>
      </div>
    );
  }

  return (
    <div className="flex flex-1 flex-col overflow-y-auto px-4 py-6">
      <div className="mx-auto flex w-full max-w-xl flex-col gap-4">
        <div>
          <h2 className="text-sm font-medium text-theme-text-primary">
            {slotFree
              ? "A slot is free"
              : `${capacity.limit} investigations are already running`}
          </h2>
          <p className="mt-1 text-sm text-theme-text-secondary">
            {slotFree
              ? `Start ${target} now.`
              : `Radar runs up to ${capacity.limit} at a time. Stop one to start ${target}, or wait for one to finish.`}
          </p>
        </div>
        {!runsKnown ? (
          <p className="text-sm text-theme-text-tertiary">
            Loading the running investigations…
          </p>
        ) : holding.length > 0 ? (
          <ul
            aria-label="Running investigations"
            className="divide-y divide-theme-border rounded-lg border border-theme-border"
          >
            {holding.map((run) => {
              const isStopping =
                stopping.has(run.id) || run.status === "stopping";
              const didNotStop =
                !isStopping &&
                stopSettled.has(run.id) &&
                run.status === "running";
              return (
                <li key={run.id} className="flex items-center gap-3 px-3 py-2">
                  <div className="min-w-0 flex-1">
                    <div className="truncate text-sm text-theme-text-primary">
                      {runLabel(run)}
                    </div>
                    <div className="text-xs text-theme-text-tertiary">
                      {isStopping
                        ? "Stopping…"
                        : didNotStop
                          ? "Still running. Radar couldn’t stop it; try again."
                          : `Started ${formatRelativeAgeTime(run.createdAt)}`}
                    </div>
                  </div>
                  <button
                    type="button"
                    onClick={() => onOpenRun(run.id)}
                    className={SECONDARY_BUTTON}
                  >
                    Open
                  </button>
                  <button
                    type="button"
                    disabled={isStopping}
                    onClick={() => stop(run.id)}
                    className={SECONDARY_BUTTON}
                    aria-label={`Stop ${runLabel(run)}`}
                  >
                    Stop
                  </button>
                </li>
              );
            })}
          </ul>
        ) : null}
        <div className="flex flex-wrap items-center gap-2">
          <button
            type="button"
            disabled={!canStart}
            onClick={onStart}
            className="btn-brand px-3 py-1.5 text-sm"
          >
            Start investigation
          </button>
          <button
            type="button"
            onClick={onDismiss}
            className={SECONDARY_BUTTON}
          >
            Cancel
          </button>
          {cluster === "unknown" ? (
            <span className="text-xs text-theme-text-tertiary">
              Checking which cluster Radar is connected to…
            </span>
          ) : null}
        </div>
      </div>
    </div>
  );
}

/** InvestigationCapacityNotice wired to the investigation state. */
export function InvestigationCapacityRecovery({
  capacity,
}: {
  capacity: StartCapacity;
}) {
  const d = useDiagnose();
  const { data: contexts } = useContexts();
  const current = contexts?.find((c) => c.isCurrent)?.name;
  return (
    <InvestigationCapacityNotice
      capacity={capacity}
      runs={d.runs}
      runsKnown={d.runsLoaded && !d.runsLoadFailed}
      cluster={capacityCluster(capacity.context, current)}
      onOpenRun={d.openRun}
      onStop={async (id) => {
        await stopRun(id);
        await d.refreshRuns();
      }}
      onStart={() => d.openInvestigation(capacity.target)}
      onDismiss={d.dismissError}
    />
  );
}
