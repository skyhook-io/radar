package helm

import (
	"errors"
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
		name      string
		stamped   []stampedObject
		flux      []fluxEvidence
		storage   string
		wantNS    string
		wantErr   error
		wantFound bool
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
			name:    "a release whose Flux record stores it elsewhere doesn't answer for this namespace",
			stamped: []stampedObject{object("apps", "web", "cfg")},
			flux:    []fluxEvidence{flux("flux-system/web", "apps", "flux-system")},
			storage: "apps",
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
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
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

// Two HelmReleases name the same release but store it in different places.
// The one that mapped the storage namespace describes it, whichever the scan
// met first.
func TestObservedInStorageDescribesTheMappedReleaseOnly(t *testing.T) {
	stamped := []stampedObject{{kind: "ConfigMap", namespace: "apps", name: "cfg", release: "web", releaseNS: "apps", chartLabel: "web-1.0.0"}}
	a := fluxEvidence{ref: "team-a/web", releaseName: "web", releaseNamespace: "apps", storageNamespace: "team-a",
		history: []FluxRevision{{ChartName: "web", ChartVersion: "1.0.0"}}, historyChart: "web", historyVersion: "1.0.0"}
	b := fluxEvidence{ref: "team-b/web", releaseName: "web", releaseNamespace: "apps", storageNamespace: "team-b",
		history: []FluxRevision{{ChartName: "web", ChartVersion: "2.0.0"}}, historyChart: "web", historyVersion: "2.0.0"}
	for _, order := range [][]fluxEvidence{{a, b}, {b, a}} {
		scan := &stampScan{stamped: stamped, flux: order, coverage: resourceapi.SearchCoverage{Status: resourceapi.CoverageComplete}}
		detail, err := observedInStorage(&ObjectScope{Provider: &recordingObjects{}}, scan, "team-b", "web")
		if err != nil || detail == nil {
			t.Fatalf("detail = %+v, err = %v", detail, err)
		}
		if detail.ManagedByFluxHelmRelease != "team-b/web" || detail.StorageNamespace != "team-b" || detail.ChartVersion != "2.0.0" || len(detail.FluxHistory) != 1 || detail.FluxHistory[0].ChartVersion != "2.0.0" {
			t.Fatalf("detail = %+v, want team-b/web's record", detail.ObservedRelease)
		}
	}
}
