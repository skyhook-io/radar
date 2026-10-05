import { Globe, Network, Cable, Folder, List } from "lucide-react";
import { FactValue, FactSource, type Fact, RefLink } from "@skyhook-io/k8s-ui";
import {
  buildDatumHostnames,
  conditionFact,
  datumCoverage,
  datumLeaseFact,
  datumConnectorConsumers,
  datumList,
  datumObjectFact,
  datumRef,
  type DatumWorkspace,
} from "@skyhook-io/k8s-ui/components/datum/workspace";
import {
  ScreenBody,
  SectionTable,
  Notice,
  Segments,
  FilterChips,
  namespaceChip,
} from "../workspace/layout";
import type { SelectedResource } from "../../types";
import { getDatumStatus } from "@skyhook-io/k8s-ui/components/resources/resource-utils-datum";
import { useWorkspaceNavigate } from "../workspace/useWorkspaceNavigate";
import { currentPageLabel } from "../../utils/page-links";
import {
  datumDetailPath,
  datumDetailKindFor,
  DATUM_SCREENS,
  type DatumScreen,
} from "./routes";
import { DatumProjectConnection } from "./DatumProjectConnection";

export function DatumFactCell({ fact }: { fact: Fact }) {
  return (
    <div>
      <FactValue fact={fact} />
      <div className="line-clamp-2 break-words">
        <FactSource fact={fact} />
      </div>
    </div>
  );
}
const recorded = (v: any, source: string): Fact => ({
  text: v === undefined || v === null || v === "" ? "Not reported" : String(v),
  tone: v === undefined || v === null || v === "" ? "unknown" : "neutral",
  source,
});
const unknown = (text: string, source: string): Fact => ({
  text,
  tone: "unknown",
  source,
});
function attentionFirst(objects: any[]): any[] {
  return [...objects].sort(
    (a, b) =>
      Number(getDatumStatus(b).label === "Attention needed") -
        Number(getDatumStatus(a).label === "Attention needed") ||
      a.metadata.name.localeCompare(b.metadata.name),
  );
}
export function DatumScreens({
  data,
  screen,
  onInspect,
  inspected,
  namespaces,
  onClearNamespaces,
  attentionOnly,
  onAttentionChange,
}: {
  data: DatumWorkspace;
  screen: DatumScreen;
  onInspect: (r: SelectedResource) => void;
  inspected: SelectedResource | null;
  namespaces: string[];
  onClearNamespaces: () => void;
  attentionOnly: boolean;
  onAttentionChange: (value: boolean) => void;
}) {
  const navigate = useWorkspaceNavigate();
  const open = (obj: any) => {
    const ref = datumRef(obj),
      plural = datumDetailKindFor(ref.kind, ref.group);
    if (plural)
      navigate(
        datumDetailPath(
          { plural, namespace: ref.namespace, name: ref.name },
          data.context,
        ),
        { state: { returnLabel: currentPageLabel(), returnCtx: data.context } },
      );
  };
  const resource = (obj: any) => datumRef(obj) as SelectedResource;
  const name = (obj: any) => (
    <div>
      <div className="font-medium text-theme-text-primary">
        {obj.metadata.name}
      </div>
      <div className="text-xs text-theme-text-tertiary">
        {obj.metadata.namespace || "Control-plane scoped"}
      </div>
    </div>
  );
  const openCell = (obj: any) => (
    <button
      className="text-accent-text hover:underline text-xs"
      onClick={(e) => {
        e.stopPropagation();
        open(obj);
      }}
    >
      Open →
    </button>
  );
  const empty = (key: string) =>
    datumCoverage(data, key)
      ? "None observed in this view."
      : `Inventory ${data.coverage[key]?.state === "notInstalled" ? "API is not served" : data.coverage[key]?.state === "denied" ? "access is denied" : data.coverage[key]?.state === "syncing" ? "is still synchronizing or discovery is incomplete" : "could not be read"}; absence is not established.`;
  const partial = Object.entries(data.coverage).filter(
    ([, c]) => !["full", "notInstalled"].includes(c.state),
  );
  const synthetic = Object.values(data.objects)
    .flat()
    .some(
      (o) =>
        o.metadata?.annotations?.["radar.skyhook.io/synthetic-status"] ===
        "true",
    );
  const heading = DATUM_SCREENS.find((s) => s.id === screen)!;
  const icons = {
      hostnames: Globe,
      dns: List,
      connectors: Cable,
      networking: Network,
      projects: Folder,
    },
    Icon = icons[screen];
  return (
    <ScreenBody>
      <div>
        <h1 className="flex items-center gap-2 text-xl font-semibold text-theme-text-primary">
          <Icon className="h-5 w-5 text-theme-text-tertiary" />
          <span className="text-theme-text-tertiary">Datum /</span>{" "}
          {heading.label}
        </h1>
        <p className="text-sm text-theme-text-secondary mt-1">
          {screen === "hostnames"
            ? "Follow each configured hostname from verification to its backend. Controller reports are separate from public reachability."
            : screen === "dns"
              ? "Zone programming, authoritative nameservers and record-level failures."
              : screen === "connectors"
                ? "Reported connector readiness, lease evidence and configured consumers."
                : screen === "projects"
                  ? "Open a project using the parent kubeconfig without saving a new context."
                  : "Networks and their configured contexts, bindings and allocated subnets."}
        </p>
      </div>
      {screen !== "projects" && (
        <FilterChips chips={namespaceChip(namespaces, onClearNamespaces)} />
      )}
      {synthetic && (
        <Notice>
          Synthetic fixture status: the Kubernetes API validates these objects,
          but controllers did not produce the seeded conditions.
        </Notice>
      )}
      {partial.length > 0 && (
        <Notice>
          Not fully read:{" "}
          {partial.map(([key, c]) => `${key} (${c.state})`).join(", ")}. Counts
          over partial inventory are lower bounds; unread values remain unknown.
        </Notice>
      )}
      {screen === "hostnames" && (
        <>
          <Notice>
            DNS resolution, actual delegation, the certificate served to your
            client and HTTP traffic are not tested here. Hostname-to-Domain
            association is inferred by the longest matching domain suffix;
            DNSZone-to-Domain and backend references come from the API.
          </Notice>
          <Segments
            label="Hostname filter"
            value={attentionOnly ? "attention" : "all"}
            options={[
              { id: "all", label: "All hostnames" },
              { id: "attention", label: "Needs attention" },
            ]}
            onChange={(v) => onAttentionChange(v === "attention")}
          />
          <SectionTable
            title="Configured hostnames"
            subtitle="Reported failures first"
            rows={buildDatumHostnames(data).filter(
              (r) => !attentionOnly || !!r.problem,
            )}
            rowKey={(r) => r.key}
            rowResource={(r) => resource(r.object)}
            onInspect={onInspect}
            inspected={inspected}
            minWidth={1300}
            empty={
              attentionOnly
                ? "No reported failures in the inventory read. Unknown stages remain unassessed."
                : empty("httpproxies")
            }
            columns={[
              {
                header: "Hostname",
                width: "18%",
                cell: (r) => (
                  <div>
                    <div className="font-mono text-theme-text-primary break-all">
                      {r.hostname}
                    </div>
                    <div className="text-xs text-theme-text-tertiary">
                      {r.namespace}
                    </div>
                  </div>
                ),
              },
              {
                header: "Verification",
                cell: (r) => <DatumFactCell fact={r.verification} />,
              },
              {
                header: "DNS programming",
                cell: (r) => <DatumFactCell fact={r.dns} />,
              },
              {
                header: "Proxy programming",
                cell: (r) => <DatumFactCell fact={r.proxy} />,
              },
              {
                header: "Backend",
                cell: (r) => <DatumFactCell fact={r.backend} />,
              },
              {
                header: "Certificates",
                cell: (r) => <DatumFactCell fact={r.tls} />,
              },
              {
                header: "Top problem",
                className: "break-words",
                cell: (r) => (
                  <span
                    className={
                      r.problem
                        ? "text-theme-text-primary line-clamp-3"
                        : "text-theme-text-tertiary"
                    }
                  >
                    {r.problem ||
                      "No reported failure; reachability unassessed"}
                  </span>
                ),
              },
              { header: "", width: "5%", cell: (r) => openCell(r.object) },
            ]}
          />
        </>
      )}
      {screen === "dns" && (
        <>
          <SectionTable
            title="Zones"
            rows={attentionFirst(datumList(data, "dnszones"))}
            rowKey={(o) => o.metadata.uid}
            rowResource={resource}
            onInspect={onInspect}
            inspected={inspected}
            empty={empty("dnszones")}
            columns={[
              { header: "Zone", cell: name },
              {
                header: "Domain",
                cell: (o) => (
                  <DatumFactCell
                    fact={recorded(
                      o.spec?.domainName,
                      "DNSZone · spec.domainName",
                    )}
                  />
                ),
              },
              {
                header: "Acceptance",
                cell: (o) => (
                  <DatumFactCell fact={conditionFact(o, "Accepted")} />
                ),
              },
              {
                header: "Programming",
                cell: (o) => (
                  <DatumFactCell fact={conditionFact(o, "Programmed")} />
                ),
              },
              {
                header: "Nameservers",
                cell: (o) => (
                  <DatumFactCell
                    fact={recorded(
                      o.status?.nameservers?.join(", "),
                      "DNSZone · status.nameservers; delegation untested",
                    )}
                  />
                ),
              },
              {
                header: "Records",
                cell: (o) => {
                  const sets = datumList(data, "dnsrecordsets").filter(
                    (r) =>
                      r.metadata.namespace === o.metadata.namespace &&
                      r.spec?.dnsZoneRef?.name === o.metadata.name,
                  );
                  return (
                    <>
                      <DatumFactCell
                        fact={recorded(
                          o.status?.recordCount,
                          "DNSZone · status.recordCount",
                        )}
                      />
                      <DatumFactCell
                        fact={
                          datumCoverage(
                            data,
                            "dnsrecordsets",
                            o.metadata.namespace,
                          )
                            ? recorded(
                                `${sets.reduce((n, r) => n + (r.spec?.records?.length || 0), 0)} declared in ${sets.length} observed sets`,
                                "Radar cache · DNSRecordSet specs",
                              )
                            : unknown(
                                "Record inventory not fully read",
                                "Workspace coverage",
                              )
                        }
                      />
                    </>
                  );
                },
              },
              { header: "", width: "7%", cell: openCell },
            ]}
          />
          <SectionTable
            title="Record sets needing attention or awaiting status"
            rows={attentionFirst(
              datumList(data, "dnsrecordsets").filter(
                (o) => getDatumStatus(o).label !== "Ready",
              ),
            )}
            rowKey={(o) => o.metadata.uid}
            rowResource={resource}
            onInspect={onInspect}
            inspected={inspected}
            empty={empty("dnsrecordsets")}
            columns={[
              { header: "Record set", cell: name },
              {
                header: "Zone",
                cell: (o) => (
                  <RefLink
                    refTo={{
                      kind: "dnszones",
                      group: "dns.networking.miloapis.com",
                      namespace: o.metadata.namespace,
                      name: o.spec.dnsZoneRef.name,
                    }}
                    onNavigate={(ref) => onInspect(ref as SelectedResource)}
                  />
                ),
              },
              { header: "Type", cell: (o) => o.spec.recordType },
              {
                header: "Reported state",
                cell: (o) => <DatumFactCell fact={datumObjectFact(o)} />,
              },
              {
                header: "Record conditions",
                cell: (o) => (
                  <div>
                    {(o.status?.recordSets || []).map((r: any) => (
                      <div key={r.name}>
                        {r.name}:{" "}
                        {(r.conditions || [])
                          .map(
                            (c: any) => `${c.type}=${c.status} (${c.reason})`,
                          )
                          .join(", ")}
                      </div>
                    ))}
                  </div>
                ),
              },
              { header: "", width: "7%", cell: openCell },
            ]}
          />
        </>
      )}
      {screen === "connectors" && (
        <SectionTable
          title="Connectors"
          rows={attentionFirst(datumList(data, "connectors"))}
          rowKey={(o) => o.metadata.uid}
          rowResource={resource}
          onInspect={onInspect}
          inspected={inspected}
          empty={empty("connectors")}
          columns={[
            { header: "Connector", cell: name },
            {
              header: "Readiness",
              cell: (o) => <DatumFactCell fact={conditionFact(o, "Ready")} />,
            },
            {
              header: "Lease",
              cell: (o) => <DatumFactCell fact={datumLeaseFact(data, o)} />,
            },
            {
              header: "Capabilities",
              cell: (o) => (
                <div>
                  {(o.status?.capabilities || []).map((c: any) => (
                    <div key={c.type}>
                      {c.type}:{" "}
                      {(c.conditions || [])
                        .map((v: any) => `${v.type}=${v.status} (${v.reason})`)
                        .join(", ")}
                    </div>
                  ))}
                  {!o.status?.capabilities?.length && (
                    <span className="text-theme-text-tertiary">
                      Not reported
                    </span>
                  )}
                </div>
              ),
            },
            {
              header: "Proxy consumers",
              cell: (o) => {
                const consumers = datumConnectorConsumers(data, o);
                return (
                  <div>
                    {consumers.map((p) => (
                      <div key={p.metadata.uid}>
                        <RefLink
                          refTo={datumRef(p)}
                          onNavigate={(ref) =>
                            onInspect(ref as SelectedResource)
                          }
                        />
                      </div>
                    ))}
                    {consumers.length === 0 &&
                      (datumCoverage(data, "httpproxies", o.metadata.namespace)
                        ? "None observed"
                        : "HTTPProxy inventory not fully read")}
                    <div className="text-xs text-theme-text-tertiary">
                      HTTPProxy · configured connector references
                    </div>
                  </div>
                );
              },
            },
            { header: "", width: "7%", cell: openCell },
          ]}
        />
      )}
      {screen === "networking" && (
        <>
          <SectionTable
            title="Networks"
            rows={attentionFirst(datumList(data, "networks"))}
            rowKey={(o) => o.metadata.uid}
            rowResource={resource}
            onInspect={onInspect}
            inspected={inspected}
            empty={empty("networks")}
            columns={[
              { header: "Network", cell: name },
              {
                header: "Reported state",
                cell: (o) => <DatumFactCell fact={datumObjectFact(o)} />,
              },
              {
                header: "Declared IPAM",
                cell: (o) => (
                  <DatumFactCell
                    fact={recorded(
                      o.spec?.ipam?.mode,
                      "Network · spec.ipam.mode",
                    )}
                  />
                ),
              },
              {
                header: "Assigned IPv6",
                cell: (o) => (
                  <DatumFactCell
                    fact={recorded(
                      o.status?.ipam?.ipv6Prefix,
                      "Network · status.ipam.ipv6Prefix",
                    )}
                  />
                ),
              },
              {
                header: "Contexts / Bindings",
                cell: (o) => {
                  const related = (key: string) =>
                    datumList(data, key).filter(
                      (c) =>
                        c.spec?.network?.name === o.metadata.name &&
                        (c.spec.network.namespace || c.metadata.namespace) ===
                          o.metadata.namespace,
                    );
                  return (
                    <div>
                      {["networkcontexts", "networkbindings"].map((key) => (
                        <DatumFactCell
                          key={key}
                          fact={
                            datumCoverage(data, key, o.metadata.namespace)
                              ? recorded(
                                  `${related(key).length} ${key} observed in this view`,
                                  "Configured Network references observed in the current namespace filter; bindings in other namespaces are not counted",
                                )
                              : unknown(
                                  `${key} not fully read`,
                                  "Workspace coverage",
                                )
                          }
                        />
                      ))}
                    </div>
                  );
                },
              },
              { header: "", width: "7%", cell: openCell },
            ]}
          />
          <SectionTable
            title="Contexts, bindings and subnets"
            rows={attentionFirst(
              [
                "networkcontexts",
                "networkbindings",
                "subnets",
                "subnetclaims",
                "networkservices",
                "instances",
                "workloads",
              ].flatMap((k) => datumList(data, k)),
            )}
            rowKey={(o) => o.metadata.uid}
            rowResource={resource}
            onInspect={onInspect}
            inspected={inspected}
            empty="No network inventory observed; see the coverage note for unread kinds."
            columns={[
              {
                header: "Resource",
                cell: (o) => (
                  <>
                    {name(o)}
                    <div className="text-xs text-theme-text-tertiary">
                      {o.kind}
                    </div>
                  </>
                ),
              },
              {
                header: "Reported state",
                cell: (o) => <DatumFactCell fact={datumObjectFact(o)} />,
              },
              {
                header: "Context / Network",
                cell: (o) =>
                  recorded(
                    o.spec?.networkContext?.name ||
                      o.status?.networkContextRef?.name ||
                      o.spec?.network?.name,
                    `${o.kind} · configured/resolved reference`,
                  ).text,
              },
              {
                header: "Allocation",
                cell: (o) => (
                  <DatumFactCell
                    fact={recorded(
                      o.status?.startAddress
                        ? `${o.status.startAddress}/${o.status.prefixLength}`
                        : o.status?.ipam?.ipv6Prefix,
                      `${o.kind} · status; desired allocation is separate`,
                    )}
                  />
                ),
              },
              { header: "", width: "7%", cell: openCell },
            ]}
          />
        </>
      )}
      {screen === "projects" && (
        <SectionTable
          title="Projects"
          subtitle="Control-plane wide; namespace filter does not apply"
          rows={attentionFirst(datumList(data, "projects"))}
          rowKey={(o) => o.metadata.uid}
          rowResource={resource}
          onInspect={onInspect}
          inspected={inspected}
          empty={empty("projects")}
          minWidth={850}
          columns={[
            { header: "Project", width: "22%", cell: name },
            {
              header: "Description",
              cell: (o) => (
                <DatumFactCell
                  fact={recorded(
                    o.spec?.description,
                    "Project · spec.description",
                  )}
                />
              ),
            },
            {
              header: "Reported state",
              cell: (o) => <DatumFactCell fact={datumObjectFact(o)} />,
            },
            {
              header: "Connection",
              width: "40%",
              cell: (o) => <DatumProjectConnection project={o} />,
            },
            { header: "", width: "6%", cell: openCell },
          ]}
        />
      )}
    </ScreenBody>
  );
}
