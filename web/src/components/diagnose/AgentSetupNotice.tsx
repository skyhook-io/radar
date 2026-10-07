import { useState } from "react";
import clsx from "clsx";
import { Sparkles, Copy, Check, ExternalLink, RotateCw } from "lucide-react";
import { copyText } from "@skyhook-io/k8s-ui/utils/clipboard";
import { SUPPORTED_AGENTS, type AgentInstall } from "./agentCatalog";
import { type DiagnoseSetup } from "./DiagnoseContext";
import { openExternal } from "../../utils/navigation";

const REPORT_URL =
  "https://github.com/skyhook-io/radar/issues/new?labels=bug&template=bug_report.md";

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

// Shown in the AI surface's Home when investigations are eligible in this
// deployment but not runnable yet. "needs-install": no agent CLI found; the
// server picks one up as soon as it's installed, so the notice offers a re-check
// rather than a restart. "needs-restart": RADAR_AI_CLI_BIN pins the engine to a
// file Radar can't run, which only a corrected variable and a restart fix.
export function AgentSetupNotice({
  setupState,
  checkingAgents,
  recheckAgents,
}: {
  setupState: DiagnoseSetup;
  checkingAgents: boolean;
  recheckAgents: () => Promise<void>;
}) {
  const [checked, setChecked] = useState(false);
  const badOverride = setupState === "needs-restart";
  return (
    <div className="mx-auto max-w-md px-1 py-6">
      <div className="mb-3 flex h-10 w-10 items-center justify-center rounded-xl border border-accent/30 bg-accent/5">
        <Sparkles className="h-5 w-5 text-accent" />
      </div>
      <h3 className="text-base font-semibold text-theme-text-primary">
        {badOverride
          ? "Radar can't run the agent CLI it was given"
          : "Set up AI investigations"}
      </h3>
      {badOverride ? (
        <p className="mt-1 text-sm text-theme-text-secondary">
          This Radar was started with{" "}
          <code className="inline-code">RADAR_AI_CLI_BIN</code> set to a file it
          can&apos;t run, so it isn&apos;t using the agent CLIs it found.
          Correct the path or remove the variable, then restart Radar. The
          startup output shows the path it tried.
        </p>
      ) : (
        <>
          <p className="mt-1 text-sm text-theme-text-secondary">
            Radar runs investigations through a coding agent CLI on your own
            machine, with no Radar cloud and no API key. Install one of these:
          </p>
          <div className="mt-4 flex flex-col gap-2.5">
            {SUPPORTED_AGENTS.map((a) => (
              <AgentRow key={a.name} agent={a} />
            ))}
          </div>
          <button
            type="button"
            disabled={checkingAgents}
            onClick={() => {
              void recheckAgents().then(() => setChecked(true));
            }}
            className="btn-brand mt-4 inline-flex items-center gap-1.5 px-3 py-1.5 text-sm"
          >
            <RotateCw
              className={clsx("h-3.5 w-3.5", checkingAgents && "animate-spin")}
            />
            {checkingAgents ? "Checking…" : "Check again"}
          </button>
          {checked && !checkingAgents && (
            <p className="mt-2 text-xs text-theme-text-secondary">
              Still no agent CLI found. Radar looks on its PATH and in each
              tool&apos;s usual install folder. If yours runs in a terminal and
              Radar still can&apos;t find it,{" "}
              <button
                type="button"
                onClick={() => openExternal(REPORT_URL)}
                className="underline hover:text-theme-text-primary"
              >
                report it
              </button>
              .
            </p>
          )}
        </>
      )}

      <p className="mt-4 text-xs text-theme-text-tertiary">
        Your agent runs locally. Resource details and logs are sent to its model
        provider under your account, not to Radar.
      </p>
    </div>
  );
}
