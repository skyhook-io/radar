import type { Fact, KindCoverage } from "../facts";
import type { ResourceRef } from "../../types/core";
import {
  datumObservations,
  datumConditionTone,
  getDatumStatus,
  isDatumResource,
} from "../resources/resource-utils-datum";

const DNS = "dns.networking.miloapis.com",
  NET = "networking.datumapis.com",
  RM = "resourcemanager.miloapis.com";
export const DATUM_KINDS = {
  dnszones: { kind: "DNSZone", group: DNS, home: "dns" },
  dnsrecordsets: { kind: "DNSRecordSet", group: DNS, home: "dns" },
  dnszoneclasses: {
    kind: "DNSZoneClass",
    group: DNS,
    home: "dns",
    clusterScoped: true,
  },
  domains: { kind: "Domain", group: NET, home: "hostnames" },
  httpproxies: { kind: "HTTPProxy", group: NET, home: "hostnames" },
  connectors: { kind: "Connector", group: NET, home: "connectors" },
  connectorclasses: {
    kind: "ConnectorClass",
    group: NET,
    home: "connectors",
    clusterScoped: true,
  },
  connectoradvertisements: {
    kind: "ConnectorAdvertisement",
    group: NET,
    home: "connectors",
  },
  networks: { kind: "Network", group: NET, home: "networking" },
  networkcontexts: { kind: "NetworkContext", group: NET, home: "networking" },
  networkbindings: { kind: "NetworkBinding", group: NET, home: "networking" },
  subnets: { kind: "Subnet", group: NET, home: "networking" },
  subnetclaims: { kind: "SubnetClaim", group: NET, home: "networking" },
  networkservices: { kind: "NetworkService", group: NET, home: "networking" },
  instances: {
    kind: "Instance",
    group: "compute.datumapis.com",
    home: "networking",
  },
  workloads: {
    kind: "Workload",
    group: "compute.datumapis.com",
    home: "networking",
  },
  projects: {
    kind: "Project",
    group: RM,
    home: "projects",
    clusterScoped: true,
  },
  organizations: {
    kind: "Organization",
    group: RM,
    home: "projects",
    clusterScoped: true,
  },
} as const;
export type DatumKind = keyof typeof DATUM_KINDS;
export interface DatumIssue {
  id: string;
  kind: string;
  group?: string;
  namespace?: string;
  name: string;
  reason: string;
  message?: string;
  severity: "critical" | "warning";
}
export interface DatumWorkspace {
  installed: boolean;
  context: string;
  namespaces: string[] | null;
  coverage: Record<string, KindCoverage>;
  objects: Record<string, any[]>;
  issues: DatumIssue[];
}
export interface HostnameRow {
  key: string;
  hostname: string;
  namespace: string;
  object: any;
  proxies: any[];
  domains: any[];
  zones: any[];
  records: any[];
  backends: any[];
  verification: Fact;
  dns: Fact;
  proxy: Fact;
  backend: Fact;
  tls: Fact;
  problem?: string;
}
export function datumRef(obj: any): ResourceRef {
  const plural = Object.keys(DATUM_KINDS).find(
    (k) =>
      DATUM_KINDS[k as DatumKind].kind === obj.kind &&
      obj.apiVersion?.split("/")[0] === DATUM_KINDS[k as DatumKind].group,
  );
  return {
    kind: plural || obj.kind,
    group: obj.apiVersion?.includes("/") ? obj.apiVersion.split("/")[0] : "",
    namespace: obj.metadata?.namespace || "",
    name: obj.metadata?.name || "",
  };
}
export function datumList(ws: DatumWorkspace, kind: string): any[] {
  return ws.objects[kind] || [];
}
export function datumCoverage(
  ws: DatumWorkspace,
  kind: string,
  ns?: string,
): boolean {
  const c = ws.coverage[kind];
  return (
    c?.state === "full" ||
    (!!ns && c?.state === "partial" && !!c.allowedNamespaces?.includes(ns))
  );
}
export function conditionFact(obj: any, type: string, scope?: string): Fact {
  const scopedField =
    obj.kind === "DNSRecordSet"
      ? "recordSets"
      : obj.kind === "Connector"
        ? "capabilities"
        : "hostnameStatuses";
  const source = `${obj.kind} ${obj.metadata?.name} · status.${scope ? `${scopedField}[${scope}].` : ""}conditions[${type}]`;
  const c = datumObservations(obj).find(
    (c) =>
      c.type === type && (scope === undefined ? !c.scope : c.scope === scope),
  );
  if (!c) return { text: "Not reported", tone: "unknown", source };
  if (
    c.observedGeneration !== undefined &&
    c.observedGeneration < (obj.metadata?.generation || 0)
  )
    return {
      text: "Stale; waiting for current generation",
      tone: "unknown",
      source,
      detail: c.message,
    };
  const tone = datumConditionTone(c, obj.metadata?.generation || 0);
  return {
    text: c.status === "True" ? type : c.reason || c.status,
    tone,
    source,
    detail: c.message,
  };
}
export function datumCombined(facts: Fact[], empty: string): Fact {
  if (!facts.length) return { text: empty, tone: "unknown" };
  const failed = facts.find((f) =>
    ["unhealthy", "alert", "degraded"].includes(f.tone),
  );
  const unknown = facts.find((f) => f.tone === "unknown");
  const pending = facts.find((f) => f.tone === "neutral");
  const chosen = failed || unknown || pending || facts[0];
  return {
    ...chosen,
    text: chosen.text,
    source: chosen.source,
    detail: facts
      .map((f) => [f.source, f.text, f.detail].filter(Boolean).join(": "))
      .join("; "),
  };
}
const hostname = (value: string) => value.toLowerCase().replace(/\.$/, "");
const sameNamespace = (a: any, b: any) =>
  a.metadata?.namespace === b.metadata?.namespace;
