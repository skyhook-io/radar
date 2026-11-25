// Package longhorn serves Radar's Longhorn workspace: it reads the caller's
// Longhorn objects through the observations the server supplies and turns
// them into readings with pkg/longhorn. Permission checks and cache access
// belong to the server adapter; this package decides what is shown and how
// certain it is.
package longhorn

import (
	"context"
	"sort"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/skyhook-io/radar/internal/integration"
	"github.com/skyhook-io/radar/internal/issues"
	"github.com/skyhook-io/radar/pkg/issuesapi"
	domain "github.com/skyhook-io/radar/pkg/longhorn"
)

const (
	volumesKey = "volumes"
	enginesKey = "engines"
	// PersistentVolumesKey is the coverage entry for the PV read that proves
	// each Volume's current use.
	PersistentVolumesKey = "persistentVolumes"

	// maxIssues bounds the issues returned; IssueTotal counts them all.
	maxIssues = 100
)

var groups = []string{domain.Group}

var workspaceKinds = []integration.WorkspaceKind{
	{Key: volumesKey, Group: domain.Group, Kind: "Volume", Resource: "volumes"},
	{Key: enginesKey, Group: domain.Group, Kind: "Engine", Resource: "engines"},
}

// WorkspaceKinds are the Longhorn kinds the workspace reads.
func WorkspaceKinds() []integration.WorkspaceKind {
	return append([]integration.WorkspaceKind(nil), workspaceKinds...)
}

// Observations are the caller-scoped reads the server supplies.
type Observations struct {
	// Installed: discovery serves longhorn.io Volumes.
	Installed bool
	// Namespaces is the request scope: nil for every namespace the caller may
	// read, or the forced --namespace-scope target.
	Namespaces      []string
	ForcedNamespace string
	WorkspaceRead   func(ctx context.Context, k integration.WorkspaceKind, namespaces []string) (integration.KindAccess, []*unstructured.Unstructured)
	// PersistentVolumes returns every PV with a KindCoverage state: full when
	// all were read, otherwise denied, syncing or uncached and no PVs.
	PersistentVolumes func() ([]*corev1.PersistentVolume, string)
	// ClaimReadable: the caller may read PVCs in namespace.
	ClaimReadable func(namespace string) bool
	// VolumeNames counts the objects of k (Volumes) of each name in every
	// namespace, read with Radar's identity, so a name shared with a Volume
	// the caller can't see still counts twice. ok is false when not every
	// one was read.
	VolumeNames func(ctx context.Context, k integration.WorkspaceKind) (counts map[string]int, ok bool)
}

// Reader builds the workspace for one request.
type Reader struct {
	ClusterContext string
	Observations   Observations
}

// Issue is the subset of an Issues row the workspace renders.
type Issue struct {
	ID        string             `json:"id"`
	Severity  issuesapi.Severity `json:"severity"`
	Category  issuesapi.Category `json:"category"`
	Kind      string             `json:"kind"`
	Group     string             `json:"group,omitempty"`
	Namespace string             `json:"namespace,omitempty"`
	Name      string             `json:"name"`
	Reason    string             `json:"reason"`
	Message   string             `json:"message,omitempty"`
	// FirstSeen is when the fault began, absent when its onset is not known;
	// absence does not mean the fault is new.
	FirstSeen time.Time `json:"first_seen,omitzero"`
}

// WorkspaceResponse is GET /api/longhorn/workspace.
type WorkspaceResponse struct {
	ObservedAt      time.Time                           `json:"observedAt"`
	Installed       bool                                `json:"installed"`
	Context         string                              `json:"context"`
	ForcedNamespace string                              `json:"forcedNamespace,omitempty"`
	Coverage        map[string]integration.KindCoverage `json:"coverage"`
	// Counts is null when no Volume could be read.
	Counts  *domain.HealthCounts   `json:"counts"`
	Volumes []domain.VolumeReading `json:"volumes"`
	// Issues are the first 100 in Problems priority order; IssueTotal counts
	// every one raised from the Volumes read.
	Issues     []Issue `json:"issues"`
	IssueTotal int     `json:"issueTotal"`
}

