// Groups worth offering an admin to bind. Radar Cloud forwards its own
// vocabulary: only the IdP, user and email groups identify this person;
// radar:<role> and radar:org:<id> would grant every member of the org, and
// cloud:* are legacy duplicates. Outside Cloud every group is the operator's.
export function bindableGroups(groups: readonly string[]): string[] {
  const radar = groups.filter((g) => g.startsWith('radar:') || g.startsWith('cloud:'))
  if (radar.length === 0) return [...groups]
  return groups.filter(
    (g) => g.startsWith('radar:idp:') || g.startsWith('radar:user:') || g.startsWith('radar:email:'),
  )
}
