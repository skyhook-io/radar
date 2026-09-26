import { useEffect, useState, useId } from "react";
import {
  CheckCircle2,
  AlertTriangle,
  ShieldCheck,
  Wrench,
  Sparkles,
  RefreshCw,
} from "lucide-react";
import { DialogPortal } from "@skyhook-io/k8s-ui/components/ui/DialogPortal";
import { parseContextName } from "../../utils/context-name";
import type { Diagnosis, ApplyMutationOutcome } from "../../api/diagnose";
import { AIMarkdown, CopyButton } from "./AIMarkdown";

// The Apply confirmation — wider than a generic confirm so the recommended fix
// (rendered markdown) is legible, making it unambiguous what the one click does.
export function ApplyDialog({
  open,
  onClose,
  onConfirm,
  agentLabel,
  resourceLabel,
  context,
  fix,
  reason,
  precondition,
  managedBy,
  confidence,
}: {
  open: boolean;
  onClose: () => void;
  onConfirm: () => void;
  agentLabel: string;
  resourceLabel: string;
  context: string;
  fix?: string;
  /** The agent's one-clause case for this step, repeated at the decision. */
  reason?: string;
  /** The condition the agent attached to this step; shown before the operator confirms. */
  precondition?: string;
  managedBy?: string; // GitOps/Helm owner of the resource, if any
  confidence?: number;
}) {
  const titleId = useId();
  const fixText = fix?.trim();
  const lowConfidence = confidence != null && confidence < 0.5;
  // A GitOps/Helm-managed resource needs an explicit acknowledgment before applying
  // a direct change — it's the canonical footgun (the controller reverts it). Gating
  // (not just warning) makes the user opt into "yes, I know this may be undone."
  // TODO(SKY-1075): once Radar can connect the user's SCM (GitHub/GitLab/…), replace
  //   direct apply on managed resources with "open a PR against the Git source"
  //   instead — the durable fix. See Linear SKY-1075.
  const [acked, setAcked] = useState(false);
  useEffect(() => {
    if (open) setAcked(false);
  }, [open]);
  const applyBlocked = !!managedBy && !acked;
  return (
    <DialogPortal
      ariaLabelledBy={titleId}
      open={open}
      onClose={onClose}
      className="flex max-h-[calc(100dvh-2rem)] w-full max-w-2xl flex-col overflow-hidden"
    >
      <div className="flex shrink-0 items-start gap-3 border-b border-theme-border p-4">
        <div className="flex h-10 w-10 shrink-0 items-center justify-center rounded-full bg-amber-500/20">
          <AlertTriangle className="h-5 w-5 text-amber-500" />
        </div>
        <div className="min-w-0 flex-1">
          <h3 id={titleId} className="text-lg font-semibold text-theme-text-primary">
            Apply this fix?
          </h3>
          <p className="mt-1 text-sm text-theme-text-secondary">
            Let {agentLabel} apply the recommended change to{" "}
            <span className="font-medium text-theme-text-primary">
              {resourceLabel}
            </span>
            .
          </p>
          <p className="mt-2 text-sm text-theme-text-secondary">
            Cluster:{" "}
            <span className="font-medium text-theme-text-primary">
              {parseContextName(context).clusterName}
            </span>
          </p>
          {context !== parseContextName(context).clusterName && (
            <p className="mt-0.5 break-all font-mono text-xs text-theme-text-tertiary">
              {context}
            </p>
          )}
        </div>
      </div>

      <div className="min-h-0 overflow-y-auto">
        {fixText && (
          <div className="border-b border-theme-border p-4">
            <div className="mb-1.5 flex items-center gap-1.5 text-xs font-semibold uppercase tracking-wide text-accent">
              <Sparkles className="h-3.5 w-3.5" />
              Proposed change
            </div>
            <AIMarkdown className="text-sm text-theme-text-primary [overflow-wrap:anywhere] [&_code]:font-normal [&_p]:my-0 [&_p]:text-theme-text-primary [&_pre]:my-1.5 [&_pre]:whitespace-pre-wrap [&_pre_code]:whitespace-pre-wrap">
              {fixText}
            </AIMarkdown>
            {reason ? (
              <p
                data-apply-reason
                className="mt-2 text-sm text-theme-text-secondary"
              >
                <span className="font-medium text-theme-text-primary">
                  Why this step:
                </span>{" "}
                {reason}
              </p>
            ) : null}
            {precondition ? (
              <p
                data-apply-precondition
                className="mt-2 flex items-start gap-1.5 rounded-md border border-amber-500/30 bg-amber-500/5 px-2.5 py-1.5 text-xs text-theme-text-primary"
              >
                <AlertTriangle className="mt-0.5 h-3.5 w-3.5 shrink-0 text-amber-500" />
                <span>
                  <span className="font-medium">
                    The agent said this applies only if:
                  </span>{" "}
                  {precondition}. Radar has not checked that condition.
                </span>
              </p>
            ) : null}
          </div>
        )}
      </div>
      <div className="shrink-0 space-y-2 p-4">
        {/* The star warning: when we KNOW a controller owns this resource, a live
            change reverts on the next reconcile — say so authoritatively. */}
        {managedBy && (
          <div className="space-y-2 rounded border border-amber-500/40 bg-amber-500/10 p-3 text-sm text-theme-text-primary">
            <div className="flex items-start gap-2">
              <RefreshCw className="mt-0.5 h-4 w-4 shrink-0 text-amber-500" />
              <span>
                <span className="font-medium">Managed by {managedBy}.</span>{" "}
                Unless you turn off auto-sync, a direct change here will be
                undone within minutes when {managedBy} re-syncs from Git — the
                durable fix is to change it in Git (the {managedBy} source).
              </span>
            </div>
            <label className="flex cursor-pointer items-center gap-2 pl-6 text-xs text-theme-text-secondary">
              <input
                type="checkbox"
                checked={acked}
                onChange={(e) => setAcked(e.target.checked)}
                className="h-3.5 w-3.5 accent-amber-500"
              />
              I understand {managedBy} may revert this — apply anyway.
            </label>
          </div>
        )}
        {lowConfidence && (
          <div className="flex items-start gap-2 rounded border border-theme-border bg-theme-elevated p-3 text-sm text-theme-text-secondary">
            <AlertTriangle className="mt-0.5 h-4 w-4 shrink-0 text-theme-text-tertiary" />
            <span>
              The agent had <span className="font-medium">low confidence</span>{" "}
              in this conclusion — consider asking a follow-up to verify before
              applying.
            </span>
          </div>
        )}
        <div className="flex items-start gap-2 rounded border border-theme-border bg-theme-base/50 p-3 text-sm text-theme-text-secondary">
          <AlertTriangle className="mt-0.5 h-4 w-4 shrink-0 text-theme-text-tertiary" />
          <span>
            {agentLabel} will change your cluster using your kubeconfig
            credentials. Review the change above; if you&apos;re not sure, ask a
            follow-up first.
          </span>
        </div>
      </div>
      <div className="flex shrink-0 items-center justify-end gap-3 border-t border-theme-border p-4">
        <button
          onClick={onClose}
          className="rounded-lg px-4 py-2 text-sm font-medium text-theme-text-secondary transition-colors hover:bg-theme-elevated hover:text-theme-text-primary"
        >
          Cancel
        </button>
        <button
          onClick={onConfirm}
          disabled={applyBlocked}
          className="flex items-center gap-1.5 rounded-lg btn-brand px-4 py-2 text-sm font-medium disabled:cursor-not-allowed disabled:opacity-50"
        >
          <Wrench className="h-4 w-4" />
          Apply fix
        </button>
      </div>
    </DialogPortal>
  );
}

