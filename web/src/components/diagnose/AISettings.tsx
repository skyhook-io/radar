// The agent/model/effort controls + a clear-history action for the Settings
// "AI diagnose" tab. Controlled by the dialog: it edits a STAGED draft committed
// on Save (like the rest of Settings), not on every keystroke. The heading,
// description, and Save button live in the dialog so this tab matches the other
// Settings tabs' layout — this renders only the controls (no card, no heading).
import { useState } from "react";
import { Trash2 } from "lucide-react";
import { clearHistory, type AgentInfo } from "../../api/diagnose";
import { AgentControls } from "./parts";

export interface AIDraft {
  agent: string;
  isolated: boolean;
  model: string;
  effort: string;
}

// ClearHistoryRow is an immediate action (not part of the staged draft): a
// two-step confirm button that wipes finished investigations from the local
// history DB. Live investigations survive.
function ClearHistoryRow({ onCleared }: { onCleared: () => void }) {
  const [confirming, setConfirming] = useState(false);
  const [state, setState] = useState<"idle" | "busy" | "done" | "error">(
    "idle",
  );
  const run = () => {
    setState("busy");
    clearHistory()
      .then(() => {
        setState("done");
        setConfirming(false);
        onCleared();
      })
      .catch(() => setState("error"));
  };
  return (
    <div className="mt-3 flex items-center justify-between gap-2 border-t border-theme-border/60 pt-3">
      <p className="text-[11px] leading-snug text-theme-text-tertiary">
        Investigation transcripts are kept on this machine (
        <code className="font-mono">~/.radar</code>) so history survives
        restarts.
        {state === "done" && (
          <span className="ml-1 font-medium text-theme-text-secondary">
            History cleared.
          </span>
        )}
        {state === "error" && (
          <span className="ml-1 font-medium text-red-400">
            Couldn&apos;t clear history.
          </span>
        )}
      </p>
      {confirming ? (
        <div className="flex shrink-0 items-center gap-1.5">
          <button
            onClick={() => setConfirming(false)}
            className="rounded-md border border-theme-border px-2 py-1 text-xs text-theme-text-secondary hover:bg-theme-hover"
          >
            Cancel
          </button>
          <button
            onClick={run}
            disabled={state === "busy"}
            className="rounded-md border border-red-500/40 bg-red-500/10 px-2 py-1 text-xs font-medium text-red-400 hover:bg-red-500/20 disabled:opacity-50"
          >
            Clear all history
          </button>
        </div>
      ) : (
        <button
          onClick={() => {
            setState("idle");
            setConfirming(true);
          }}
          className="flex shrink-0 items-center gap-1 rounded-md border border-theme-border px-2 py-1 text-xs text-theme-text-secondary hover:bg-theme-hover hover:text-theme-text-primary"
        >
          <Trash2 className="h-3 w-3" />
          Clear history…
        </button>
      )}
    </div>
  );
}

export function AISettingsSection({
  available,
  agents,
  draft,
  onChange,
  onHistoryCleared,
}: {
  available: boolean;
  agents: AgentInfo[];
  draft: AIDraft;
  onChange: (patch: Partial<AIDraft>) => void;
  onHistoryCleared: () => void;
}) {
  if (!available || agents.length === 0) return null;
  return (
    <>
      <AgentControls
        agents={agents}
        selectedAgent={draft.agent}
        // Model + effort are agent-specific; reset them when the agent changes.
        onSelectAgent={(a) => onChange({ agent: a, model: "", effort: "" })}
        isolated={draft.isolated}
        onSetIsolated={(v) => onChange({ isolated: v })}
        model={draft.model}
        onSetModel={(v) => onChange({ model: v })}
        effort={draft.effort}
        onSetEffort={(v) => onChange({ effort: v })}
      />
      <ClearHistoryRow onCleared={onHistoryCleared} />
    </>
  );
}
