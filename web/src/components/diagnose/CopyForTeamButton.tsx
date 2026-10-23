import { useState } from "react";
import { Check, ClipboardCopy } from "lucide-react";
import { copyText } from "@skyhook-io/k8s-ui/utils/clipboard";
import { Tooltip } from "../ui/Tooltip";

// Copies the whole report for a teammate: pasting it into a ticket or a chat
// is how a local investigation gets shared. The text leaves Radar with it, so
// the tooltip says what goes.
export function CopyForTeamButton({ text }: { text: string }) {
  const [state, setState] = useState<"idle" | "copied" | "failed">("idle");
  return (
    <Tooltip
      content={
        state === "copied"
          ? "Copied"
          : state === "failed"
            ? "Copy failed"
            : "Copies the full report: findings, Radar's evidence excerpts and every step"
      }
      delay={100}
      wrapperClassName="shrink-0"
    >
      <button
        type="button"
        onClick={async () => {
          setState((await copyText(text)) ? "copied" : "failed");
          setTimeout(() => setState("idle"), 1200);
        }}
        className="inline-flex items-center gap-1.5 whitespace-nowrap rounded-md px-2 py-1 text-xs font-medium text-theme-text-secondary hover:bg-theme-hover hover:text-theme-text-primary"
      >
        {state === "copied" ? (
          <Check className="h-3.5 w-3.5 text-emerald-500" aria-hidden />
        ) : (
          <ClipboardCopy className="h-3.5 w-3.5" aria-hidden />
        )}
        {state === "copied" ? "Copied" : state === "failed" ? "Copy failed" : "Copy for the team"}
      </button>
    </Tooltip>
  );
}
