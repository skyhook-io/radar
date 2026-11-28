package kargo

import (
	"slices"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

const freightDoc = `
apiVersion: kargo.akuity.io/v1alpha1
kind: Freight
metadata:
  name: 3f2a9c1d0e
  namespace: shop
  labels:
    kargo.akuity.io/alias: singing-octopus
  creationTimestamp: "2026-11-25T10:00:00Z"
alias: singing-octopus
origin: {kind: Warehouse, name: shop}
images:
- {repoURL: public.ecr.aws/nginx/nginx, tag: 1.31.9, digest: "sha256:0123456789abcdef0123"}
commits:
- {repoURL: "https://git.example/shop/config.git", id: 3f2a9c1d0e5b, branch: main}
charts:
- {repoURL: "oci://ghcr.io/example/charts/podinfo", version: 6.5.0}
status:
  currentlyIn:
    uat: {since: "2026-11-25T11:00:00Z"}
  verifiedIn:
    uat: {verifiedAt: "2026-11-25T11:05:00Z", longestSoak: 30m}
  approvedFor:
    prod: {approvedAt: "2026-11-25T12:00:00Z"}
`

func TestParseFreightReadsAliasOriginAndVersions(t *testing.T) {
	f := ParseFreight(parse(t, freightDoc))
	if f.Alias != "singing-octopus" || f.Origin != (Origin{Kind: "Warehouse", Name: "shop"}) || f.Origin.String() != "Warehouse/shop" {
		t.Fatalf("identity = %+v", f)
	}
	want := []string{"nginx:1.31.9", "config@3f2a9c1", "podinfo 6.5.0"}
	if got := f.Labels(); !slices.Equal(got, want) {
		t.Fatalf("labels = %v, want %v", got, want)
	}
	if !f.DiscoveredAt.Equal(time.Date(2026, 11, 25, 10, 0, 0, 0, time.UTC)) {
		t.Fatalf("discoveredAt falls back to creation, got %v", f.DiscoveredAt)
	}
}

func TestAliasFallsBackToLabel(t *testing.T) {
	u := parse(t, freightDoc)
	delete(u.Object, "alias")
	if got := Alias(u); got != "singing-octopus" {
		t.Fatalf("alias = %q", got)
	}
}

func TestVersionLabels(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   Versions
		want []string
	}{
		{"digest without tag", Versions{Images: []Image{{RepoURL: "ghcr.io/a/b", Digest: "sha256:0123456789abcdef"}}}, []string{"b@sha256:0123456789ab"}},
		{"commit tag wins over id", Versions{Commits: []Commit{{RepoURL: "https://x/repo", ID: "abcdef123456", Tag: "v1.2.0"}}}, []string{"repo@v1.2.0"}},
		{"named chart", Versions{Charts: []Chart{{RepoURL: "https://charts.example", Name: "web", Version: "1.0.0"}}}, []string{"web 1.0.0"}},
		{"other artifact", Versions{Artifacts: []Artifact{{SubscriptionName: "model", Version: "7"}}}, []string{"model 7"}},
		{"nothing", Versions{}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.in.Labels(); !slices.Equal(got, tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestStatusInReadsV112FreightStatus(t *testing.T) {
	u := parse(t, freightDoc)
	now := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	uat := StatusIn(u, "uat")
	if !uat.In || !uat.Verified || uat.LongestSoak != 30*time.Minute || uat.Approved {
		t.Fatalf("uat = %+v", uat)
	}
	if got := uat.SoakedFor(now); got != time.Hour {
		t.Fatalf("soaked = %v, want the current stay of 1h over the 30m record", got)
	}
	if prod := StatusIn(u, "prod"); !prod.Approved || prod.In || prod.Verified || prod.SoakedFor(now) != 0 {
		t.Fatalf("prod = %+v", prod)
	}
	if since, ok := Since(u, "uat"); !ok || !since.Equal(time.Date(2026, 11, 25, 11, 0, 0, 0, time.UTC)) {
		t.Fatalf("since = %v %v", since, ok)
	}
	if _, ok := Since(u, "prod"); ok {
		t.Fatal("a stage the Freight isn't in has no since")
	}
}

func TestFreightIndexOrdersNewestFirst(t *testing.T) {
	old := parse(t, freightDoc)
	newer := parse(t, freightDoc)
	newer.SetName("9b8c7d6e5f")
	set(t, newer, "2026-11-25T12:00:00Z", "discoveredAt")
	other := parse(t, freightDoc)
	other.SetName("other")
	set(t, other, map[string]any{"kind": "Warehouse", "name": "infra"}, "origin")
	x := IndexFreight([]*unstructured.Unstructured{old, newer, other})
	got := x.Newest(Origin{Kind: "Warehouse", Name: "shop"})
	if len(got) != 2 || got[0].Name != "9b8c7d6e5f" || got[1].Name != "3f2a9c1d0e" {
		t.Fatalf("newest = %+v", got)
	}
}
