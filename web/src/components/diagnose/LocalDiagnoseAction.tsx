import { Loader2, Sparkles } from "lucide-react";
import { useDiagnose, useDiagnoseLayout } from "./DiagnoseContext";
import { runTargetKey } from "./target";
import { Tooltip } from "../ui/Tooltip";
import type { RenderDiagnoseAction } from "../../context/DiagnoseCustomization";

// The per-resource AI entry point. It no longer owns a panel — it just dispatches
// to the single app-level AI surface (DiagnoseContext), opening a new investigation
// for this resource. Self-hides when no agent CLI is present. Hosts can override
// this slot with their own action.
//
// Adaptive by health: on a resource with a live problem it reads as a prominent
// "Investigate" action; when the resource is fine or health is unknown it shrinks
// to a quiet colored-icon affordance ("ask my agent about this") — so it never
// implies "something is wrong here" on a healthy resource. The tooltip leads with
// the BYO framing (the user's OWN agent, locally) — unless the agent is hosted,
// where those claims would be false.
function DiagnoseResourceButton({
  kind,
  group,
  namespace,
  name,
  health,
}: {
  kind: string;
  group?: string;
  namespace: string;
  name: string;
  health?: "problem" | "healthy" | "unknown";
}) {
  const d = useDiagnose();
  const { runningKeys } = useDiagnoseLayout();
  // Hidden only when the feature can't work here (auth/cloud/--no-mcp). When it's
  // supported but no agent is installed we KEEP the button — clicking opens the
  // setup notice so the feature is discoverable rather than silently absent.
  if (d.setupState === "off") return null;
  const ready = d.available;
  const problem = health === "problem";
  const running =
    ready && runningKeys.has(runTargetKey(kind, namespace, name, group ?? ""));
  const tooltip = !ready
    ? "Set up AI investigations — runs your own agent locally."
    : running
      ? `${d.agentLabel} is investigating this resource — click to watch it live.`
      : d.hosted
        ? problem
          ? `Investigate with ${d.agentLabel} — reads this resource's spec, events & logs to look for the cause.`
          : `Ask ${d.agentLabel} about this resource — reads its spec, events & logs.`
        : problem
          ? `Investigate with your own ${d.agentLabel} — runs locally and reads this resource's spec, events & logs.`
          : `Ask your own ${d.agentLabel} about this resource — runs locally, reads its spec, events & logs.`;
  // While an investigation is live, the button advertises it (and clicking focuses
  // the existing run rather than starting a new one — openInvestigation dedups).
  const showLabel = problem || running;
  return (
    <Tooltip content={tooltip} position="bottom">
      <button
        onClick={() =>
          ready
            ? d.openInvestigation({
                kind,
                group: group ?? "",
                namespace,
                name,
              })
            : d.openHome()
        }
        aria-label={
          !ready
            ? "Set up AI investigations"
            : running
              ? "Investigation running — click to view"
              : problem
                ? "Investigate with AI"
                : "Ask AI about this resource"
        }
        className={
          showLabel
            ? "inline-flex items-center gap-1.5 rounded-lg border border-accent/40 bg-accent/5 px-2.5 py-1.5 text-sm font-medium text-accent hover:bg-accent/10"
            : "inline-flex items-center rounded-lg border border-theme-border p-1.5 text-theme-text-secondary hover:bg-theme-hover hover:text-theme-text-primary"
        }
      >
        {running ? (
          <Loader2 className="h-3.5 w-3.5 animate-spin text-accent" />
        ) : (
          <Sparkles className="h-3.5 w-3.5 text-accent" />
        )}
        {running ? "Investigating…" : problem && "Investigate"}
      </button>
    </Tooltip>
  );
}

export const defaultDiagnoseAction: RenderDiagnoseAction = ({
  kind,
  group,
  namespace,
  name,
  health,
}) => (
  <DiagnoseResourceButton
    kind={kind}
    group={group}
    namespace={namespace}
    name={name}
    health={health}
  />
);

// Compact per-issue "Investigate" action for the Issues queue — launches an
// investigation for the issue's subject from where the problem is surfaced.
// stopPropagation so it doesn't toggle the issue row it lives in.
export function IssueDiagnoseButton({
  kind,
  group,
  namespace,
  name,
}: {
  kind: string;
  group?: string;
  namespace: string;
  name: string;
}) {
  const d = useDiagnose();
  if (d.setupState === "off") return null;
  const ready = d.available;
  return (
    <Tooltip
      content={
        d.setupState === "needs-restart"
          ? "Restart Radar to enable AI investigations — a supported agent is installed"
          : !ready
            ? "Set up AI investigations — install a local agent"
            : d.hosted
              ? `Sends this resource's context to ${d.agentLabel} for investigation`
              : `Runs ${d.agentLabel} on your machine to investigate this resource`
      }
      position="left"
    >
      <button
        onClick={(e) => {
          e.stopPropagation();
          if (ready)
            d.openInvestigation({
              kind,
              group: group ?? "",
              namespace,
              name,
            });
          else d.openHome();
        }}
        className="flex shrink-0 items-center gap-1 rounded-md border border-theme-border px-2 py-1 text-xs text-theme-text-secondary hover:bg-theme-hover hover:text-theme-text-primary"
      >
        <Sparkles className="h-3 w-3 text-accent" />
        Investigate
      </button>
    </Tooltip>
  );
}

// Global top-bar entry into the AI surface (opens its Home / recent
// investigations). Self-hides when no agent CLI is present.
export function GlobalDiagnoseButton() {
  const d = useDiagnose();
  const { runningKeys } = useDiagnoseLayout();
  if (d.setupState === "off") return null;
  const ready = d.available;
  const runningCount = runningKeys.size;
  const agentSuffix = d.hosted
    ? `powered by ${d.agentLabel}`
    : `runs your own ${d.agentLabel} locally`;
  return (
    <Tooltip
      content={
        !ready
          ? "Set up AI investigations — runs your own agent locally"
          : runningCount > 0
            ? `${runningCount} investigation${runningCount > 1 ? "s" : ""} running — ${agentSuffix}`
            : `AI investigations — ${agentSuffix}`
      }
      position="bottom"
    >
      <button
        onClick={d.openHome}
        className="relative rounded-md bg-theme-elevated p-1.5 text-theme-text-secondary transition-colors hover:bg-theme-hover hover:text-theme-text-primary"
        aria-label={
          runningCount > 0
            ? `AI investigations (${runningCount} running)`
            : "AI investigations"
        }
      >
        <Sparkles className="h-4 w-4 text-accent" />
        {runningCount > 0 && (
          <span className="absolute right-0.5 top-0.5 flex h-2 w-2">
            <span className="absolute inline-flex h-full w-full animate-ping rounded-full bg-accent opacity-75" />
            <span className="relative inline-flex h-2 w-2 rounded-full bg-accent" />
          </span>
        )}
      </button>
    </Tooltip>
  );
}
