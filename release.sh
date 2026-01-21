#!/bin/bash
set -e

readonly REPO="https://github.com/skyhook-io/explorer"

# Check prerequisites
echo "Checking prerequisites..."

# Check GITHUB_TOKEN
if [ -z "$GITHUB_TOKEN" ]; then
  # Try to get token from gh CLI
  if command -v gh &> /dev/null && gh auth status &> /dev/null; then
    export GITHUB_TOKEN=$(gh auth token)
    echo "  Using token from gh CLI ✓"
  else
    echo "Error: GITHUB_TOKEN is not set and gh CLI is not authenticated"
    echo ""
    echo "Either:"
    echo "  1. export GITHUB_TOKEN=<your-token>"
    echo "  2. gh auth login"
    exit 1
  fi
else
  echo "  GITHUB_TOKEN set ✓"
fi

# Check goreleaser
if ! command -v goreleaser &> /dev/null; then
  echo "Error: goreleaser not found"
  echo "Install with: brew install goreleaser"
  exit 1
fi
echo "  goreleaser found ✓"

echo ""

# Function to increment version
increment_version() {
  local version=$1
  local part=$2
  version=${version#v}
  IFS='.' read -r major minor patch <<< "$version"

  case $part in
    major) echo "v$((major + 1)).0.0" ;;
    minor) echo "v${major}.$((minor + 1)).0" ;;
    patch) echo "v${major}.${minor}.$((patch + 1))" ;;
  esac
}

# Get latest tag
echo "Checking latest release..."
latest_tag=$(git describe --tags --abbrev=0 2>/dev/null || echo "v0.0.0")
echo "Latest release: $latest_tag"
echo ""

# Choose version
echo "Choose release version:"
echo "1) Patch release ($(increment_version $latest_tag patch))"
echo "2) Minor release ($(increment_version $latest_tag minor))"
echo "3) Major release ($(increment_version $latest_tag major))"
echo "4) Re-release $latest_tag"
echo ""
read -p "Enter choice (1-4): " choice

case $choice in
  1) tag=$(increment_version $latest_tag patch) ;;
  2) tag=$(increment_version $latest_tag minor) ;;
  3) tag=$(increment_version $latest_tag major) ;;
  4) tag=$latest_tag ;;
  *) echo "Invalid choice"; exit 1 ;;
esac

# Check if tag exists
if git rev-parse "$tag" >/dev/null 2>&1; then
  echo "Warning: Tag $tag already exists"
  read -p "Delete and recreate? (y/n): " -n 1 -r
  echo
  [[ $REPLY =~ ^[Yy]$ ]] || exit 1
  git tag -d "$tag"
  git push origin --delete "$tag" 2>/dev/null || true
  gh release delete "$tag" --yes 2>/dev/null || true
fi

echo ""
echo "Ready to release $tag"
read -p "Proceed? (y/n): " -n 1 -r
echo
[[ $REPLY =~ ^[Yy]$ ]] || exit 1

# Tag and release
git tag -a "$tag" -m "Release $tag"
git push origin "$tag"

echo "Running goreleaser..."
goreleaser release --clean

echo ""
echo "Release $tag complete!"
echo "View at: $REPO/releases/tag/$tag"
