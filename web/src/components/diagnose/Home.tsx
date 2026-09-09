// Server-side runs keep background and running investigations visible in both
// the docked Home view and the maximized workspace's master pane.
import {
  ArrowRight,
  CircleAlert,
  Loader2,
  Server,
  Sparkles,
  Square,
} from "lucide-react";
import { type RunSummary } from "../../api/diagnose";
import {
  groupQualifiesLaneId,
  pluralToKind,
} from "@skyhook-io/k8s-ui/utils/navigation";
import { parseContextName } from "../../utils/context-name";
import { formatInvestigationTarget } from "./target";
import { Tooltip } from "../ui/Tooltip";

function historyDay(date: Date, now: Date): string {
  if (date.toDateString() === now.toDateString()) return "Today";
  const yesterday = new Date(now);
  yesterday.setDate(yesterday.getDate() - 1);
  if (date.toDateString() === yesterday.toDateString()) return "Yesterday";
  return date.toLocaleDateString(undefined, {
    month: "short",
    day: "numeric",
    ...(date.getFullYear() === now.getFullYear() ? {} : { year: "numeric" }),
  });
}

// Relative age makes the list easy to scan; a stable local timestamp makes two
// investigations of the same target distinguishable when they ran close
// together. Keep today's label compact because the date is redundant there.
export function absoluteTime(ts: number, now: number): string {
  const date = new Date(ts);
  const current = new Date(now);
  const today =
    date.getFullYear() === current.getFullYear() &&
    date.getMonth() === current.getMonth() &&
    date.getDate() === current.getDate();

  return date.toLocaleString(
    undefined,
    today
      ? { hour: "numeric", minute: "2-digit" }
      : {
          month: "short",
          day: "numeric",
          ...(date.getFullYear() === current.getFullYear()
            ? {}
            : { year: "numeric" as const }),
          hour: "numeric",
          minute: "2-digit",
        },
  );
}

const historyStatuses = {
  running: {
    label: "Running",
    short: "Running",
    Icon: Loader2,
    className: "text-accent-text",
  },
  stopping: {
    label: "Stopping",
    short: "Stopping",
    Icon: Loader2,
    className: "text-theme-text-tertiary",
  },
  done: {
    label: "Completed",
    short: "",
    Icon: undefined,
    className: "text-theme-text-tertiary",
  },
  error: {
    label: "Investigation failed",
    short: "Failed",
    Icon: CircleAlert,
    className: "text-theme-text-secondary",
  },
  stopped: {
    label: "Stopped",
    short: "Stopped",
    Icon: Square,
    className: "text-theme-text-secondary",
  },
  stale: {
    label: "Read-only investigation",
    short: "",
    Icon: undefined,
    className: "text-theme-text-tertiary",
  },
} as const;

// A short text status means no run outcome relies on decoding a 6px colored dot.
export function statusWord(status: RunSummary["status"]): {
  text: string;
  cls: string;
} {
  switch (status) {
    case "running":
      return { text: "Running", cls: "text-accent" };
    case "done":
      return { text: "Completed", cls: "text-theme-text-secondary" };
    case "error":
      return { text: "Failed", cls: "text-red-400" };
    case "stopped":
      return { text: "Stopped", cls: "text-theme-text-tertiary" };
    case "stopping":
      return { text: "Stopping", cls: "text-theme-text-tertiary" };
    case "stale":
      return { text: "Read-only", cls: "text-theme-text-tertiary" };
  }
}

export function InvestigationHome({
  agentLabel,
  onBrowseIssues,
}: {
  agentLabel: string;
  onBrowseIssues?: () => void;
}) {
  return (
    <div className="flex min-h-full w-full items-center justify-center px-4 py-8 sm:px-6">
      <section className="mx-auto max-w-2xl text-center">
        <span className="mx-auto flex h-10 w-10 items-center justify-center rounded-xl bg-accent-muted text-accent">
          <Sparkles className="h-5 w-5" />
        </span>
        <h1 className="mt-4 text-xl font-semibold text-theme-text-primary">
          Choose an investigation
        </h1>
        <p className="mx-auto mt-1 max-w-md text-sm text-theme-text-tertiary">
          {onBrowseIssues
            ? "Pick a problem from Issues, then choose "
            : "Select one from your history, or open a resource and choose "}
          <span className="font-medium text-theme-text-secondary">
            Investigate
          </span>{" "}
          to start a focused investigation with {agentLabel}.
        </p>
        {onBrowseIssues && (
          <button
            type="button"
            onClick={onBrowseIssues}
            className="btn-brand mt-4 inline-flex items-center gap-1.5 rounded-lg px-3 py-1.5 text-sm font-medium"
          >
            Browse issues
            <ArrowRight className="h-3.5 w-3.5" />
          </button>
        )}
      </section>
    </div>
  );
}

