import { useState } from "react";
import { AlertTriangle, Copy, Check } from "lucide-react";
import { Markdown } from "../ui/Markdown";
import { Tooltip } from "../ui/Tooltip";

export function CopyButton({ text, label }: { text: string; label: string }) {
  const [state, setState] = useState<"idle" | "copied" | "failed">("idle");
  const copied = state === "copied";
  return (
    <Tooltip
      content={
        state === "copied"
          ? "Copied"
          : state === "failed"
            ? "Copy failed"
            : label
      }
      delay={100}
      wrapperClassName="shrink-0"
    >
      <button
        onClick={async () => {
          // Report what happened, not what was attempted: the write can be
          // denied or unavailable, and "Copied" over an empty clipboard is worse
          // than no button.
          try {
            if (!navigator.clipboard) throw new Error("clipboard unavailable");
            await navigator.clipboard.writeText(text);
            setState("copied");
          } catch {
            setState("failed");
          }
          setTimeout(() => setState("idle"), 1200);
        }}
        className="shrink-0 rounded p-1 text-theme-text-tertiary hover:bg-theme-hover hover:text-theme-text-primary"
        aria-label={
          state === "copied"
            ? `${label} — copied`
            : state === "failed"
              ? `${label} — copy failed`
              : label
        }
        aria-live="polite"
      >
        {copied ? (
          <Check className="h-3.5 w-3.5 text-emerald-400" />
        ) : state === "failed" ? (
          <AlertTriangle className="h-3.5 w-3.5 text-amber-500" />
        ) : (
          <Copy className="h-3.5 w-3.5" />
        )}
      </button>
    </Tooltip>
  );
}

// LLMs occasionally open a ```fence mid-line ("run this: ```bash kubectl …") or
// put the command on the same line as the ```lang marker. GFM then won't parse
// it as a fence — it leaks the literal ``` and renders an empty code box. Coerce
// fence markers onto their own lines and push trailing content off the opener so
// the block renders. (Well-formed markdown is unaffected.)
function tidyFences(md: string): string {
  if (!md || !md.includes("```")) return md;
  return md
    .replace(/([^\n])```/g, "$1\n\n```") // opener/closer must start a line
    .replace(/```([A-Za-z0-9_-]*)[ \t]+(\S)/g, "```$1\n$2"); // content off the opener line
}

// Diagnosis output is dense with inline `code`; the shared chip's brand tint is
// too loud at that density, so neutralize it (border/bg only) for this surface.
const SOFT_INLINE_CODE =
  "[&_.inline-code]:border-theme-border/60 [&_.inline-code]:bg-theme-base [&_.inline-code]:font-normal";

// Markdown for agent-generated text — normalizes flaky fences + softens code.
export function AIMarkdown({
  className,
  children,
}: {
  className?: string;
  children: string;
}) {
  return (
    <Markdown className={`${SOFT_INLINE_CODE} ${className ?? ""}`}>
      {tidyFences(children)}
    </Markdown>
  );
}
