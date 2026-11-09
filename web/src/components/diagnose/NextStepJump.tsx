import { useLayoutEffect, useRef, useState } from "react";
import type { Diagnosis } from "../../api/diagnose";
import { Tooltip } from "../ui/Tooltip";
import { remediationHeadline } from "./AssessmentCard";

/** The first sentence of the step Apply would perform, or of the first step. */
export function recommendedStepHeadline(
  diagnosis: Diagnosis | null | undefined,
): string | undefined {
  if (!diagnosis) return undefined;
  const index = diagnosis.recommendedIndex ? diagnosis.recommendedIndex - 1 : 0;
  const text =
    diagnosis.steps?.[index]?.text ??
    diagnosis.remediation?.[index] ??
    diagnosis.remediation?.[0];
  return text ? remediationHeadline(text) || undefined : undefined;
}

/**
 * The assessment's jump link to Next steps. It names the decision it leads
 * to in one whole sentence; the clamp keeps a long sentence from growing the
 * card, and the full sentence is on hover only when the clamp hid some of it.
 */
export function NextStepJump({
  label,
  headline,
  onClick,
}: {
  label?: string;
  headline?: string;
  onClick: () => void;
}) {
  const textRef = useRef<HTMLSpanElement>(null);
  const [clamped, setClamped] = useState(false);
  useLayoutEffect(() => {
    const el = textRef.current;
    if (!el) return;
    const measure = () =>
      setClamped(
        el.scrollHeight > el.clientHeight + 1 ||
          el.scrollWidth > el.clientWidth + 1,
      );
    measure();
    if (typeof ResizeObserver === "undefined") return;
    const observer = new ResizeObserver(measure);
    observer.observe(el);
    return () => observer.disconnect();
  }, [headline, label]);

  const text = label ?? (headline ? `Next: ${headline}` : "Next steps");
  return (
    <Tooltip
      content={headline}
      disabled={!clamped || !headline || label !== undefined}
      preserveWrapperWhenDisabled
      wrapperClassName="block"
    >
      <button
        type="button"
        onClick={onClick}
        className="flex max-w-md items-start gap-1 rounded-md px-2 py-1 text-left text-xs font-medium text-accent-text hover:bg-theme-hover"
      >
        <span
          ref={textRef}
          className="line-clamp-2 min-w-0 [overflow-wrap:anywhere]"
        >
          {text}
        </span>
        <span aria-hidden="true" className="shrink-0">
          ↓
        </span>
      </button>
    </Tooltip>
  );
}
