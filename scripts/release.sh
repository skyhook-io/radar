#!/bin/bash
set -e

# Radar Release Script
# Usage: ./scripts/release.sh

readonly REPO="https://github.com/skyhook-io/radar"
readonly DOCKER_REPO="ghcr.io/skyhook-io/radar"

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

info() { echo -e "${GREEN}[INFO]${NC} $1"; }
warn() { echo -e "${YELLOW}[WARN]${NC} $1"; }
error() { echo -e "${RED}[ERROR]${NC} $1"; exit 1; }

# Get current version info
get_version() {
  if ! git fetch --tags 2>&1 | tee /tmp/git-fetch-output.txt | grep -q "would clobber"; then
    # Fetch succeeded or had non-clobber issues
    if grep -q "fatal\|error" /tmp/git-fetch-output.txt 2>/dev/null; then
      error "Failed to fetch tags: $(cat /tmp/git-fetch-output.txt)"
    fi
  else
    echo ""
    warn "Tag conflict detected:"
    grep "rejected" /tmp/git-fetch-output.txt
    echo ""
    echo "Your local tags are out of sync with remote."
    echo "To fix, run one of:"
    echo "  git fetch --tags --force    # Overwrite local tags with remote"
    echo "  git tag -d <tag-name>       # Delete specific local tag"
    echo ""
    error "Please resolve tag conflicts and try again"
  fi

  LATEST_TAG=$(git tag -l 'v*' --sort=-v:refname | head -n1)
  LATEST_TAG=${LATEST_TAG:-v0.0.0}
}

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

choose_version() {
  echo ""
  info "Latest release: $LATEST_TAG"
  echo ""
  echo "Choose release version:"
  echo "  1) Patch  $(increment_version "$LATEST_TAG" patch)"
  echo "  2) Minor  $(increment_version "$LATEST_TAG" minor)"
  echo "  3) Major  $(increment_version "$LATEST_TAG" major)"
  echo "  4) Re-release $LATEST_TAG"
  echo "  5) Custom"
  echo ""
  read -p "Choice (1-5): " choice

  case $choice in
    1) VERSION=$(increment_version "$LATEST_TAG" patch) ;;
    2) VERSION=$(increment_version "$LATEST_TAG" minor) ;;
    3) VERSION=$(increment_version "$LATEST_TAG" major) ;;
    4) VERSION=$LATEST_TAG ;;
    5) read -p "Enter version (e.g., v1.2.3): " VERSION ;;
    *) error "Invalid choice" ;;
  esac

  if [[ ! $VERSION =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
    error "Invalid version format: $VERSION (expected vX.Y.Z)"
  fi
}

choose_release_mode() {
  echo ""
  echo "Release mode:"
  echo "  1) Remote - Push tag, GitHub Actions builds everything (recommended)"
  echo "  2) Local  - Run goreleaser locally"
  echo ""
  read -p "Choice (1-2): " choice

  case $choice in
    1) RELEASE_MODE="remote" ;;
    2) RELEASE_MODE="local" ;;
    *) error "Invalid choice" ;;
  esac
}

check_prerequisites() {
  # Git
  if ! git rev-parse --git-dir > /dev/null 2>&1; then
    error "Not in a git repository"
  fi

  # Check for uncommitted changes
  if [ -n "$(git status --porcelain)" ]; then
    warn "You have uncommitted changes"
    git status --short
    echo ""
    read -p "Continue anyway? (y/n): " -n 1 -r
    echo
    [[ $REPLY =~ ^[Yy]$ ]] || exit 1
  fi

  # Local mode needs goreleaser and GITHUB_TOKEN
  if [ "$RELEASE_MODE" = "local" ]; then
    if ! command -v goreleaser &> /dev/null; then
      error "goreleaser not found. Install with: brew install goreleaser"
    fi

    if [ -z "$GITHUB_TOKEN" ]; then
      if command -v gh &> /dev/null && gh auth status &> /dev/null; then
        export GITHUB_TOKEN=$(gh auth token)
        info "Using token from gh CLI"
      else
        error "GITHUB_TOKEN not set. Either 'export GITHUB_TOKEN=...' or 'gh auth login'"
      fi
    fi
  fi
}

