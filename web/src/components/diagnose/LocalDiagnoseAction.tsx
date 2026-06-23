import { Sparkles } from "lucide-react";
import { useDiagnose } from "./DiagnoseContext";
import { Tooltip } from "../ui/Tooltip";
import type { RenderDiagnoseAction } from "../../context/DiagnoseCustomization";

// The per-resource AI entry point. It no longer owns a panel — it just dispatches
// to the single app-level AI surface (DiagnoseContext), opening a new investigation
// for this resource. Self-hides when no agent CLI is present. A host like Radar Hub
// overrides this slot with its own action.
//
// Adaptive by health: on a resource with a live problem it reads as a prominent
// "Diagnose" (find the root cause); when the resource is fine or health is unknown
// it shrinks to a quiet colored-icon affordance ("ask my agent about this") — so it
// never implies "something is wrong here" on a healthy resource. The tooltip leads
// with the BYO framing: this runs the user's OWN agent, locally.
function DiagnoseResourceButton({
  kind,
  namespace,
  name,
  health,
}: {
  kind: string;
  namespace: string;
  name: string;
  health?: "problem" | "healthy" | "unknown";
}) {
  const d = useDiagnose();
  if (!d.available) return null;
  const problem = health === "problem";
  const tooltip = problem
    ? `Diagnose with your own ${d.agentLabel} — runs locally, reads this resource's spec, events & logs to find the root cause.`
    : `Ask your own ${d.agentLabel} about this resource — runs locally, reads its spec, events & logs.`;
  return (
    <Tooltip content={tooltip} position="bottom">
      <button
        onClick={() => d.openInvestigation({ kind, namespace, name })}
        aria-label={problem ? "Diagnose with AI" : "Ask AI about this resource"}
        className={
          problem
            ? "inline-flex items-center gap-1.5 rounded-lg border border-accent/40 bg-accent/5 px-2.5 py-1.5 text-sm font-medium text-accent hover:bg-accent/10"
            : "inline-flex items-center rounded-lg border border-theme-border p-1.5 text-theme-text-secondary hover:bg-theme-hover hover:text-theme-text-primary"
        }
      >
        <Sparkles className="h-3.5 w-3.5 text-accent" />
        {problem && "Diagnose"}
      </button>
    </Tooltip>
  );
}

export const defaultDiagnoseAction: RenderDiagnoseAction = ({
  kind,
  namespace,
  name,
  health,
}) => (
  <DiagnoseResourceButton
    kind={kind}
    namespace={namespace}
    name={name}
    health={health}
  />
);

// Compact per-issue "Diagnose" action for the Issues queue — launches an
// investigation for the issue's subject from where the problem is surfaced.
// stopPropagation so it doesn't toggle the issue row it lives in.
export function IssueDiagnoseButton({
  kind,
  namespace,
  name,
}: {
  kind: string;
  namespace: string;
  name: string;
}) {
  const d = useDiagnose();
  if (!d.available) return null;
  return (
    <Tooltip
      content={`Runs ${d.agentLabel} on your machine and sends it this resource's context to find the root cause`}
      position="left"
    >
      <button
        onClick={(e) => {
          e.stopPropagation();
          d.openInvestigation({ kind, namespace, name });
        }}
        className="flex shrink-0 items-center gap-1 rounded-md border border-theme-border px-2 py-1 text-xs text-theme-text-secondary hover:bg-theme-hover hover:text-theme-text-primary"
      >
        <Sparkles className="h-3 w-3 text-accent" />
        Diagnose
      </button>
    </Tooltip>
  );
}

// Global top-bar entry into the AI surface (opens its Home / recent
// investigations). Self-hides when no agent CLI is present.
export function GlobalDiagnoseButton() {
  const d = useDiagnose();
  if (!d.available) return null;
  return (
    <Tooltip
      content={`AI investigations — runs your own ${d.agentLabel} locally`}
      position="bottom"
    >
      <button
        onClick={d.openHome}
        className="rounded-md bg-theme-elevated p-1.5 text-theme-text-secondary transition-colors hover:bg-theme-hover hover:text-theme-text-primary"
        aria-label="AI investigations"
      >
        <Sparkles className="h-4 w-4 text-accent" />
      </button>
    </Tooltip>
  );
}
