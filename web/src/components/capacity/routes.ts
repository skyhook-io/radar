export type CapacityTopTab = "overview" | "demand" | "activity";
export type PoolSection = "summary" | "workloads" | "members" | "configuration";

export interface CapacityRoute {
  topTab: CapacityTopTab;
  poolName?: string;
  poolSection: PoolSection;
}

export function parseCapacityRoute(pathname: string): CapacityRoute {
  const segments = pathname.replace(/^\/+|\/+$/g, "").split("/");
  if (segments[0] !== "capacity")
    return { topTab: "overview", poolSection: "summary" };
  if (segments[1] === "pools" && segments[2]) {
    const section = segments[3];
    return {
      topTab: "overview",
      poolName: decodePathSegment(segments[2]),
      poolSection:
        section === "workloads" ||
        section === "members" ||
        section === "configuration"
          ? section
          : "summary",
    };
  }
  if (segments[1] === "demand" || segments[1] === "activity")
    return { topTab: segments[1], poolSection: "summary" };
  return { topTab: "overview", poolSection: "summary" };
}

function decodePathSegment(value: string): string {
  try {
    return decodeURIComponent(value);
  } catch {
    return value;
  }
}

export function capacityPoolPath(name: string): string {
  return `/capacity/pools/${encodeURIComponent(name)}`;
}
