import { useContext } from "react";
import {
  Badge,
  DiffViewer,
  TerminalBlock,
  formatRelativeAgeTime,
  ResourceLink,
  stripAnsi,
} from "@skyhook-io/k8s-ui";
import { apiVersionToGroup } from "../../../../utils/navigation";
import { Tooltip } from "../../../ui/Tooltip";
import { EvidenceNavigationContext } from "../navigation";
import type { EvidenceDataOf } from "../cardParts";

export const VISIBLE_LOG_EVIDENCE_LINES = 12;

export function LogsBody({
  data,
  condensed = false,
}: {
  data: EvidenceDataOf<"logs">;
  condensed?: boolean;
}) {
  const lines = data.logs?.lines ?? [];
  const visibleLines = lines
    .slice(-VISIBLE_LOG_EVIDENCE_LINES)
    .map((line) => stripAnsi(line));
  const omittedLines = lines.length - visibleLines.length;
  return (
    <div className="space-y-2">
      <div className="flex flex-wrap items-center gap-1.5">
        {condensed ? null : (
          <>
            <Badge tone="structural" size="sm">
              {data.pod} / {data.container}
            </Badge>
            <Badge severity="neutral" size="sm">
              {data.previous ? "previous instance" : "current instance"}
            </Badge>
          </>
        )}
        {data.logs?.fallback ? (
          <Badge severity="neutral" size="sm">
            Log tail · filter matched nothing
          </Badge>
        ) : null}
        {data.logs ? (
          <span className="text-xs text-theme-text-tertiary">
            {data.logs.matchedLines} of {data.logs.totalLines} read lines
            matched the filter
          </span>
        ) : null}
      </div>
      {condensed ? (
        omittedLines > 0 ? (
          <p className="text-xs text-theme-text-tertiary">
            Showing the last {visibleLines.length} of {lines.length} lines.
          </p>
        ) : null
      ) : visibleLines.length > 0 ? (
        <TerminalBlock
          label={
            omittedLines > 0
              ? `Selected log excerpt · last ${visibleLines.length} of ${lines.length} lines`
              : "Selected log excerpt"
          }
        >
          {visibleLines.join("\n")}
        </TerminalBlock>
      ) : (
        <p className="text-xs italic text-theme-text-tertiary">
          No lines were captured from this stream.
        </p>
      )}
      {data.error ? (
        <p className="text-xs leading-relaxed text-red-400">{data.error}</p>
      ) : null}
      {data.warnings.map((warning) => (
        <p key={warning} className="text-xs text-warning-text">
          {warning}
        </p>
      ))}
    </div>
  );
}

export function EventsBody({ data }: { data: EvidenceDataOf<"events"> }) {
  return (
    <div>
      <p className="mb-2 text-xs text-theme-text-tertiary">{data.scope}</p>
      <ol className="max-h-[28rem] space-y-0 overflow-y-auto pr-1">
        {data.events.map((event, index) => (
          <li
            key={`${event.reason}-${event.lastTimestamp}-${index}`}
            className="relative flex gap-3 pb-3 last:pb-0"
          >
            {index < data.events.length - 1 ? (
              <span className="absolute bottom-0 left-[5px] top-3 w-px bg-theme-border" />
            ) : null}
            <span className="relative mt-1.5 h-2.5 w-2.5 shrink-0 rounded-full border-2 border-amber-500 bg-theme-surface" />
            <div className="min-w-0 flex-1">
              <div className="flex flex-wrap items-baseline justify-between gap-x-3 gap-y-0.5">
                <span className="text-xs font-semibold text-theme-text-primary">
                  {event.reason}
                  {event.count > 1 ? (
                    <span className="ml-1.5 font-mono text-[11px] font-normal text-theme-text-tertiary">
                      ×{event.count}
                    </span>
                  ) : null}
                </span>
                <Tooltip
                  content={new Date(event.lastTimestamp).toLocaleString()}
                  delay={150}
                  position="left"
                >
                  <time
                    dateTime={event.lastTimestamp}
                    className="text-xs text-theme-text-tertiary"
                  >
                    {formatRelativeAgeTime(event.lastTimestamp)}
                  </time>
                </Tooltip>
              </div>
              <p className="mt-0.5 text-xs leading-relaxed text-theme-text-secondary">
                {event.message}
              </p>
            </div>
          </li>
        ))}
      </ol>
    </div>
  );
}

export function ChangesBody({ data }: { data: EvidenceDataOf<"changes"> }) {
  const { onOpenResource } = useContext(EvidenceNavigationContext);
  return (
    <div className="space-y-2.5">
      {data.changeContext?.evidence ? (
        <p className="text-xs leading-relaxed text-theme-text-secondary [overflow-wrap:anywhere]">
          {data.changeContext.evidence}
        </p>
      ) : null}
      {data.changes.map((change, index) => (
        <div
          key={`${change.kind}-${change.namespace ?? ""}-${change.name}-${change.timestamp}-${index}`}
          className="rounded-md border border-theme-border/70 bg-theme-base/30 p-2.5"
        >
          <div className="flex flex-wrap items-center gap-1.5">
            <Badge tone="structural" size="sm">
              {change.kind}
            </Badge>
            <span className="font-mono text-xs">
              <ResourceLink
                name={change.name}
                kind={change.kind}
                namespace={change.namespace ?? ""}
                group={
                  change.apiVersion
                    ? apiVersionToGroup(change.apiVersion)
                    : undefined
                }
                label={`${change.namespace ? `${change.namespace}/` : ""}${change.name}`}
                onNavigate={
                  change.apiVersion && onOpenResource
                    ? (ref) => onOpenResource(ref)
                    : undefined
                }
              />
            </span>
            <Badge tone="note" size="sm">
              {change.changeType.replaceAll("_", " ")}
            </Badge>
            <Tooltip
              content={new Date(change.timestamp).toLocaleString()}
              delay={150}
              position="left"
              wrapperClassName="ml-auto"
            >
              <time
                dateTime={change.timestamp}
                className="text-xs text-theme-text-tertiary"
              >
                {formatRelativeAgeTime(change.timestamp)}
              </time>
            </Tooltip>
          </div>
          {change.summary ? (
            <p className="mt-1.5 text-xs text-theme-text-secondary">
              {change.summary}
            </p>
          ) : null}
          {change.fields?.length ? (
            <div className="mt-2">
              <DiffViewer
                diff={{
                  summary: `${change.fields.length} changed ${change.fields.length === 1 ? "field" : "fields"}`,
                  fields: change.fields.map((field) => ({
                    path: field.path,
                    oldValue: field.oldValue ?? null,
                    newValue: field.newValue ?? null,
                  })),
                }}
              />
            </div>
          ) : null}
        </div>
      ))}
    </div>
  );
}