export function RecentList({
  agentLabel,
  runs,
  onSelect,
  selectedId,
  historyDegraded = false,
  currentContext,
}: {
  agentLabel: string;
  runs: RunSummary[];
  onSelect: (id: string) => void;
  selectedId?: string | null;
  historyDegraded?: boolean;
  currentContext?: string;
}) {
  const now = new Date();
  const contexts = new Map(
    runs.map((r) => [r.context, parseContextName(r.context)]),
  );
  const contextsByName = new Map<string, Set<string>>();
  const groupsByKind = new Map<string, Set<string>>();
  for (const [raw, parsed] of contexts) {
    const names = contextsByName.get(parsed.clusterName) ?? new Set<string>();
    names.add(raw);
    contextsByName.set(parsed.clusterName, names);
  }
  for (const r of runs) {
    const kind = pluralToKind(r.kind);
    const groups = groupsByKind.get(kind) ?? new Set<string>();
    // Match Radar's resource-lane display convention: built-in API groups
    // share a readable kind label; custom groups may need disambiguation.
    groups.add(groupQualifiesLaneId(r.group) ? r.group : "");
    groupsByKind.set(kind, groups);
  }
  const organizationRuns = runs.filter(
    (r) => r.trigger === "background" || r.ownedByMe === false,
  );
  const yourRuns = runs.filter((r) => !organizationRuns.includes(r));
  const collections = organizationRuns.length
    ? [
        { label: "Your investigations", runs: yourRuns },
        { label: "Organization", runs: organizationRuns },
      ].filter((collection) => collection.runs.length > 0)
    : [{ label: "", runs }];
  // Status bookkeeping (including cluster switches) updates updatedAt. It must
  // not change the apparent start time or reshuffle the navigation list.
  const groupedCollections = collections.map((collection) => {
    const days = new Map<string, RunSummary[]>();
    for (const r of [...collection.runs].sort(
      (a, b) => Date.parse(b.createdAt) - Date.parse(a.createdAt),
    )) {
      const day = historyDay(new Date(r.createdAt), now);
      const entries = days.get(day) ?? [];
      entries.push(r);
      days.set(day, entries);
    }
    return { label: collection.label, days };
  });

  // Persistence broke (disk error) — without this the user reasonably assumes
  // their history survives a restart, and it won't.
  const degradedNote = historyDegraded ? (
    <div className="mb-2 rounded-md border border-amber-500/40 bg-amber-500/10 px-2.5 py-1.5 text-[11px] leading-snug text-theme-text-secondary">
      History isn&apos;t being saved right now (disk error) — investigations
      won&apos;t survive a restart.
    </div>
  ) : null;

  if (runs.length === 0) {
    return (
      <div>
        {degradedNote}
        <div className="flex flex-col items-center px-4 py-12 text-center">
          <Sparkles className="mb-3 h-7 w-7 text-accent" />
          <div className="text-sm font-medium text-theme-text-primary">
            No investigations yet
          </div>
          <p className="mt-1 max-w-xs text-sm text-theme-text-tertiary">
            Open a resource and use its{" "}
            <Sparkles className="inline h-3.5 w-3.5 align-text-bottom text-accent" />{" "}
            action to investigate it with {agentLabel} —{" "}
            <span className="font-medium text-theme-text-secondary">
              Investigate
            </span>{" "}
            a problem, or just ask about it. Investigations run in the
            background and are kept in your history here.
          </p>
        </div>
      </div>
    );
  }

  return (
    <div className="space-y-4">
      {degradedNote}
      <h2 className="px-2 text-sm font-medium text-theme-text-secondary">
        Investigations
      </h2>
      {groupedCollections.map((collection) => (
        <div key={collection.label} className="space-y-4">
          {collection.label && (
            <h3 className="px-2 text-xs font-semibold text-theme-text-secondary">
              {collection.label}
            </h3>
          )}
          {[...collection.days].map(([day, entries]) => (
            <section key={day} aria-label={day} className="space-y-1">
              <h3 className="px-2 pb-1 text-xs font-medium text-theme-text-tertiary">
                {day}
              </h3>
              {entries.map((r) => {
                const { label, short, Icon, className } =
                  historyStatuses[r.status];
                const parsed = contexts.get(r.context)!;
                const collision =
                  contextsByName.get(parsed.clusterName)!.size > 1;
                const peers = [
                  ...contextsByName.get(parsed.clusterName)!,
                ].filter((raw) => raw !== r.context);
                const qualifier =
                  parsed.account &&
                  peers.every(
                    (raw) => contexts.get(raw)!.account !== parsed.account,
                  )
                    ? parsed.account
                    : parsed.account &&
                        parsed.region &&
                        peers.every((raw) => {
                          const peer = contexts.get(raw)!;
                          return (
                            peer.account !== parsed.account ||
                            peer.region !== parsed.region
                          );
                        })
                      ? `${parsed.account} · ${parsed.region}`
                      : r.context;
                const readableKind = pluralToKind(r.kind);
                const kind = groupsByKind.get(readableKind)!.size > 1
                    ? `${readableKind} · ${r.group || "core"}`
                    : readableKind;
                const initialIssue = r.health?.topReason?.trim();
                const isCurrentCluster = currentContext === r.context;
                const visibility =
                  r.trigger === "background"
                    ? "Automatic"
                    : r.visibility === "organization"
                      ? "Shared"
                      : r.visibility === "private"
                        ? "Private"
                        : "";
                const identity = `${formatInvestigationTarget(r)} · ${r.context}${isCurrentCluster ? " · Current cluster" : ""} · ${label}${visibility ? ` · ${visibility}` : ""} · Started ${new Date(r.createdAt).toLocaleString()}${initialIssue ? ` · Started with ${initialIssue}` : ""}`;
                return (
                  <button
                    key={r.id}
                    onClick={() => onSelect(r.id)}
                    aria-label={identity}
                    aria-current={r.id === selectedId ? "true" : undefined}
                    className={`flex w-full min-w-0 flex-col gap-0.5 rounded-md border-l-2 px-2 py-2 text-left focus-visible:outline-2 focus-visible:outline-accent ${
                      r.id === selectedId
                        ? "border-accent bg-accent-muted"
                        : "border-transparent hover:bg-theme-hover"
                    }`}
                  >
                    <span className="flex w-full items-start gap-2">
                      <Tooltip
                        content={r.name}
                        position="right"
                        delay={600}
                        className="pointer-events-none"
                        wrapperClassName="min-w-0 flex-1"
                      >
                        <span className="min-w-0 flex-1 line-clamp-2 break-words text-sm font-medium leading-5 text-theme-text-primary">
                          {r.name}
                        </span>
                      </Tooltip>
                      {(Icon || short) && (
                        <span
                          aria-hidden="true"
                          className={`flex shrink-0 items-center gap-1 text-xs leading-5 ${className}`}
                        >
                          {Icon && (
                            <Icon
                              className={`mt-0.5 h-3.5 w-3.5 ${r.status === "running" || r.status === "stopping" ? "animate-spin motion-reduce:animate-none" : r.status === "error" ? "text-semantic-error" : ""}`}
                            />
                          )}
                          {short}
                        </span>
                      )}
                    </span>
                    <span className="flex w-full items-baseline gap-2 text-xs leading-4 text-theme-text-secondary">
                      <span className="min-w-0 flex-1 truncate">
                        {r.namespace ? `${r.namespace} · ` : ""}
                        {kind}
                      </span>
                      <time
                        dateTime={r.createdAt}
                        className="shrink-0 tabular-nums text-theme-text-tertiary"
                      >
                        {new Date(r.createdAt).toLocaleTimeString(undefined, {
                          hour: "numeric",
                          minute: "2-digit",
                        })}
                      </time>
                    </span>
                    <Tooltip
                      content={`${r.context}${isCurrentCluster ? " · Current cluster" : ""}`}
                      position="right"
                      delay={600}
                      className="pointer-events-none"
                      wrapperClassName="w-full min-w-0"
                    >
                      <span
                        aria-label={
                          isCurrentCluster
                            ? `Current cluster: ${parsed.clusterName}`
                            : `Cluster: ${parsed.clusterName}`
                        }
                        className={`flex w-full items-center gap-1 text-xs leading-4 ${isCurrentCluster ? "text-accent-text" : "text-theme-text-tertiary"}`}
                      >
                        <Server className="h-3 w-3 shrink-0" aria-hidden />
                        <span className="min-w-0 flex-1 truncate">
                          {parsed.clusterName}
                        </span>
                        {visibility && (
                          <span className="shrink-0 text-theme-text-tertiary">
                            {visibility}
                          </span>
                        )}
                      </span>
                    </Tooltip>
                    {collision && qualifier !== parsed.clusterName && (
                      <span className="w-full break-words text-xs leading-4 text-theme-text-secondary">
                        {qualifier}
                      </span>
                    )}
                    {initialIssue && (
                      <span className="line-clamp-1 w-full text-xs leading-4 text-theme-text-secondary">
                        Started with {initialIssue}
                      </span>
                    )}
                  </button>
                );
              })}
            </section>
          ))}
        </div>
      ))}
    </div>
  );
}
