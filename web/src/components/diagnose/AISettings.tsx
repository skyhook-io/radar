// The agent/model/effort controls for the Settings "AI diagnose" tab. Controlled
// by the dialog: it edits a STAGED draft and is committed on Save (like the rest
// of Settings), not on every keystroke. The heading, description, and Save button
// live in the dialog so this tab matches the other Settings tabs' layout — this
// renders only the controls (no card, no heading).
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
  );
}
