import {
  cnpgFormatBytes,
  FactGrid,
  FactRow,
  formatAge,
  formatGrant,
  toneTextClass,
} from "@skyhook-io/k8s-ui";
import {
  useCNPGRestoreChecks,
  type CNPGRestoreChecksResponse,
} from "../../../api/cnpg-inspect";
import { cnpgDeclaredTargetText, cnpgRecoveryStopText } from "../inspectModel";

const LIST_SHOWN = 6;

function countList(names: string[], total: number): string {
  const shown = names.slice(0, LIST_SHOWN).join(", ");
  return total > LIST_SHOWN ? `${shown} and ${total - LIST_SHOWN} more` : shown;
}

function approxRows(n: number): string {
  if (n >= 1_000_000) return `about ${(n / 1_000_000).toFixed(1)} M rows`;
  if (n >= 1_000) return `about ${Math.round(n / 1_000)} k rows`;
  return `about ${n} rows`;
}

/**
 * What Radar reads inside a restored cluster's primary: where recovery
 * stopped beside the declared target, and what the databases hold. These
 * describe what is there; whether it is what the restore needed is still the
 * person's call, recorded separately as the validation note.
 */
export function CNPGRestoreChecks({
  namespace,
  name,
}: {
  namespace: string;
  name: string;
}) {
  const q = useCNPGRestoreChecks(namespace, name);
  const d = q.data;
  return (
    <div className="border-b border-theme-border px-4 py-3 text-sm">
      <div className="flex flex-wrap items-baseline gap-x-2">
        <h3 className="text-xs font-semibold uppercase tracking-wide text-theme-text-tertiary">
          What Radar reads in it
        </h3>
        {d?.pod && d.state === "ok" && (
          <span className="text-xs text-theme-text-tertiary">
            {d.pod} ·{" "}
            {d.capturedAt ? `${formatAge(d.capturedAt)} ago` : "just now"} ·
            fixed read-only SQL over your pods/exec
          </span>
        )}
        <button
          type="button"
          onClick={() => q.refetch()}
          disabled={q.isFetching}
          className="ml-auto text-xs text-accent-text hover:underline disabled:opacity-50"
        >
          {q.isFetching ? "Reading…" : "Read again"}
        </button>
      </div>
      <div className="mt-2">
        {!d ? (
          <p className="text-theme-text-tertiary">
            {q.isLoading
              ? "Reading the restored primary…"
              : `Could not read it: ${q.error instanceof Error ? q.error.message : "unknown error"}`}
          </p>
        ) : d.state === "denied" ? (
          <p className="text-theme-text-secondary">
            Reading inside the restored databases needs{" "}
            {formatGrant(d.permission.grant) ??
              `create pods/exec in namespace ${namespace}`}
            .
          </p>
        ) : d.state !== "ok" ? (
          <p className={toneTextClass("unknown")}>
            Could not read the primary{d.pod ? ` ${d.pod}` : ""}:{" "}
            {d.error ?? d.state}.
          </p>
        ) : (
          <RestoreFacts d={d} />
        )}
      </div>
    </div>
  );
}

function RestoreFacts({ d }: { d: CNPGRestoreChecksResponse }) {
  const history = [...(d.history ?? [])].reverse();
  return (
    <>
      <FactGrid>
        <FactRow label="State">
          {d.inRecovery ? (
            <span className={toneTextClass("degraded")}>
              Still in recovery: it does not accept writes yet
            </span>
          ) : (
            <span className="text-theme-text-primary">
              Out of recovery, on timeline {d.timeline}
            </span>
          )}
        </FactRow>
        <FactRow label="Where recovery stopped">
          {d.historyMissing ? (
            <span className="text-theme-text-tertiary">
              No timeline history: timeline {d.timeline} never switched
            </span>
          ) : history.length === 0 ? (
            <span className="text-theme-text-tertiary">
              The history file lists no switches
            </span>
          ) : (
            <ul className="space-y-0.5">
              {history.slice(0, 4).map((sw) => (
                <li key={`${sw.from}-${sw.switchLsn}`}>
                  <span className="font-mono text-xs text-theme-text-tertiary">
                    timeline {sw.from} → {sw.to} at {sw.switchLsn}
                  </span>{" "}
                  <span className="text-theme-text-primary">
                    {cnpgRecoveryStopText(sw)}
                  </span>
                </li>
              ))}
            </ul>
          )}
          <div className="mt-0.5 text-[11.5px] text-theme-text-tertiary">
            Declared target: {cnpgDeclaredTargetText(d.target)}. From the
            current timeline’s history file, newest first; later switches are
            failovers or switchovers since the restore.
          </div>
        </FactRow>
        <FactRow label="Databases">
          {d.databases && d.databases.length > 0 ? (
            <span>
              {countList(
                d.databases.map((x) => `${x.name} ${cnpgFormatBytes(x.bytes)}`),
                d.databaseCount ?? d.databases.length,
              )}
            </span>
          ) : (
            <span className="text-theme-text-tertiary">
              None besides the templates
            </span>
          )}
        </FactRow>
        <FactRow label="Roles">
          {d.roles && d.roles.length > 0 ? (
            <span>
              {countList(
                d.roles.map((r) => r.name),
                d.roleCount ?? d.roles.length,
              )}
            </span>
          ) : (
            <span className="text-theme-text-tertiary">None</span>
          )}
        </FactRow>
        <FactRow label={<span className="font-mono">{d.database}</span>}>
          {d.contents ? (
            <>
              <span className="text-theme-text-primary">
                {d.contents.tables}{" "}
                {d.contents.tables === 1 ? "table" : "tables"} ·{" "}
                {approxRows(d.contents.estimatedRows)}
              </span>
              {d.contents.largest.length > 0 && (
                <div className="mt-0.5 text-xs text-theme-text-secondary">
                  Largest:{" "}
                  {d.contents.largest
                    .slice(0, 3)
                    .map(
                      (t) =>
                        `${t.name} ${cnpgFormatBytes(t.bytes)}${t.estimatedRows !== undefined ? ` (${approxRows(t.estimatedRows).replace("about ", "~")})` : ""}`,
                    )
                    .join(" · ")}
                </div>
              )}
              <div className="mt-0.5 text-[11.5px] text-theme-text-tertiary">
                Row counts are the planner’s estimates, which can be stale
                {d.contents.neverAnalyzed > 0
                  ? `; ${d.contents.neverAnalyzed} never analyzed, so not counted`
                  : ""}
                .
              </div>
            </>
          ) : (
            <span className="text-theme-text-tertiary">
              Not read: {d.contentsSource?.error ?? "unknown reason"}
            </span>
          )}
        </FactRow>
      </FactGrid>
      <p className="mt-2 text-[11.5px] text-theme-text-tertiary">
        This describes what is there, not whether it is what you expected. The
        bootstrap database and owner exist whether or not the backup held them:
        CloudNativePG creates them when missing.
      </p>
    </>
  );
}