// The result of an apply turn. Mutation truth comes from Radar write-tool
// results, not the agent's prose or process exit. Missing outcome metadata is
// therefore fail-closed as unknown; only explicit confirmation renders green.
export function ApplyOutcomeCard({
  diagnosis,
  error,
  applyOutcome,
  onCheckStatus,
  animate,
}: {
  diagnosis: Diagnosis | null;
  error?: string | null;
  applyOutcome?: ApplyMutationOutcome;
  onCheckStatus?: () => void;
  animate: boolean;
}) {
  const outcome = diagnosis?.report || diagnosis?.rootCause;
  const status = applyOutcome ?? "unknown";
  const confirmed = status === "confirmed";
  const failed = status === "failed";
  const heading = confirmed
    ? "Applied"
    : failed
      ? "Not applied"
      : "Outcome unknown";
  const detail = error || outcome;
  const fallbackDetail = confirmed
    ? "Radar confirmed that the change was applied. Check the current state to confirm its effect."
    : failed
      ? "Radar could not confirm that the change was applied. Review Activity before retrying."
      : "Radar could not confirm whether the change was applied. Check the current state before retrying.";
  const containerClass = confirmed
    ? "border-emerald-500/30 bg-emerald-500/5"
    : failed
      ? "border-red-500/30 bg-red-500/5"
      : "border-amber-500/40 bg-amber-500/10";
  const accentClass = confirmed
    ? "text-emerald-500"
    : failed
      ? "text-red-400"
      : "text-amber-400";
  const buttonClass = confirmed
    ? "border-emerald-500/40 text-emerald-500 hover:bg-emerald-500/10"
    : "border-amber-500/40 text-amber-400 hover:bg-amber-500/10";
  const provenance = confirmed
    ? error
      ? "Radar confirmed the change — the agent report is incomplete"
      : "Radar confirmed the change was applied"
    : failed
      ? "Radar did not confirm that the change was applied"
      : "Radar cannot confirm whether the change was applied";
  return (
    <div className={`mt-3 space-y-2 ${animate ? "animate-result-in" : ""}`}>
      <div className={`rounded-lg border p-3 ${containerClass}`}>
        <div className="mb-1 flex items-center justify-between gap-2">
          <div
            className={`flex items-center gap-1.5 text-xs font-semibold uppercase tracking-wide ${accentClass}`}
          >
            {confirmed ? (
              <CheckCircle2 className="h-3.5 w-3.5" />
            ) : (
              <AlertTriangle className="h-3.5 w-3.5" />
            )}
            {heading}
          </div>
          {detail && <CopyButton text={detail} label="Copy apply result" />}
        </div>
        <AIMarkdown className="text-sm text-theme-text-primary [overflow-wrap:anywhere] [&_code]:font-normal [&_li]:text-theme-text-primary [&_p]:my-1 [&_p]:text-theme-text-primary [&_p:first-child]:mt-0 [&_p:last-child]:mb-0">
          {detail || fallbackDetail}
        </AIMarkdown>
        {!confirmed && !failed && (
          <div className="mt-2 flex items-start gap-1.5 text-xs font-medium text-amber-300">
            <RefreshCw className="mt-0.5 h-3.5 w-3.5 shrink-0" />
            <span>
              Check the current state before trying to apply this change again.
            </span>
          </div>
        )}
        {onCheckStatus && !failed && (
          <button
            type="button"
            onClick={onCheckStatus}
            className={`mt-3 flex w-full items-center justify-center gap-1.5 rounded-lg border py-2 text-sm font-medium ${buttonClass}`}
          >
            <RefreshCw className="h-4 w-4" />
            Check current status
          </button>
        )}
      </div>
      <div className="flex items-center gap-1 px-0.5 text-[11px] text-theme-text-tertiary">
        <ShieldCheck className="h-3 w-3 shrink-0" />
        <span className="truncate">{provenance}</span>
      </div>
    </div>
  );
}
