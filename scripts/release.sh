#!/bin/bash
set -e

# Radar Release Script
# Usage: ./scripts/release.sh [--binaries] [--docker] [--helm] [--all]

readonly REPO="https://github.com/skyhook-io/radar"
readonly DOCKER_REPO="ghcr.io/skyhook-io/radar"
readonly HELM_CHARTS_REPO="skyhook-io/helm-charts"

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

info() { echo -e "${GREEN}[INFO]${NC} $1"; }
warn() { echo -e "${YELLOW}[WARN]${NC} $1"; }
error() { echo -e "${RED}[ERROR]${NC} $1"; exit 1; }

# Parse arguments
RELEASE_BINARIES=false
RELEASE_DOCKER=false
RELEASE_HELM=false

if [ $# -eq 0 ]; then
  # Interactive mode
  INTERACTIVE=true
else
  INTERACTIVE=false
  for arg in "$@"; do
    case $arg in
      --binaries) RELEASE_BINARIES=true ;;
      --docker) RELEASE_DOCKER=true ;;
      --helm) RELEASE_HELM=true ;;
      --all) RELEASE_BINARIES=true; RELEASE_DOCKER=true; RELEASE_HELM=true ;;
      --help|-h)
        echo "Usage: $0 [OPTIONS]"
        echo ""
        echo "Options:"
        echo "  --binaries  Release CLI binaries via goreleaser (GitHub + Homebrew)"
        echo "  --docker    Build and push Docker image"
        echo "  --helm      Update Helm chart in skyhook-io/helm-charts"
        echo "  --all       Release everything"
        echo ""
        echo "Without options, runs in interactive mode."
        exit 0
        ;;
      *) error "Unknown option: $arg" ;;
    esac
  done
fi

# Check prerequisites
check_prerequisites() {
  info "Checking prerequisites..."

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

  # GITHUB_TOKEN for goreleaser
  if [ "$RELEASE_BINARIES" = true ]; then
    if [ -z "$GITHUB_TOKEN" ]; then
      if command -v gh &> /dev/null && gh auth status &> /dev/null; then
        export GITHUB_TOKEN=$(gh auth token)
        if [ -z "$GITHUB_TOKEN" ]; then
          error "gh auth token returned empty. Try 'gh auth login' to re-authenticate."
        fi
        info "Using token from gh CLI"
      else
        error "GITHUB_TOKEN not set. Either 'export GITHUB_TOKEN=...' or 'gh auth login'"
      fi
    fi

    if ! command -v goreleaser &> /dev/null; then
      error "goreleaser not found. Install with: brew install goreleaser"
    fi
  fi

  # Docker
  if [ "$RELEASE_DOCKER" = true ]; then
    if ! command -v docker &> /dev/null; then
      error "docker not found"
    fi
    if ! docker info &> /dev/null; then
      error "Docker daemon not running"
    fi
  fi

  # Helm chart release
  if [ "$RELEASE_HELM" = true ]; then
    if ! command -v gh &> /dev/null; then
      error "gh CLI not found (needed for helm-charts repo)"
    fi
  fi

  info "Prerequisites OK"
}

# Get version
get_version() {
  git fetch --tags --quiet
  LATEST_TAG=$(git tag -l 'v*' --sort=-v:refname | head -n1)
  LATEST_TAG=${LATEST_TAG:-v0.0.0}

  echo ""
  info "Latest release: $LATEST_TAG"
  echo ""
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
  echo "Choose release version:"
  echo "  1) Patch release ($(increment_version "$LATEST_TAG" patch))"
  echo "  2) Minor release ($(increment_version "$LATEST_TAG" minor))"
  echo "  3) Major release ($(increment_version "$LATEST_TAG" major))"
  echo "  4) Re-release $LATEST_TAG"
  echo "  5) Custom version"
  echo ""
  read -p "Enter choice (1-5): " choice

  case $choice in
    1) VERSION=$(increment_version "$LATEST_TAG" patch) ;;
    2) VERSION=$(increment_version "$LATEST_TAG" minor) ;;
    3) VERSION=$(increment_version "$LATEST_TAG" major) ;;
    4) VERSION=$LATEST_TAG ;;
    5) read -p "Enter version (e.g., v1.2.3): " VERSION ;;
    *) error "Invalid choice" ;;
  esac

  # Validate version format
  if [[ ! $VERSION =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
    error "Invalid version format: $VERSION (expected vX.Y.Z)"
  fi
}

choose_targets() {
  echo ""
  echo "What would you like to release?"
  echo "  1) CLI binaries only (GitHub releases + Homebrew)"
  echo "  2) Docker image only"
  echo "  3) Helm chart only"
  echo "  4) CLI + Docker + Helm (everything)"
  echo ""
  read -p "Enter choice (1-4): " choice

  case $choice in
    1) RELEASE_BINARIES=true ;;
    2) RELEASE_DOCKER=true ;;
    3) RELEASE_HELM=true ;;
    4) RELEASE_BINARIES=true; RELEASE_DOCKER=true; RELEASE_HELM=true ;;
    *) error "Invalid choice" ;;
  esac
}

