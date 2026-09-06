import type { ReactNode } from "react";
import {
  INVESTIGATION_CONFIG_ROW_LIMIT,
  buildInvestigationResourceEvidenceModel,
  type InvestigationResourceEvidenceInput,
  type InvestigationResourceEvidenceModel,
} from "./investigationResourceEvidenceModel";

export function InvestigationResourceEvidence({
  resource,
}: {
  resource: InvestigationResourceEvidenceInput;
}) {
  const model = buildInvestigationResourceEvidenceModel(resource);
  if (!model?.hasDetails) return null;
  switch (model.kind) {
    case "configmap":
      return <ConfigMapEvidence model={model} />;
    case "secret":
      return <SecretEvidence model={model} />;
    case "sealedsecret":
      return <SealedSecretEvidence model={model} />;
  }
}

function ConfigMapEvidence({
  model,
}: {
  model: Extract<InvestigationResourceEvidenceModel, { kind: "configmap" }>;
}) {
  const entries = model.entries;
  const visible = entries.slice(0, INVESTIGATION_CONFIG_ROW_LIMIT);
  const omitted = entries.length - visible.length;

  return (
    <div className="overflow-hidden rounded-md border border-theme-border bg-theme-base/40">
      <div className="flex items-center justify-between gap-3 border-b border-theme-border px-2.5 py-1.5">
        <span className="text-xs font-medium text-theme-text-tertiary">
          ConfigMap values
        </span>
        <span className="text-xs tabular-nums text-theme-text-tertiary">
          {entries.length} {entries.length === 1 ? "key" : "keys"}
        </span>
      </div>
      {visible.length > 0 ? (
        <table className="w-full table-fixed text-left text-xs">
          <caption className="sr-only">
            Selected ConfigMap keys and values
          </caption>
          <colgroup>
            <col className="w-[38%]" />
            <col />
          </colgroup>
          <tbody className="divide-y divide-theme-border/70">
            {visible.map((entry) => {
              return (
                <tr key={`${entry.binary ? "binary" : "data"}-${entry.key}`}>
                  <th
                    scope="row"
                    className="px-2.5 py-1.5 align-top font-mono text-xs font-medium text-theme-text-secondary"
                  >
                    <span className="block truncate">{entry.key}</span>
                  </th>
                  <td className="px-2.5 py-1.5 align-top font-mono text-xs leading-relaxed text-theme-text-primary">
                    {entry.sensitive ? (
                      <span className="font-sans text-theme-text-tertiary">
                        Value hidden · potentially sensitive
                      </span>
                    ) : entry.binary ? (
                      <span className="font-sans text-theme-text-tertiary">
                        Binary value not shown
                      </span>
                    ) : (
                      <span className="block max-h-20 overflow-y-auto whitespace-pre-wrap break-all pr-1">
                        {entry.value}
                      </span>
                    )}
                  </td>
                </tr>
              );
            })}
          </tbody>
        </table>
      ) : (
        <p className="px-2.5 py-2 text-xs text-theme-text-tertiary">
          No data keys were found.
        </p>
      )}
      {omitted > 0 ? (
        <p className="border-t border-theme-border px-2.5 py-1.5 text-xs text-theme-text-tertiary">
          {omitted} more {omitted === 1 ? "key" : "keys"} available in Activity
        </p>
      ) : null}
    </div>
  );
}

function SecretEvidence({
  model,
}: {
  model: Extract<InvestigationResourceEvidenceModel, { kind: "secret" }>;
}) {
  return (
    <div className="rounded-md border border-theme-border bg-theme-base/40 px-2.5 py-2">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <span className="text-xs font-medium text-theme-text-tertiary">
          Secret keys
        </span>
        <span className="text-xs text-theme-text-tertiary">
          {model.secretType}
        </span>
      </div>
      {model.keys.length > 0 ? (
        <div className="mt-1.5 flex flex-wrap gap-1">
          {model.keys.map((key) => (
            <span
              key={key}
              className="rounded border border-theme-border bg-theme-elevated/60 px-1.5 py-0.5 font-mono text-xs text-theme-text-secondary"
            >
              {key}
            </span>
          ))}
        </div>
      ) : (
        <p className="mt-1.5 text-xs text-theme-text-tertiary">
          No Secret key names were found.
        </p>
      )}
      <p className="mt-1.5 text-xs text-theme-text-tertiary">
        Secret values are never shown here.
      </p>
    </div>
  );
}

function SealedSecretEvidence({
  model,
}: {
  model: Extract<InvestigationResourceEvidenceModel, { kind: "sealedsecret" }>;
}) {
  return (
    <div className="overflow-hidden rounded-md border border-theme-border bg-theme-base/40">
      <div className="flex flex-wrap items-center justify-between gap-2 border-b border-theme-border px-2.5 py-1.5">
        <span className="text-xs font-medium text-theme-text-tertiary">
          SealedSecret state
        </span>
        <span
          className={
            model.syncLabel === "Synced"
              ? "text-xs font-medium text-emerald-500"
              : model.syncLabel === "Not synced"
                ? "text-xs font-medium text-red-400"
                : "text-xs font-medium text-theme-text-tertiary"
          }
        >
          {model.syncLabel}
        </span>
      </div>
      {model.conditions.length > 0 ? (
        <div className="px-2.5 py-2">
          <div className="mb-1 text-xs font-medium text-theme-text-tertiary">
            Conditions
          </div>
          <div className="space-y-1.5">
            {model.conditions.map((condition) => (
              <div
                key={`${condition.type}-${condition.status}-${condition.reason ?? ""}`}
                className="text-sm leading-relaxed text-theme-text-primary"
              >
                <span className="font-medium text-theme-text-primary">
                  {condition.type}={condition.status}
                </span>
                {condition.reason ? ` · ${condition.reason}` : ""}
                {condition.message ? ` — ${condition.message}` : ""}
              </div>
            ))}
          </div>
        </div>
      ) : null}
      <dl className="flex flex-wrap gap-x-5 gap-y-1 border-b border-theme-border px-2.5 py-1.5 text-xs">
        <ResourceFact label="Scope" value={model.scope} />
        <ResourceFact
          label="Created"
          value={
            model.created ? (
              <time dateTime={model.created.dateTime}>
                {model.created.label}
              </time>
            ) : undefined
          }
        />
      </dl>
      <div className="px-2.5 py-2">
        <div className="text-xs font-medium text-theme-text-tertiary">
          Encrypted keys · {model.encryptedKeys.length}
        </div>
        {model.encryptedKeys.length > 0 ? (
          <div className="mt-1 flex flex-wrap gap-1">
            {model.encryptedKeys.map((key) => (
              <span
                key={key}
                className="rounded border border-theme-border bg-theme-elevated/60 px-1.5 py-0.5 font-mono text-xs text-theme-text-secondary"
              >
                {key}
              </span>
            ))}
          </div>
        ) : (
          <p className="mt-1 text-xs text-theme-text-tertiary">
            No encrypted key names were found.
          </p>
        )}
      </div>
      <p className="border-t border-theme-border px-2.5 py-1.5 text-xs text-theme-text-tertiary">
        Encrypted values stay hidden. Key names and controller status are shown.
      </p>
    </div>
  );
}

function ResourceFact({ label, value }: { label: string; value: ReactNode }) {
  if (value === undefined || value === null || value === "") return null;
  return (
    <div className="flex min-w-0 gap-1.5">
      <dt className="text-theme-text-tertiary">{label}</dt>
      <dd className="font-medium text-theme-text-secondary">{value}</dd>
    </div>
  );
}