create_tag() {
  local is_rerelease=false

  if git rev-parse "$VERSION" >/dev/null 2>&1; then
    warn "Tag $VERSION already exists"
    read -p "Delete and recreate? (y/n): " -n 1 -r
    echo
    if [[ $REPLY =~ ^[Yy]$ ]]; then
      is_rerelease=true
      info "Deleting existing release and tag..."
      gh release delete "$VERSION" --yes 2>/dev/null || true
      git push origin --delete "$VERSION" 2>/dev/null || true
      git tag -d "$VERSION" 2>/dev/null || true
    else
      error "Aborted"
    fi
  fi

  info "Creating tag $VERSION..."
  git tag "$VERSION"
  git push origin "$VERSION"
}

release_remote() {
  create_tag

  echo ""
  info "Tag pushed! GitHub Actions will now:"
  echo "    - Build binaries and create GitHub release"
  echo "    - Update Homebrew tap"
  echo "    - Build and push Docker image"
  echo "    - Update Helm chart"
  echo "    - Open PR to krew-index"
  echo ""
  info "Watch progress: gh run watch"
}

release_local() {
  create_tag

  info "Running goreleaser locally..."
  goreleaser release --clean

  info "Binaries released to GitHub + Homebrew"
  echo ""
  warn "Note: Docker image and Helm chart were NOT released."
  warn "For full release, use remote mode or run manually."
}

