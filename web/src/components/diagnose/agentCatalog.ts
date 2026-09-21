// Install metadata for the agent CLIs Radar can drive. Used by the setup notice
// shown when AI investigations are eligible but no agent is installed. Names match the
// backend's supported set (see internal/ai/detect.go knownAgents + isSupportedAgent).
export interface AgentInstall {
  name: string; // stable id, matches AgentInfo.name
  label: string; // display name
  install: string; // one-line install command
  docs: string; // canonical setup/docs URL
}

export const SUPPORTED_AGENTS: AgentInstall[] = [
  {
    name: "claude",
    label: "Claude Code",
    install: "npm install -g @anthropic-ai/claude-code",
    docs: "https://docs.claude.com/en/docs/claude-code/setup",
  },
  {
    name: "codex",
    label: "Codex",
    install: "npm install -g @openai/codex",
    docs: "https://github.com/openai/codex",
  },
  {
    name: "cursor-agent",
    label: "Cursor CLI",
    install: "curl https://cursor.com/install -fsS | bash",
    docs: "https://docs.cursor.com/en/cli/overview",
  },
];