export function buildDatumHostnames(ws: DatumWorkspace): HostnameRow[] {
  const rows = new Map<string, HostnameRow>();
  for (const obj of [
    ...datumList(ws, "httpproxies"),
    ...datumList(ws, "domains"),
  ]) {
    const names: string[] =
      obj.kind === "HTTPProxy"
        ? obj.spec?.hostnames || []
        : obj.spec?.domainName
          ? [obj.spec.domainName]
          : [];
    for (const name of names) {
      const host = hostname(name),
        ns = obj.metadata?.namespace || "",
        key = `${ns}/${host}`;
      let row = rows.get(key);
      if (!row) {
        row = {
          key,
          hostname: host,
          namespace: ns,
          object: obj,
          proxies: [],
          domains: [],
          zones: [],
          records: [],
          backends: [],
          verification: { text: "Not reported", tone: "unknown" },
          dns: { text: "Not reported", tone: "unknown" },
          proxy: { text: "Not configured", tone: "unknown" },
          backend: { text: "Unassessed", tone: "unknown" },
          tls: { text: "Not reported", tone: "unknown" },
        };
        rows.set(key, row);
      }
      if (obj.kind === "HTTPProxy") row.proxies.push(obj);
    }
  }
  for (const row of rows.values()) {
    const matches = datumList(ws, "domains").filter(
      (d) =>
        d.metadata?.namespace === row.namespace &&
        ((d.spec?.domainName && row.hostname === hostname(d.spec.domainName)) ||
          row.hostname.endsWith(`.${hostname(d.spec?.domainName || "")}`)),
    );
    const longest = Math.max(
      0,
      ...matches.map((d) => (d.spec?.domainName || "").length),
    );
    row.domains = matches.filter((d) => d.spec.domainName.length === longest);
    row.zones = datumList(ws, "dnszones").filter(
      (z) =>
        z.metadata?.namespace === row.namespace &&
        row.domains.some((d) => z.status?.domainRef?.name === d.metadata.name),
    );
    row.records = datumList(ws, "dnsrecordsets").filter((r) =>
      row.zones.some(
        (z) =>
          sameNamespace(r, z) &&
          r.spec?.dnsZoneRef?.name === z.metadata.name &&
          (r.spec?.records || []).some((record: any) => {
            const raw = hostname(record.name || "");
            const fqdn =
              raw === "@"
                ? hostname(z.spec?.domainName || "")
                : raw.includes(".") &&
                    (raw === hostname(z.spec?.domainName || "") ||
                      raw.endsWith(`.${hostname(z.spec?.domainName || "")}`))
                  ? raw
                  : `${raw}.${hostname(z.spec?.domainName || "")}`;
            return fqdn === row.hostname;
          }),
      ),
    );
    row.verification = datumCombined(
      [
        ...row.domains.map((d) => ({
          ...conditionFact(d, "Verified"),
          source: `${conditionFact(d, "Verified").source} · inferred hostname association`,
        })),
        ...row.proxies
          .filter((p) =>
            datumObservations(p).some(
              (c) => c.scope === row.hostname && c.type === "Verified",
            ),
          )
          .map((p) => conditionFact(p, "Verified", row.hostname)),
      ],
      datumCoverage(ws, "domains", row.namespace)
        ? "No associated Domain observed"
        : "Domains not read",
    );
    row.dns = datumCombined(
      [
        ...row.zones.map((z) => conditionFact(z, "Programmed")),
        ...row.records.flatMap((r) => [
          conditionFact(r, "Programmed"),
          ...datumObservations(r)
            .filter((c) => c.scope && c.type === "RecordProgrammed")
            .map((c) => conditionFact(r, "RecordProgrammed", c.scope)),
        ]),
        ...row.proxies
          .filter((p) =>
            datumObservations(p).some(
              (c) =>
                c.scope === row.hostname && c.type === "DNSRecordProgrammed",
            ),
          )
          .map((p) => conditionFact(p, "DNSRecordProgrammed", row.hostname)),
        ...(!row.records.length && row.zones.length
          ? [
              {
                text: datumCoverage(ws, "dnsrecordsets", row.namespace)
                  ? "No DNSRecordSet for this hostname"
                  : "DNSRecordSet inventory unread",
                tone: "unknown" as const,
                source: "DNSRecordSet inventory",
              },
            ]
          : []),
      ],
      datumCoverage(ws, "dnszones", row.namespace) &&
        datumCoverage(ws, "dnsrecordsets", row.namespace)
        ? "No associated DNS configuration observed"
        : "DNS inventory not read",
    );
    row.proxy = datumCombined(
      row.proxies.flatMap((p) => [
        conditionFact(p, "Programmed"),
        conditionFact(p, "Available", row.hostname),
      ]),
      datumCoverage(ws, "httpproxies", row.namespace)
        ? "No proxy declares this hostname"
        : "HTTPProxies not read",
    );
    row.tls = datumCombined(
      row.proxies.map((p) =>
        conditionFact(p, "CertificateReady", row.hostname),
      ),
      "Certificate readiness not reported",
    );
    const backendFacts: Fact[] = [];
    for (const p of row.proxies)
      for (const rule of p.spec?.rules || [])
        for (const backend of rule.backends || []) {
          if (backend.connector) {
            const c = datumList(ws, "connectors").find(
              (c) =>
                sameNamespace(c, p) &&
                c.metadata.name === backend.connector.name,
            );
            if (c) {
              row.backends.push(c);
              backendFacts.push(conditionFact(c, "Ready"));
            } else
              backendFacts.push({
                text: datumCoverage(ws, "connectors", row.namespace)
                  ? "Referenced Connector not observed"
                  : "Connectors not read",
                tone: "unknown",
                source: `HTTPProxy ${p.metadata.name} · spec.rules.backends.connector`,
              });
          } else if (backend.instance) {
            const s = datumList(ws, "endpointslices").find(
              (s) =>
                sameNamespace(s, p) &&
                s.metadata.name === backend.instance.name,
            );
            if (s) {
              row.backends.push(s);
              const endpoints = s.endpoints || [];
              const ready = endpoints.filter(
                (e: any) => e.conditions?.ready === true,
              ).length;
              backendFacts.push({
                text: `${ready} ready of ${endpoints.length} endpoints`,
                tone:
                  ready > 0
                    ? "healthy"
                    : endpoints.some(
                          (e: any) => e.conditions?.ready === undefined,
                        )
                      ? "unknown"
                      : "degraded",
                source: `EndpointSlice ${s.metadata.name} · endpoints.conditions.ready`,
              });
            } else
              backendFacts.push({
                text: "EndpointSlice not observed",
                tone: "unknown",
                source: "HTTPProxy · instance backend names an EndpointSlice",
              });
          } else if (backend.networkService) {
            const s = datumList(ws, "networkservices").find(
              (s) =>
                sameNamespace(s, p) &&
                s.metadata.name === backend.networkService.name,
            );
            if (s) {
              row.backends.push(s);
              backendFacts.push(conditionFact(s, "Ready"));
            } else
              backendFacts.push({
                text: "NetworkService not observed",
                tone: "unknown",
              });
          } else
            backendFacts.push({
              text: "Endpoint reachability unassessed",
              tone: "unknown",
              source: `HTTPProxy ${p.metadata.name} · configured endpoint`,
            });
        }
    row.backend = datumCombined(backendFacts, "No backend observed");
    const failed = [
      row.verification,
      row.dns,
      row.proxy,
      row.backend,
      row.tls,
    ].find((f) => ["degraded", "alert", "unhealthy"].includes(f.tone));
    row.problem = failed?.text;
  }
  return [...rows.values()].sort(
    (a, b) =>
      Number(!!b.problem) - Number(!!a.problem) ||
      a.hostname.localeCompare(b.hostname) ||
      a.namespace.localeCompare(b.namespace),
  );
}
export function datumObjectFact(obj: any): Fact {
  const s = getDatumStatus(obj);
  return {
    text: s.label,
    tone: s.color,
    source: `${obj.kind} · current reported conditions`,
  };
}
export function datumIssuesFor(ws: DatumWorkspace, obj: any): DatumIssue[] {
  return ws.issues.filter(
    (i) =>
      i.group === datumRef(obj).group &&
      i.kind === obj.kind &&
      (i.namespace || "") === (obj.metadata?.namespace || "") &&
      i.name === obj.metadata?.name,
  );
}
export function isDatumSummary(obj: any): boolean {
  return isDatumResource(obj);
}