release_k8s_ui() {
  echo ""
  echo -e "${BLUE}=========================================="
  echo "  @skyhook/k8s-ui Release"
  echo -e "==========================================${NC}"
  echo ""
  echo "This publishes @skyhook/k8s-ui to GitHub Packages."
  echo "Tags use the prefix 'k8s-ui-' (e.g. k8s-ui-v0.1.0)."
  echo ""

  # Fetch tags
  git fetch --tags 2>/dev/null || true

  LATEST_K8S_UI_TAG=$(git tag -l 'k8s-ui-v*' --sort=-v:refname | head -n1)
  LATEST_K8S_UI_TAG=${LATEST_K8S_UI_TAG:-k8s-ui-v0.0.0}

  # Strip prefix for version math
  LATEST_K8S_UI_VER=${LATEST_K8S_UI_TAG#k8s-ui-}

  info "Latest k8s-ui release: $LATEST_K8S_UI_TAG"
  echo ""
  echo "Choose release version:"
  echo "  1) Patch  k8s-ui-$(increment_version "$LATEST_K8S_UI_VER" patch)"
  echo "  2) Minor  k8s-ui-$(increment_version "$LATEST_K8S_UI_VER" minor)"
  echo "  3) Major  k8s-ui-$(increment_version "$LATEST_K8S_UI_VER" major)"
  echo "  4) Custom"
  echo ""
  read -p "Choice (1-4): " choice

  case $choice in
    1) K8S_UI_VERSION="k8s-ui-$(increment_version "$LATEST_K8S_UI_VER" patch)" ;;
    2) K8S_UI_VERSION="k8s-ui-$(increment_version "$LATEST_K8S_UI_VER" minor)" ;;
    3) K8S_UI_VERSION="k8s-ui-$(increment_version "$LATEST_K8S_UI_VER" major)" ;;
    4) read -p "Enter version (e.g., k8s-ui-v0.2.0): " K8S_UI_VERSION ;;
    *) error "Invalid choice" ;;
  esac

  if [[ ! $K8S_UI_VERSION =~ ^k8s-ui-v[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
    error "Invalid version format: $K8S_UI_VERSION (expected k8s-ui-vX.Y.Z)"
  fi

  if [ -n "$(git status --porcelain)" ]; then
    warn "You have uncommitted changes"
    git status --short
    echo ""
    read -p "Continue anyway? (y/n): " -n 1 -r
    echo
    [[ $REPLY =~ ^[Yy]$ ]] || exit 1
  fi

  echo ""
  echo "  Tag: $K8S_UI_VERSION"
  echo ""
  read -p "Proceed? (y/n): " -n 1 -r
  echo
  [[ $REPLY =~ ^[Yy]$ ]] || exit 1

  git tag "$K8S_UI_VERSION"
  git push origin "$K8S_UI_VERSION"

  echo ""
  info "Tag pushed! GitHub Actions will publish @skyhook/k8s-ui to GitHub Packages."
  echo ""
  echo "  Consumers can now run:"
  echo "    npm install @skyhook/k8s-ui@${K8S_UI_VERSION#k8s-ui-v}"
  echo ""
  echo "  Watch progress: gh run watch"
  echo ""
}

release_pkg() {
  echo ""
  echo -e "${BLUE}=========================================="
  echo "  pkg Sub-module Release"
  echo -e "==========================================${NC}"
  echo ""
  echo "This releases github.com/skyhook-io/radar/pkg independently."
  echo "Tags use the prefix 'pkg/' (e.g. pkg/v0.1.0)."
  echo ""

  # Fetch tags
  git fetch --tags 2>/dev/null || true

  LATEST_PKG_TAG=$(git tag -l 'pkg/v*' --sort=-v:refname | head -n1)
  LATEST_PKG_TAG=${LATEST_PKG_TAG:-pkg/v0.0.0}

  # Strip prefix for version math
  LATEST_PKG_VER=${LATEST_PKG_TAG#pkg/}

  info "Latest pkg release: $LATEST_PKG_TAG"
  echo ""
  echo "Choose release version:"
  echo "  1) Patch  pkg/$(increment_version "$LATEST_PKG_VER" patch)"
  echo "  2) Minor  pkg/$(increment_version "$LATEST_PKG_VER" minor)"
  echo "  3) Major  pkg/$(increment_version "$LATEST_PKG_VER" major)"
  echo "  4) Custom"
  echo ""
  read -p "Choice (1-4): " choice

  case $choice in
    1) PKG_VERSION="pkg/$(increment_version "$LATEST_PKG_VER" patch)" ;;
    2) PKG_VERSION="pkg/$(increment_version "$LATEST_PKG_VER" minor)" ;;
    3) PKG_VERSION="pkg/$(increment_version "$LATEST_PKG_VER" major)" ;;
    4) read -p "Enter version (e.g., pkg/v0.2.0): " PKG_VERSION ;;
    *) error "Invalid choice" ;;
  esac

  if [[ ! $PKG_VERSION =~ ^pkg/v[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
    error "Invalid version format: $PKG_VERSION (expected pkg/vX.Y.Z)"
  fi

  if [ -n "$(git status --porcelain)" ]; then
    warn "You have uncommitted changes"
    git status --short
    echo ""
    read -p "Continue anyway? (y/n): " -n 1 -r
    echo
    [[ $REPLY =~ ^[Yy]$ ]] || exit 1
  fi

  echo ""
  echo "  Tag: $PKG_VERSION"
  echo ""
  read -p "Proceed? (y/n): " -n 1 -r
  echo
  [[ $REPLY =~ ^[Yy]$ ]] || exit 1

  git tag "$PKG_VERSION"
  git push origin "$PKG_VERSION"

  echo ""
  info "Tag pushed! Go module proxy will serve pkg at $PKG_VERSION"
  echo ""
  echo "  Consumers can now run:"
  echo "    go get github.com/skyhook-io/radar/pkg@${PKG_VERSION#pkg/}"
  echo ""
  echo "  Release: $REPO/releases/tag/$PKG_VERSION"
  echo ""
}

# Main
main() {
  echo ""
  echo -e "${BLUE}=========================================="
  echo "  Radar Release"
  echo -e "==========================================${NC}"
  echo ""
  echo "What would you like to release?"
  echo "  1) Radar app      (v*.*.*)"
  echo "  2) pkg module     (pkg/v*.*.*)"
  echo "  3) @skyhook/k8s-ui (k8s-ui-v*.*.*)"
  echo ""
  read -p "Choice (1-3): " release_choice
  case $release_choice in
    1) : ;; # continue to app release flow below
    2) release_pkg; exit 0 ;;
    3) release_k8s_ui; exit 0 ;;
    *) error "Invalid choice" ;;
  esac

  get_version
  choose_version
  choose_release_mode
  check_prerequisites

  echo ""
  echo "=========================================="
  echo "  Release Summary"
  echo "=========================================="
  echo "  Version: $VERSION"
  echo "  Mode:    $RELEASE_MODE"
  echo "=========================================="
  echo ""

  read -p "Proceed? (y/n): " -n 1 -r
  echo
  [[ $REPLY =~ ^[Yy]$ ]] || exit 1

  if [ "$RELEASE_MODE" = "remote" ]; then
    release_remote
  else
    release_local
  fi

  echo ""
  echo "=========================================="
  info "Done!"
  echo "=========================================="
  echo ""
  echo "  Release: $REPO/releases/tag/$VERSION"
  echo ""
}

main "$@"
