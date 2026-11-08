package helm

import (
	"errors"
	"strings"
	"testing"

	"github.com/skyhook-io/radar/pkg/resourceapi"
)

func TestObservedInStorageResolvesFluxStorageNamespaces(t *testing.T) {
	object := func(releaseNS, release, name string) stampedObject {
		return stampedObject{kind: "ConfigMap", namespace: releaseNS, name: name, release: release, releaseNS: releaseNS, chartLabel: "web-1.0.0"}
	}
	flux := func(ref, releaseNS, storageNS string) fluxEvidence {
		return fluxEvidence{ref: ref, releaseName: "web", releaseNamespace: releaseNS, storageNamespace: storageNS,
			history: []FluxRevision{{ChartName: "web", ChartVersion: ref}}, historyChart: "web", historyVersion: ref}
	}
	for _, tc := range []struct {
		name        string
		stamped     []stampedObject
		flux        []fluxEvidence
		storage     string
		wantNS      string
		wantErr     error
		wantErrText string
		wantFound   bool
	}{
		{
			name:    "native Helm stores a release in its own namespace",
			stamped: []stampedObject{object("apps", "web", "cfg")},
			storage: "apps", wantNS: "apps", wantFound: true,
		},
		{
			name:    "a Flux HelmRelease storing it elsewhere maps the storage namespace",
			stamped: []stampedObject{object("apps", "web", "cfg")},
			flux:    []fluxEvidence{flux("flux-system/web", "apps", "flux-system")},
			storage: "flux-system", wantNS: "apps", wantFound: true,
		},
		{
			name:    "the mapping wins over a same-named release living in the storage namespace",
			stamped: []stampedObject{object("apps", "web", "cfg"), object("flux-system", "web", "other")},
			flux:    []fluxEvidence{flux("flux-system/web", "apps", "flux-system")},
			storage: "flux-system", wantNS: "apps", wantFound: true,
		},
		{
			name:    "a release whose Flux record stores it elsewhere names that namespace",
			stamped: []stampedObject{object("apps", "web", "cfg")},
			flux:    []fluxEvidence{flux("flux-system/web", "apps", "flux-system")},
			storage: "apps", wantErr: ErrStoredElsewhere, wantErrText: "pass namespace=flux-system",
		},
		{
			name:    "two HelmReleases storing the name in one namespace aren't guessed between",
			stamped: []stampedObject{object("apps", "web", "cfg"), object("staging", "web", "cfg")},
			flux:    []fluxEvidence{flux("flux-system/web-b", "staging", "flux-system"), flux("flux-system/web-a", "apps", "flux-system")},
			storage: "flux-system", wantErr: ErrAmbiguousStorage,
		},
		{
			name:    "nothing readable carries the release",
			stamped: []stampedObject{object("apps", "other", "cfg")},
			storage: "apps",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			scan := &stampScan{stamped: tc.stamped, flux: tc.flux, coverage: resourceapi.SearchCoverage{Status: resourceapi.CoverageComplete}}
			detail, err := observedInStorage(&ObjectScope{Provider: &recordingObjects{}}, scan, tc.storage, "web")
			if !errors.Is(err, tc.wantErr) || (err != nil && !strings.Contains(err.Error(), tc.wantErrText)) {
				t.Fatalf("err = %v, want %v containing %q", err, tc.wantErr, tc.wantErrText)
			}
			if (detail != nil) != tc.wantFound {
				t.Fatalf("detail = %+v, want found=%v", detail, tc.wantFound)
			}
			if detail == nil {
				return
			}
			if detail.Namespace != tc.wantNS || detail.ResourceCount != 1 {
				t.Fatalf("detail = %s with %d objects, want %s with 1", detail.Namespace, detail.ResourceCount, tc.wantNS)
			}
		})
	}
}

// Two HelmReleases deploy the same release but store it in different places.
// The objects' stamps record only apps/web, so they can't say whose they are:
// asking by either storage namespace refuses rather than crediting them to one.
func TestObservedInStorageRefusesAReleaseTwoHelmReleasesDeploy(t *testing.T) {
	stamped := []stampedObject{{kind: "ConfigMap", namespace: "apps", name: "cfg", release: "web", releaseNS: "apps", chartLabel: "web-1.0.0"}}
	a := fluxEvidence{ref: "team-a/web", releaseName: "web", releaseNamespace: "apps", storageNamespace: "team-a"}
	b := fluxEvidence{ref: "team-b/web", releaseName: "web", releaseNamespace: "apps", storageNamespace: "team-b"}
	for _, order := range [][]fluxEvidence{{a, b}, {b, a}} {
		for _, storage := range []string{"team-a", "team-b", "apps"} {
			scan := &stampScan{stamped: stamped, flux: order, coverage: resourceapi.SearchCoverage{Status: resourceapi.CoverageComplete}}
			detail, err := observedInStorage(&ObjectScope{Provider: &recordingObjects{}}, scan, storage, "web")
			if detail != nil {
				t.Fatalf("%s: credited the objects to one HelmRelease: %+v", storage, detail.ObservedRelease)
			}
			want := ErrAmbiguousStorage
			if storage == "apps" {
				want = ErrStoredElsewhere
			}
			if !errors.Is(err, want) || !strings.Contains(err.Error(), "team-a/web") || !strings.Contains(err.Error(), "team-b/web") {
				t.Fatalf("%s: err = %v, want %v naming both HelmReleases", storage, err, want)
			}
		}
	}
}