export function datumLeaseFact(
  ws: DatumWorkspace,
  connector: any,
  now = Date.now(),
): Fact {
  const source = "Connector · status.leaseRef";
  const name = connector.status?.leaseRef?.name;
  if (!name) return { text: "Lease not reported", tone: "unknown", source };
  const lease = datumList(ws, "leases").find(
    (l) => sameNamespace(l, connector) && l.metadata.name === name,
  );
  if (!lease)
    return {
      text: datumCoverage(ws, "leases", connector.metadata.namespace)
        ? "Referenced lease not observed"
        : "Leases not read",
      tone: "unknown",
      source,
    };
  const renew = lease.spec?.renewTime,
    duration = lease.spec?.leaseDurationSeconds;
  const expires =
    renew && duration !== undefined ? Date.parse(renew) + duration * 1000 : NaN;
  if (!Number.isFinite(expires))
    return {
      text: "Lease expiry not reported",
      tone: "unknown",
      source: "Lease · spec",
    };
  return {
    text: expires < now ? "Lease expired" : "Lease not expired",
    tone: expires < now ? "degraded" : "neutral",
    source: `Lease ${name} · spec.renewTime + spec.leaseDurationSeconds`,
    detail: `Holder: ${lease.spec?.holderIdentity || "not reported"}; renewal is reported evidence, not a continuously observed heartbeat`,
  };
}
export function datumConnectorConsumers(
  ws: DatumWorkspace,
  connector: any,
): any[] {
  return datumList(ws, "httpproxies").filter(
    (p) =>
      sameNamespace(p, connector) &&
      p.spec?.rules?.some((r: any) =>
        r.backends?.some(
          (b: any) => b.connector?.name === connector.metadata.name,
        ),
      ),
  );
}