create_tag() {
  if git rev-parse "$VERSION" >/dev/null 2>&1; then
    warn "Tag $VERSION already exists"
    read -p "Delete and recreate? (y/n): " -n 1 -r
    echo
    if [[ $REPLY =~ ^[Yy]$ ]]; then
      git tag -d "$VERSION"
      git push origin --delete "$VERSION" 2>/dev/null || true
      gh release delete "$VERSION" --yes 2>/dev/null || true
    else
      info "Using existing tag"
      return
    fi
  fi

  info "Creating tag $VERSION..."
  git tag -a "$VERSION" -m "Release $VERSION"
  git push origin "$VERSION"
}

release_binaries() {
  info "Releasing CLI binaries via goreleaser..."
  goreleaser release --clean
  info "CLI binaries released to GitHub + Homebrew"
}

release_docker() {
  local docker_tag=${VERSION#v}  # Strip 'v' prefix for Docker tags

  info "Building Docker image..."
  docker build -t "$DOCKER_REPO:$docker_tag" -t "$DOCKER_REPO:latest" .

  info "Pushing Docker image..."
  docker push "$DOCKER_REPO:$docker_tag"
  docker push "$DOCKER_REPO:latest"

  info "Docker image pushed: $DOCKER_REPO:$docker_tag"
}

release_helm() {
  info "Updating Helm chart..."

  # Update Chart.yaml with new version
  local chart_file="deploy/helm/radar/Chart.yaml"
  local version_no_v=${VERSION#v}

  # Update appVersion (the app version)
  sed -i '' "s/^appVersion:.*/appVersion: \"$version_no_v\"/" "$chart_file"

  # Update chart version to match
  sed -i '' "s/^version:.*/version: $version_no_v/" "$chart_file"

  info "Chart.yaml updated to version $version_no_v"

  # Clone helm-charts repo and update
  local tmp_dir=$(mktemp -d)
  info "Cloning helm-charts repo..."
  git clone --quiet "git@github.com:$HELM_CHARTS_REPO.git" "$tmp_dir"

  # Copy chart
  rm -rf "$tmp_dir/charts/radar"
  cp -r deploy/helm/radar "$tmp_dir/charts/"

  # Commit and push
  cd "$tmp_dir"
  git add charts/radar
  if git diff --staged --quiet; then
    warn "No changes to Helm chart"
  else
    git commit -m "Update radar chart to $VERSION"
    git push origin main
    info "Helm chart pushed to $HELM_CHARTS_REPO"
  fi
  cd - > /dev/null
  rm -rf "$tmp_dir"

  info "Helm chart released (will be published by chart-releaser-action)"
}

# Main
main() {
  echo ""
  echo "=========================================="
  echo "  Radar Release"
  echo "=========================================="
  echo ""

  if [ "$INTERACTIVE" = true ]; then
    get_version
    choose_version
    choose_targets
    check_prerequisites
  else
    check_prerequisites
    get_version
    VERSION=$LATEST_TAG
    # For non-interactive, use latest tag or require it to be set
    if [ "$VERSION" = "v0.0.0" ]; then
      error "No existing tags found. Run interactively to create first release."
    fi
    warn "Using existing tag: $VERSION"
  fi

  echo ""
  echo "=========================================="
  echo "  Release Summary"
  echo "=========================================="
  echo "  Version: $VERSION"
  echo "  Binaries (goreleaser): $RELEASE_BINARIES"
  echo "  Docker: $RELEASE_DOCKER"
  echo "  Helm chart: $RELEASE_HELM"
  echo "=========================================="
  echo ""

  if [ "$INTERACTIVE" = true ]; then
    read -p "Proceed with release? (y/n): " -n 1 -r
    echo
    [[ $REPLY =~ ^[Yy]$ ]] || exit 1
  fi

  # Create/verify tag (only for binaries release which needs it)
  if [ "$RELEASE_BINARIES" = true ]; then
    create_tag
  fi

  # Execute releases
  if [ "$RELEASE_BINARIES" = true ]; then
    release_binaries
  fi

  if [ "$RELEASE_DOCKER" = true ]; then
    release_docker
  fi

  if [ "$RELEASE_HELM" = true ]; then
    release_helm
  fi

  echo ""
  echo "=========================================="
  info "Release complete!"
  echo "=========================================="
  echo ""
  echo "  GitHub Release: $REPO/releases/tag/$VERSION"
  [ "$RELEASE_DOCKER" = true ] && echo "  Docker Image: $DOCKER_REPO:$VERSION"
  [ "$RELEASE_HELM" = true ] && echo "  Helm Chart: helm install radar skyhook/radar --version ${VERSION#v}"
  echo ""
}

main "$@"
