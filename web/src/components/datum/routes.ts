import {
  DATUM_KINDS,
  type DatumKind,
} from "@skyhook-io/k8s-ui/components/datum/workspace";
export type DatumScreen =
  "hostnames" | "dns" | "connectors" | "networking" | "projects";
export const DATUM_SCREENS: { id: DatumScreen; label: string; path: string }[] =
  [
    { id: "hostnames", label: "Hostnames", path: "/datum" },
    { id: "dns", label: "DNS", path: "/datum/dns" },
    { id: "connectors", label: "Connectors", path: "/datum/connectors" },
    { id: "networking", label: "Networking", path: "/datum/networking" },
    { id: "projects", label: "Projects", path: "/datum/projects" },
  ];
export interface DatumTarget {
  plural: DatumKind;
  namespace: string;
  name: string;
  group: string;
}
export function datumDetailKindFor(
  kind: string,
  group: string | undefined,
): DatumKind | null {
  return (
    (Object.keys(DATUM_KINDS) as DatumKind[]).find(
      (k) =>
        (k === kind.toLowerCase() ||
          DATUM_KINDS[k].kind.toLowerCase() === kind.toLowerCase()) &&
        DATUM_KINDS[k].group === group,
    ) || null
  );
}
export function datumDetailPath(
  target: { plural: string; namespace: string; name: string },
  ctx?: string,
  tab?: string,
): string {
  const p = new URLSearchParams();
  if (ctx) p.set("ctx", ctx);
  if (tab) p.set("tab", tab);
  return `/datum/${target.plural}/${encodeURIComponent(target.namespace || "_")}/${encodeURIComponent(target.name)}${p.size ? `?${p}` : ""}`;
}
export function parseDatumRoute(path: string): {
  screen: DatumScreen;
  detail?: DatumTarget;
} {
  const segments = path
    .split("/")
    .filter(Boolean)
    .map((s) => {
      try {
        return decodeURIComponent(s);
      } catch {
        return s;
      }
    });
  const plural = segments[1] as DatumKind;
  if (DATUM_KINDS[plural] && segments[2] && segments[3])
    return {
      screen: DATUM_KINDS[plural].home,
      detail: {
        plural,
        group: DATUM_KINDS[plural].group,
        namespace: segments[2] === "_" ? "" : segments[2],
        name: segments[3],
      },
    };
  return {
    screen: DATUM_SCREENS.find((s) => s.path === path)?.id || "hostnames",
  };
}
