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
catalog="${WHATS_NEW_CATALOG:-web/src/components/whats-new/releaseNotes.ts}"
version="v${version#v}"

cd "$(git rev-parse --show-toplevel)"

semver='^v([0-9]+)\.([0-9]+)\.([0-9]+)(-[0-9A-Za-z.-]+)?(\+[0-9A-Za-z.-]+)?$'
if [[ ! "$version" =~ $semver ]]; then
  echo "What's New: $version is not a release version (expected vX.Y.Z)." >&2
  exit 1
fi
if [[ -n "${BASH_REMATCH[4]}" ]]; then
  echo "What's New: $version is a prerelease; no entry required."
  exit 0
fi
if [[ "${BASH_REMATCH[3]}" != "0" ]]; then
  echo "What's New: $version is a patch release; no entry required."
  exit 0
fi
version="v${BASH_REMATCH[1]}.${BASH_REMATCH[2]}.0"

if [[ -n "$ref" ]]; then
  content="$(git show "$ref:$catalog")"
else
  content="$(cat "$catalog")"
fi

# Only real entries count: comments are dropped (a // right after ':' is part
# of a URL), a value on the line after its key is joined back, and only the
# RELEASE_NOTES array is searched, for version as an object key.
entries="$(perl -0pe 's{/\*.*?\*/}{}gs; s{(^|[^:])//[^\n]*}{$1}g; s{\bversion:\s*\n\s*}{version: }g' <<<"$content" |
  awk '/export const RELEASE_NOTES/ { on = 1 } on { print } on && (/^\]/ || /= *\[\] *;? *$/) { exit }')"

if grep -Eq "(^[[:space:]]*|[{,][[:space:]]*)version:[[:space:]]*['\"]${version//./\\.}['\"]" <<<"$entries"; then
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
