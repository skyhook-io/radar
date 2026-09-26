#!/usr/bin/env bash
# Refuses a minor or major Radar release (vX.Y.0) that has no What's New entry.
# Patch releases and prereleases pass: a patch shows the newest earlier entry.
#
# Usage: scripts/check-whats-new.sh <version> [git-ref]
#   git-ref  read the catalog as committed at that ref (release.sh passes HEAD,
#            which is what gets tagged); default is the working tree.
set -euo pipefail

version="${1:?usage: scripts/check-whats-new.sh <version> [git-ref]}"
ref="${2:-}"
catalog="web/src/components/whats-new/releaseNotes.ts"
version="v${version#v}"

cd "$(git rev-parse --show-toplevel)"

if [[ ! "$version" =~ ^v([0-9]+)\.([0-9]+)\.([0-9]+)$ ]]; then
  echo "What's New: $version is a prerelease; no entry required."
  exit 0
fi
if [[ "${BASH_REMATCH[3]}" != "0" ]]; then
  echo "What's New: $version is a patch release; no entry required."
  exit 0
fi

if [[ -n "$ref" ]]; then
  content="$(git show "$ref:$catalog")"
else
  content="$(cat "$catalog")"
fi

if grep -Eq "version:[[:space:]]*['\"]${version//./\\.}['\"]" <<<"$content"; then
  echo "What's New: found notes for $version."
  exit 0
fi

cat >&2 <<EOF
What's New: no notes for $version${ref:+ at $ref}.

$version is a minor or major release, and those must tell users what's new.
Add an entry to $catalog before tagging:

  {
    version: '$version',
    releaseUrl: 'https://github.com/skyhook-io/radar/releases/tag/$version',
    highlights: [/* lead highlight first; path + cta open the feature */],
    improvements: [/* short lines for "Also in this release" */],
  }

Preview it with a local build at http://localhost:9280/?whats-new=$version
EOF
exit 1
