import { Collapse } from "@skyhook-io/k8s-ui";
import { Check } from "lucide-react";

export interface ConnectionFeedback {
  message: string;
  tone: "success" | "warning" | "info";
}

export function ConnectionFormActions({
  dirty,
  busy,
  onSave,
  onDiscard,
  feedback,
  error,
}: {
  dirty: boolean;
  busy: boolean;
  onSave: () => void;
  onDiscard: () => void;
  feedback?: ConnectionFeedback;
  error?: string;
}) {
  const success = !dirty && !busy && !error && feedback?.tone === "success";
  const detail =
    !busy && (error || (feedback?.tone !== "success" && feedback?.message));
  return (
    <div className="mt-4 border-t border-theme-border pt-4">
      <div className="flex min-h-8 flex-wrap items-center gap-3">
        <button
          type="button"
          disabled={!dirty || busy}
          onClick={onSave}
          className="btn-brand min-w-28 px-3 py-2 text-xs"
        >
          {busy ? "Saving…" : "Save changes"}
        </button>
        {success && (
          <span
            role="status"
            className="flex items-center gap-1.5 text-xs text-theme-text-secondary"
          >
            <Check
              aria-hidden="true"
              className="h-3.5 w-3.5 text-[var(--color-success-dark)] dark:text-[var(--color-success-light)]"
            />
            {feedback.message}
          </span>
        )}
        {dirty && (
          <>
            <button
              type="button"
              disabled={busy}
              onClick={onDiscard}
              className="text-xs text-theme-text-secondary hover:underline"
            >
              Discard
            </button>
            <span className="text-xs text-theme-text-tertiary">
              Unsaved changes
            </span>
          </>
        )}
      </div>
      <Collapse open={!!detail}>
        <p
          role={error ? "alert" : "status"}
          className={`pt-2 text-xs ${error ? "text-semantic-error" : feedback?.tone === "warning" ? "text-warning-text" : "text-theme-text-secondary"}`}
        >
          {detail}
        </p>
      </Collapse>
    </div>
  );
}