// Workspace reads every Longhorn Volume the caller can see.
func (r *Reader) Workspace(ctx context.Context) WorkspaceResponse {
	resp := WorkspaceResponse{
		ObservedAt: time.Now().UTC(), Installed: r.Observations.Installed, Context: r.ClusterContext,
		ForcedNamespace: r.Observations.ForcedNamespace, Coverage: map[string]integration.KindCoverage{},
		Volumes: []domain.VolumeReading{}, Issues: []Issue{},
	}
	if !resp.Installed {
		for _, key := range []string{volumesKey, enginesKey, PersistentVolumesKey} {
			resp.Coverage[key] = integration.KindCoverage{State: integration.KindCoverageNotInstalled}
		}
		return resp
	}
	access := map[string]integration.KindAccess{}
	objects := map[string][]*unstructured.Unstructured{}
	for _, k := range workspaceKinds {
		acc, items := r.Observations.WorkspaceRead(ctx, k, r.Observations.Namespaces)
		access[k.Key], objects[k.Key] = acc, integration.KeepGroups(items, groups)
		resp.Coverage[k.Key] = acc.Coverage()
	}
	volumesAccess := access[volumesKey]
	if volumesAccess.State != integration.KindCoverageFull && volumesAccess.State != integration.KindCoveragePartial {
		resp.Coverage[PersistentVolumesKey] = integration.KindCoverage{State: volumesAccess.State}
		return resp
	}
	// PVs are read only for a caller who can read some Volumes: they are
	// what each Volume's use is proven by, and nothing else here needs them.
	pvs, pvState := r.Observations.PersistentVolumes()
	resp.Coverage[PersistentVolumesKey] = integration.KindCoverage{State: pvState}

	// Grouped once, so each Volume reads only its own Engines and PVs.
	type volumeKey struct{ namespace, name string }
	enginesOf := map[volumeKey][]*unstructured.Unstructured{}
	for _, e := range objects[enginesKey] {
		name, _, _ := unstructured.NestedString(e.Object, "spec", "volumeName")
		k := volumeKey{e.GetNamespace(), name}
		enginesOf[k] = append(enginesOf[k], e)
	}
	pvsOf := map[string][]*corev1.PersistentVolume{}
	for _, pv := range pvs {
		if h := domain.PVHandle(pv); h != "" {
			pvsOf[h] = append(pvsOf[h], pv)
		}
	}
	// A PV's handle names no namespace, so it proves a Volume only when no
	// other Volume anywhere shares the name.
	names, namesRead := map[string]int(nil), false
	if pvState == integration.KindCoverageFull {
		names, namesRead = r.Observations.VolumeNames(ctx, workspaceKinds[0])
	}
	var volumes []*unstructured.Unstructured
	for _, v := range objects[volumesKey] {
		if volumesAccess.Covers(v.GetNamespace()) {
			volumes = append(volumes, v)
		}
	}
	counts := &domain.HealthCounts{Complete: volumesAccess.State == integration.KindCoverageFull}
	for _, v := range volumes {
		ns := v.GetNamespace()
		reading := domain.ReadVolume(v, enginesOf[volumeKey{ns, v.GetName()}], access[enginesKey].Covers(ns))
		if namesRead && names[v.GetName()] == 1 {
			reading.Use = domain.UseOf(v.GetName(), pvsOf[v.GetName()], r.Observations.ClaimReadable)
		}
		counts.Add(reading.Health)
		resp.Volumes = append(resp.Volumes, reading)
	}
	resp.Counts = counts
	sort.Slice(resp.Volumes, func(i, j int) bool {
		a, b := resp.Volumes[i], resp.Volumes[j]
		return a.Namespace < b.Namespace || (a.Namespace == b.Namespace && a.Name < b.Name)
	})
	for _, iss := range issues.DetectLonghorn(volumes) {
		resp.IssueTotal++
		if len(resp.Issues) == maxIssues {
			continue
		}
		row := Issue{
			ID: iss.ID, Severity: iss.Severity, Category: iss.Category, Kind: iss.Kind, Group: iss.Group,
			Namespace: iss.Namespace, Name: iss.Name, Reason: iss.Reason, Message: iss.Message,
		}
		if !iss.OnsetUnknown {
			row.FirstSeen = iss.FirstSeen
		}
		resp.Issues = append(resp.Issues, row)
	}
	return resp
}
