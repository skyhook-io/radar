import { useState } from "react";
import { formatCompactAge } from "@skyhook-io/k8s-ui/utils/format";
import { stopRun, type RunSummary } from "../../api/diagnose";
import {
  useDiagnose,
  type StartCapacity,
  type Target,
} from "./DiagnoseContext";
import { formatInvestigationTarget } from "./target";

// The runs holding a slot, longest-running first: the likeliest to finish
// soon, and the order in which they were started.
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

const SECONDARY_BUTTON =
  "rounded-lg border border-theme-border px-3 py-1.5 text-sm text-theme-text-secondary hover:bg-theme-hover disabled:cursor-not-allowed disabled:opacity-50";

/**
 * The start the user asked for was refused because every investigation slot
 * is taken. Lists what holds them, with a way to open or stop each, and
 * starts the same request again once a slot is free. Starting stays a click:
 * it spends an agent run and takes over the panel, and by the time a slot
 * frees the user may be reading one of the others.
 */
export function InvestigationCapacityNotice({
  capacity,
  runs,
  onOpenRun,
  onStop,
  onStart,
  onDismiss,
}: {
  capacity: StartCapacity;
  runs: RunSummary[];
  onOpenRun: (id: string) => void;
  onStop: (id: string) => Promise<void>;
  onStart: () => void;
  onDismiss: () => void;
}) {
  const [stopping, setStopping] = useState<ReadonlySet<string>>(new Set());
  const holding = runsHoldingSlots(runs);
  const slotFree = holding.length < capacity.limit;
  const target = targetLabel(capacity.target);

  const stop = (id: string) => {
    setStopping((prev) => new Set(prev).add(id));
    void onStop(id);
  };

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
        {holding.length > 0 && (
          <ul
            aria-label="Running investigations"
            className="divide-y divide-theme-border rounded-lg border border-theme-border"
          >
            {holding.map((run) => {
              const isStopping =
                stopping.has(run.id) || run.status === "stopping";
              return (
                <li key={run.id} className="flex items-center gap-3 px-3 py-2">
                  <div className="min-w-0 flex-1">
                    <div className="truncate text-sm text-theme-text-primary">
                      {runLabel(run)}
                    </div>
                    <div className="text-xs text-theme-text-tertiary">
                      {isStopping
                        ? "Stopping…"
                        : `Running ${formatCompactAge(run.createdAt)}`}
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
        )}
        <div className="flex flex-wrap items-center gap-2">
          <button
            type="button"
            disabled={!slotFree}
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
  return (
    <InvestigationCapacityNotice
      capacity={capacity}
      runs={d.runs}
      onOpenRun={d.openRun}
      onStop={async (id) => {
        await stopRun(id);
        d.refreshRuns();
      }}
      onStart={() => d.openInvestigation(capacity.target)}
      onDismiss={d.dismissError}
    />
  );
}
