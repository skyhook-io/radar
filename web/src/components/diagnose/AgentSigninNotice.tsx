import { useState } from "react";
import clsx from "clsx";
import { RotateCw } from "lucide-react";
import { AlertBanner } from "@skyhook-io/k8s-ui";
import { CopyButton } from "./AgentSetupNotice";

// Shown on the consent card when the agent's own status command says it is
// signed out. It warns rather than blocks: status commands can be wrong (a
// credential they don't see), so Approve stays available.
export function AgentSigninNotice({
  agentName,
  signinCmd,
  rechecking,
  onRecheck,
}: {
  agentName: string;
  signinCmd: string;
  rechecking: boolean;
  onRecheck: () => void;
}) {
  const [asked, setAsked] = useState(false);
  return (
    <AlertBanner
      variant="warning"
      className="mt-3"
      title={`${agentName} isn't signed in`}
      message={
        <>
          The investigation will fail until it is. Run this in a terminal, then
          check again.
        </>
      }
      action={
        <button
          type="button"
          disabled={rechecking}
          onClick={() => {
            setAsked(true);
            onRecheck();
          }}
          className="inline-flex items-center gap-1 text-xs text-accent hover:underline disabled:opacity-60"
        >
          <RotateCw className={clsx("h-3 w-3", rechecking && "animate-spin")} />
          {rechecking ? "Checking…" : "Check again"}
        </button>
      }
    >
      <div className="mt-2 flex items-center gap-1.5 rounded-md bg-theme-base px-2 py-1.5">
        <code className="min-w-0 flex-1 overflow-x-auto whitespace-nowrap font-mono text-xs text-theme-text-secondary">
          {signinCmd}
        </code>
        <CopyButton text={signinCmd} label="Copy sign-in command" />
      </div>
      {asked && !rechecking && (
        <p role="status" className="mt-1.5 text-xs text-theme-text-secondary">
          Still not signed in.
        </p>
      )}
    </AlertBanner>
  );
}
