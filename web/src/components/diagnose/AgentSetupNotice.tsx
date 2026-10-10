import { useState, type ReactNode } from "react";
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

function CheckAgainButton({
  checking,
  onCheck,
}: {
  checking: boolean;
  onCheck: () => void;
}) {
  return (
    <button
      type="button"
      disabled={checking}
      onClick={onCheck}
      className="btn-brand mt-4 inline-flex items-center gap-1.5 px-3 py-1.5 text-sm"
    >
      <RotateCw className={clsx("h-3.5 w-3.5", checking && "animate-spin")} />
      {checking ? "Checking…" : "Check again"}
    </button>
  );
}

// Shown in the AI surface's Home when investigations are eligible here but not
// runnable yet:
//  - cliOverride: RADAR_AI_CLI_BIN pins the engine to one CLI, so detection is
//    off and installing another changes nothing. Investigations being off means
//    the variable names a file Radar can't run. A working file put at that path
//    is picked up on the next check; changing the variable needs a restart.
//  - "needs-install": no agent CLI found. The server picks one up as soon as
//    it's installed, so the notice offers a re-check rather than a restart.
//  - "needs-restart": an agent is reported but investigations are off. Locally
//    that's a race with an install in progress; an embedding host reports it
//    when its own runner is down. Neither has a cause the user can act on, so
//    say only that and offer a re-check.
export function AgentSetupNotice({
  setupState,
  cliOverride,
  checkingAgents,
  agentsCheckFailed,
  recheckAgents,
}: {
  setupState: DiagnoseSetup;
  cliOverride: boolean;
  checkingAgents: boolean;
  agentsCheckFailed: boolean;
  recheckAgents: () => Promise<void>;
}) {
  // The state the panel was in when a check started. "Still…" is only true when
  // the finished check left it there.
  const [checkedState, setCheckedState] = useState<DiagnoseSetup | null>(null);
  const check = () => {
    void recheckAgents().then(() => setCheckedState(setupState));
  };
  const showResult = checkedState === setupState && !checkingAgents;
  const unreachable = showResult && agentsCheckFailed && (
    <p className="mt-2 text-xs text-theme-text-secondary">
      Couldn&apos;t check. Try again.
    </p>
  );

  if (cliOverride) {
    return (
      <SetupFrame title="Radar can't run your RADAR_AI_CLI_BIN">
        <p className="mt-1 text-sm text-theme-text-secondary">
          Fix the path, or remove it and restart Radar.
        </p>
        <CheckAgainButton checking={checkingAgents} onCheck={check} />
        {unreachable ||
          (showResult && (
            <p className="mt-2 text-xs text-theme-text-secondary">
              Still can&apos;t run it.
            </p>
          ))}
      </SetupFrame>
    );
  }
  if (setupState === "needs-restart") {
    return (
      <SetupFrame title="AI investigations aren't available right now">
        <p className="mt-1 text-sm text-theme-text-secondary">
          Try again in a moment.
        </p>
        <CheckAgainButton checking={checkingAgents} onCheck={check} />
        {unreachable ||
          (showResult && (
            <p className="mt-2 text-xs text-theme-text-secondary">
              Still not available.
            </p>
          ))}
      </SetupFrame>
    );
  }
  return (
    <SetupFrame title="Set up AI investigations">
      <p className="mt-1 text-sm text-theme-text-secondary">
        Radar runs investigations through a coding agent CLI on your own
        machine, with no Radar cloud and no API key. Install one of these:
      </p>
      <div className="mt-4 flex flex-col gap-2.5">
        {SUPPORTED_AGENTS.map((a) => (
          <AgentRow key={a.name} agent={a} />
        ))}
      </div>
      <CheckAgainButton checking={checkingAgents} onCheck={check} />
      {unreachable ||
        (showResult && (
          <p className="mt-2 text-xs text-theme-text-secondary">
            Still not found. Already installed one?{" "}
            <button
              type="button"
              onClick={() => openExternal(REPORT_URL)}
              className="underline hover:text-theme-text-primary"
            >
              Report it
            </button>
            .
          </p>
        ))}
    </SetupFrame>
  );
}

function SetupFrame({
  title,
  children,
}: {
  title: string;
  children: ReactNode;
}) {
  return (
    <div className="mx-auto max-w-md px-1 py-6">
      <div className="mb-3 flex h-10 w-10 items-center justify-center rounded-xl border border-accent/30 bg-accent/5">
        <Sparkles className="h-5 w-5 text-accent" />
      </div>
      <h3 className="text-base font-semibold text-theme-text-primary">
        {title}
      </h3>
      {children}
      <p className="mt-4 text-xs text-theme-text-tertiary">
        Your agent runs locally. Resource details and logs are sent to its model
        provider under your account, not to Radar.
      </p>
    </div>
  );
}
