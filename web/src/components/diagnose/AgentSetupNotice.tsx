import { useState } from "react";
import { Sparkles, Copy, Check, ExternalLink, RotateCw } from "lucide-react";
import { copyText } from "@skyhook-io/k8s-ui/utils/clipboard";
import { SUPPORTED_AGENTS, type AgentInstall } from "./agentCatalog";
import { type DiagnoseSetup } from "./DiagnoseContext";

// Just the variable name. Every `VAR=value` form is shell-specific — Windows needs
// `set` or `$env:` — so a copyable assignment would be wrong for some readers; the
// name is the part worth copying exactly, and the prose carries the value.
const CLI_BIN_VAR = "RADAR_AI_CLI_BIN";

function CopyButton({ text, label }: { text: string; label: string }) {
  const [copied, setCopied] = useState(false);
  return (
    <button
      type="button"
      onClick={() => {
        // The shared helper, not navigator.clipboard directly: a Radar served
        // over plain HTTP is an insecure origin where the async API rejects,
        // and the tick must not claim a copy that didn't happen.
        void copyText(text).then((ok) => {
          if (!ok) return;
          setCopied(true);
          setTimeout(() => setCopied(false), 1100);
        });
      }}
      className="shrink-0 rounded-md p-1 text-theme-text-tertiary hover:bg-theme-hover hover:text-theme-text-primary"
      aria-label={copied ? "Copied" : label}
    >
      {copied ? (
        <Check className="h-3.5 w-3.5 text-emerald-500" />
      ) : (
        <Copy className="h-3.5 w-3.5" />
      )}
    </button>
  );
}

function AgentRow({ agent }: { agent: AgentInstall }) {
  return (
    <div className="rounded-lg border border-theme-border bg-theme-base p-3">
      <div className="mb-2 flex items-center justify-between gap-2">
        <span className="text-sm font-medium text-theme-text-primary">
          {agent.label}
        </span>
        <a
          href={agent.docs}
          target="_blank"
          rel="noreferrer"
          className="inline-flex items-center gap-1 text-xs text-theme-text-tertiary hover:text-theme-text-primary"
        >
          Docs
          <ExternalLink className="h-3 w-3" />
        </a>
      </div>
      <div className="flex items-center gap-1.5 rounded-md bg-theme-elevated px-2 py-1.5">
        <code className="min-w-0 flex-1 overflow-x-auto whitespace-nowrap font-mono text-xs text-theme-text-secondary">
          {agent.install}
        </code>
        <CopyButton text={agent.install} label="Copy install command" />
      </div>
    </div>
  );
}

// Shown in the AI surface's Home when investigations are eligible in this deployment
// but not runnable yet — either no agent CLI is installed ("needs-install") or a
// supported one appeared after Radar booted ("needs-restart"). Radar decides the
// engine once at startup, so a fresh install needs a restart to take effect.
export function AgentSetupNotice({
  setupState,
}: {
  setupState: DiagnoseSetup;
}) {
  const needsRestart = setupState === "needs-restart";
  return (
    <div className="mx-auto max-w-md px-1 py-6">
      <div className="mb-3 flex h-10 w-10 items-center justify-center rounded-xl border border-accent/30 bg-accent/5">
        <Sparkles className="h-5 w-5 text-accent" />
      </div>
      <h3 className="text-base font-semibold text-theme-text-primary">
        {needsRestart
          ? "Restart Radar to enable AI investigations"
          : "Set up AI investigations"}
      </h3>
      <p className="mt-1 text-sm text-theme-text-secondary">
        {needsRestart ? (
          <>
            A supported agent CLI is now installed, but Radar started before it
            was — restart Radar to pick it up. Investigations then run locally
            on your machine, keyless, using your own agent.
          </>
        ) : (
          <>
            Radar runs investigations through a coding agent CLI on your own
            machine — no Radar cloud, no API key. Install one of these, then
            restart Radar:
          </>
        )}
      </p>

      {!needsRestart && (
        <>
          <div className="mt-4 flex flex-col gap-2.5">
            {SUPPORTED_AGENTS.map((a) => (
              <AgentRow key={a.name} agent={a} />
            ))}
          </div>
          <div className="mt-4 rounded-lg border border-theme-border bg-theme-base p-3">
            <p className="text-sm font-medium text-theme-text-primary">
              Already have one installed?
            </p>
            <p className="mt-1 text-xs text-theme-text-secondary">
              Radar looks for the CLI on the PATH it was started with, plus the
              usual install directories. A launch from a shortcut or a service
              often gets a shorter PATH than your terminal. Start Radar from a
              terminal where the CLI works, or set this variable to the
              CLI&apos;s full path before starting Radar:
            </p>
            <div className="mt-2 flex items-center gap-1.5 rounded-md bg-theme-elevated px-2 py-1.5">
              <code className="min-w-0 flex-1 overflow-x-auto whitespace-nowrap font-mono text-xs text-theme-text-secondary">
                {CLI_BIN_VAR}
              </code>
              <CopyButton
                text={CLI_BIN_VAR}
                label="Copy the RADAR_AI_CLI_BIN variable name"
              />
            </div>
          </div>
        </>
      )}

      {/* The panel only re-reads /api/agents on load, so after installing or
          restarting the user needs a way to refresh in place rather than
          hunting for a manual browser reload. */}
      <button
        type="button"
        onClick={() => window.location.reload()}
        className="btn-brand mt-4 inline-flex items-center gap-1.5 px-3 py-1.5 text-sm"
      >
        <RotateCw className="h-3.5 w-3.5" />
        {needsRestart ? "Reload after restarting" : "Reload after installing"}
      </button>

      <p className="mt-4 text-xs text-theme-text-tertiary">
        Your agent runs locally. Resource details and logs are sent to its model
        provider under your account, not to Radar.
      </p>
    </div>
  );
}
