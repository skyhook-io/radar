import { FactGrid, FactRow, FactValue, FactSource, type Fact } from "../facts";
import { AlertBanner } from "../ui/drawer-components";
import { datumObservations } from "../resources/resource-utils-datum";
import { SectionHeading } from "../ui/FoldSection";
import { RefLink, type NavigateToRef } from "../ui/RefLink";
import { ProblemList, type WorkspaceProblem } from "../problems";
import { issueReasonTitle } from "../issues/severity";
import {
  buildDatumHostnames,
  conditionFact,
  datumCoverage,
  datumLeaseFact,
  datumConnectorConsumers,
  datumIssuesFor,
  datumList,
  datumObjectFact,
  datumRef,
  type DatumWorkspace,
} from "./workspace";

export function DatumSummary({
  object: obj,
  workspace: ws,
  onNavigate,
}: {
  object: any;
  workspace?: DatumWorkspace;
  onNavigate?: NavigateToRef;
}) {
  const ns = obj.metadata?.namespace || "",
    spec = obj.spec || {},
    status = obj.status || {};
  const fact = (label: string, value: Fact) => (
    <FactRow key={label} label={label}>
      <FactValue fact={value} />
      <FactSource fact={value} />
    </FactRow>
  );
  const recorded = (value: any, source: string): Fact => ({
    text:
      value === undefined || value === null || value === ""
        ? "Not reported"
        : String(value),
    tone:
      value === undefined || value === null || value === ""
        ? "unknown"
        : "neutral",
    source,
  });
  const issues = ws ? datumIssuesFor(ws, obj) : [];
  const problems: WorkspaceProblem[] = issues.map((i) => ({
    id: i.id,
    title: issueReasonTitle(i.reason) || i.reason,
    detail: i.message,
    severity: i.severity,
    category: "reconciliation",
    source: "issue",
    origin: { label: "Controller condition", detail: i.reason },
    subject: {
      kind: i.kind,
      group: i.group || "",
      namespace: i.namespace || "",
      name: i.name,
    },
  }));
  const hostnames = ws
    ? buildDatumHostnames(ws).filter(
        (row) =>
          row.object === obj ||
          row.proxies.some((p) => p.metadata.uid === obj.metadata.uid) ||
          row.domains.some((p) => p.metadata.uid === obj.metadata.uid),
      )
    : [];
  const zoneRecords =
    ws && obj.kind === "DNSZone"
      ? datumList(ws, "dnsrecordsets").filter(
          (r) =>
            r.metadata?.namespace === ns &&
            r.spec?.dnsZoneRef?.name === obj.metadata.name,
        )
      : [];
  const consumers =
    ws && obj.kind === "Connector" ? datumConnectorConsumers(ws, obj) : [];
  return (
    <div className="space-y-5 p-4">
      {obj.metadata?.annotations?.["radar.skyhook.io/synthetic-status"] ===
        "true" && (
        <AlertBanner
          variant="info"
          title="Synthetic fixture status"
          message="These conditions were seeded for UI testing; no controller produced this status."
        />
      )}
      {problems.length > 0 && (
        <section>
          <SectionHeading>Needs attention</SectionHeading>
          <ProblemList
            problems={problems}
            rootKind={obj.kind}
            onNavigate={onNavigate}
          />
        </section>
      )}
      <section>
        <SectionHeading>Reported state</SectionHeading>
        <FactGrid>
          {fact("Reconciliation", datumObjectFact(obj))}
          {obj.kind === "DNSZone" && (
            <>
              {fact("Accepted", conditionFact(obj, "Accepted"))}
              {fact("Programming", conditionFact(obj, "Programmed"))}
              {fact(
                "Nameservers",
                recorded(
                  status.nameservers?.join(", "),
                  "DNSZone · status.nameservers (provisioned; delegation not tested)",
                ),
              )}
              {fact(
                "Reported records",
                recorded(status.recordCount, "DNSZone · status.recordCount"),
              )}
              {fact(
                "Observed record sets",
                ws && datumCoverage(ws, "dnsrecordsets", ns)
                  ? recorded(
                      zoneRecords.length,
                      "Radar cache · DNSRecordSets referencing this zone; a set may hold several records",
                    )
                  : {
                      text: "DNSRecordSets not read",
                      tone: "unknown",
                      source: "Workspace coverage",
                    },
              )}
            </>
          )}
          {obj.kind === "DNSRecordSet" && (
            <>
              {fact("Accepted", conditionFact(obj, "Accepted"))}
              {fact("Programming", conditionFact(obj, "Programmed"))}
              {fact(
                "Declared records",
                recorded(spec.records?.length, "DNSRecordSet · spec.records"),
              )}
              {fact(
                "Record type",
                recorded(spec.recordType, "DNSRecordSet · spec.recordType"),
              )}
            </>
          )}
          {obj.kind === "Domain" && (
            <>
              {fact("Ownership", conditionFact(obj, "Verified"))}
              {fact("Domain validity", conditionFact(obj, "ValidDomain"))}
              {fact(
                "Registrar",
                recorded(
                  status.registration?.registrar,
                  "Domain · status.registration.registrar",
                ),
              )}
              {fact(
                "Nameservers",
                recorded(
                  status.nameservers?.map((s: any) => s.hostname).join(", "),
                  "Domain · status.nameservers",
                ),
              )}
            </>
          )}
          {obj.kind === "HTTPProxy" && (
            <>
              {fact("Acceptance", conditionFact(obj, "Accepted"))}
              {fact("Programming", conditionFact(obj, "Programmed"))}
              {fact("Certificates", conditionFact(obj, "CertificatesReady"))}
              {fact(
                "Addresses",
                recorded(
                  status.addresses?.map((a: any) => a.value).join(", "),
                  "HTTPProxy · status.addresses",
                ),
              )}
              {fact("Public reachability", {
                text: "Not tested from this client",
                tone: "unknown",
                source:
                  "Controller status does not prove DNS resolution, TLS or traffic",
              })}
            </>
          )}
          {obj.kind === "Connector" && (
            <>
              {fact("Readiness", conditionFact(obj, "Ready"))}
              {fact(
                "Capabilities",
                recorded(
                  spec.capabilities?.map((c: any) => c.type).join(", "),
                  "Connector · spec.capabilities (declared)",
                ),
              )}
              {fact(
                "Lease",
                ws
                  ? datumLeaseFact(ws, obj)
                  : {
                      text: "Related lease not read",
                      tone: "unknown",
                      source: "Workspace coverage",
                    },
              )}
            </>
          )}
          {obj.kind === "Project" && (
            <>
              {fact(
                "Display name",
                recorded(spec.displayName, "Project · spec.displayName"),
              )}
              {fact(
                "Description",
                recorded(spec.description, "Project · spec.description"),
              )}
              {fact("Control plane", {
                text: "Verified when opened; runtime connection only",
                tone: "neutral",
                source: "Parent kubeconfig CA/TLS/auth configuration",
              })}
            </>
          )}
        </FactGrid>
      </section>
      {obj.kind === "DNSRecordSet" && spec.dnsZoneRef?.name && (
        <section>
          <SectionHeading>Configured zone</SectionHeading>
          <RefLink
            refTo={{
              kind: "DNSZone",
              group: "dns.networking.miloapis.com",
              namespace: ns,
              name: spec.dnsZoneRef.name,
            }}
            onNavigate={onNavigate}
          >
            DNSZone {spec.dnsZoneRef.name}
          </RefLink>
        </section>
      )}
      {["DNSRecordSet", "Connector"].includes(obj.kind) &&
        datumObservations(obj).some((c) => c.scope) && (
          <section>
            <SectionHeading>Scoped reports</SectionHeading>
            <FactGrid>
              {datumObservations(obj)
                .filter((c) => c.scope)
                .map((c, i) => (
                  <FactRow key={i} label={`${c.scope} · ${c.type}`}>
                    <FactValue fact={conditionFact(obj, c.type, c.scope)} />
                    <FactSource fact={conditionFact(obj, c.type, c.scope)} />
                  </FactRow>
                ))}
            </FactGrid>
          </section>
        )}
      {obj.kind === "Connector" && (
        <section>
          <SectionHeading>Configured proxy consumers</SectionHeading>
          <div className="space-y-2">
            {consumers.map((p) => (
              <div key={p.metadata.uid}>
                <RefLink refTo={datumRef(p)} onNavigate={onNavigate}>
                  HTTPProxy {p.metadata.name}
                </RefLink>
              </div>
            ))}
            {ws && datumCoverage(ws, "httpproxies", ns) ? (
              consumers.length === 0 && (
                <p className="text-xs text-theme-text-tertiary">
                  None observed in this covered namespace.
                </p>
              )
            ) : (
              <p className="text-xs text-theme-text-tertiary">
                Proxy inventory is incomplete; absence is not established.
              </p>
            )}
          </div>
        </section>
      )}
      {hostnames.length > 0 && (
        <section>
          <SectionHeading>Hostname chain</SectionHeading>
          <div className="space-y-4">
            {hostnames.map((row) => (
              <div key={row.key} className="card-inner-lg">
                <div className="text-sm font-medium text-theme-text-primary mb-3">
                  {row.hostname}
                </div>
                <FactGrid>
                  {fact("Verification", row.verification)}
                  {fact("DNS programming", row.dns)}
                  {fact("Proxy programming", row.proxy)}
                  {fact("Backends", row.backend)}
                  {fact("Certificates", row.tls)}
                </FactGrid>
                <div className="flex flex-wrap gap-3 text-xs mt-3">
                  {[
                    ...row.domains,
                    ...row.zones,
                    ...row.records,
                    ...row.proxies,
                    ...row.backends,
                  ].map((o, i) => (
                    <RefLink
                      key={i}
                      refTo={datumRef(o)}
                      onNavigate={onNavigate}
                    >
                      {o.kind} {o.metadata.name}
                    </RefLink>
                  ))}
                </div>
              </div>
            ))}
          </div>
        </section>
      )}
      {zoneRecords.length > 0 && (
        <section>
          <SectionHeading>Observed record sets</SectionHeading>
          <div className="space-y-2">
            {zoneRecords.map((r) => (
              <div
                key={r.metadata.uid}
                className="flex justify-between gap-3 text-sm"
              >
                <RefLink refTo={datumRef(r)} onNavigate={onNavigate}>
                  {r.metadata.name}
                </RefLink>
                <FactValue fact={datumObjectFact(r)} />
              </div>
            ))}
          </div>
        </section>
      )}
      {!ws && (
        <p className="text-xs text-theme-text-tertiary">
          Related inventory is not read. Spec &amp; status shows the resource's
          own report.
        </p>
      )}
    </div>
  );
}
