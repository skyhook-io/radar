// The "AI Diagnosis" section of the Settings dialog. Controlled by the dialog:
// it edits a STAGED draft and is committed on Save (like the rest of Settings),
// not on every keystroke. Renders nothing when no supported agent CLI is installed.
import { type AgentInfo } from "../../api/diagnose";
import { AgentControls } from "./parts";

export interface AIDraft {
  agent: string;
  isolated: boolean;
  model: string;
  effort: string;
}

export function AISettingsSection({
  available,
  agents,
  draft,
  onChange,
}: {
  available: boolean;
  agents: AgentInfo[];
  draft: AIDraft;
  onChange: (patch: Partial<AIDraft>) => void;
}) {
  if (!available || agents.length === 0) return null;
  return (
    <section className="mb-5 rounded-md border border-theme-border bg-theme-elevated/50 p-3">
      <h3 className="mb-1 text-sm font-medium text-theme-text-primary">
        AI Diagnosis
      </h3>
      <p className="mb-3 text-xs text-theme-text-tertiary">
        Investigations run on your own machine via your installed agent CLI — no
        Radar cloud, no API key. These preferences apply to new investigations.
      </p>
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
    </section>
  );
}
