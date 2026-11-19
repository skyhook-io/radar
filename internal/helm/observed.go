package helm

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/search"
	"github.com/skyhook-io/radar/pkg/gitops"
	"github.com/skyhook-io/radar/pkg/packages"
	"github.com/skyhook-io/radar/pkg/readreason"
	"github.com/skyhook-io/radar/pkg/resourceapi"
	"github.com/skyhook-io/radar/pkg/resourceid"
)

// Helm writes these two annotations on every object it installs or upgrades
// (setMetadataVisitor in helm.sh/helm/v3 pkg/action/validate.go). The chart and
// version labels are only a chart-author convention.
const (
	releaseNameAnnotation      = "meta.helm.sh/release-name"
	releaseNamespaceAnnotation = "meta.helm.sh/release-namespace"
	chartLabelKey              = "helm.sh/chart"
	appVersionLabelKey         = "app.kubernetes.io/version"

	fluxHelmReleaseKind  = "HelmRelease"
	fluxHelmReleaseGroup = "helm.toolkit.fluxcd.io"

	// A chart that follows `helm create` truncates this label to 63 characters
	// and then trims a trailing "-", so a label of 62 or 63 characters may have
	// lost part of its version.
	maxChartLabelLength = 63
)

// ErrConnectionChanged means Radar's cluster connection changed during a
// read, so the answer can't be trusted.
var ErrConnectionChanged = errors.New("cluster connection changed during the read")

// ObservedRelease is a release as its deployed objects describe it, built
// without reading Helm's storage. It deliberately has no status, revision or
// update time: only Helm's release Secrets record those, and a default here
// would read as "deployed". Empty Chart, ChartVersion and AppVersion mean
// unknown.
type ObservedRelease struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
	// StorageNamespace is set only when a readable Flux HelmRelease records
	// that Helm stores this release in another namespace. Empty doesn't say
	// where it's stored: without that HelmRelease Radar can't know.
	StorageNamespace string `json:"storageNamespace,omitempty"`
	Chart            string `json:"chart,omitempty"`
	ChartVersion     string `json:"chartVersion,omitempty"`
	AppVersion       string `json:"appVersion,omitempty"`
	// OtherCharts lists the other helm.sh/chart labels on the release's
	// objects, as written. When Chart is empty they are the candidates Radar
	// wouldn't choose between; otherwise they are normally subcharts.
	OtherCharts []string `json:"otherCharts,omitempty"`
	// ChartVersions and AppVersions list the distinct values, sorted, found
	// when the chosen chart's objects disagree, as after an interrupted
	// upgrade. ChartVersion or AppVersion is then empty: Radar doesn't pick
	// one. They come from object labels only: when a readable Flux HelmRelease
	// names the release's chart in its history, Flux's versions are used and
	// these are empty. Absent doesn't prove the objects agree.
	ChartVersions []string `json:"chartVersions,omitempty"`
	AppVersions   []string `json:"appVersions,omitempty"`
	// ResourceCount is how many of the caller's readable objects carry the
	// release's stamps, which can be fewer than the release has.
	ResourceCount int `json:"resourceCount"`
	// ResourceHealth is "unknown" rather than "healthy" when the object read
	// had gaps. HealthSummary still counts what was read, so "unknown" can sit
	// beside "2/2 ready".
	ResourceHealth string `json:"resourceHealth,omitempty"`
	HealthIssue    string `json:"healthIssue,omitempty"`
	HealthSummary  string `json:"healthSummary,omitempty"`
	// ManagedByFluxHelmRelease is "namespace/name" of a HelmRelease the caller
	// can read that records this release. Empty doesn't mean Flux isn't
	// managing it: the caller may not be able to read that HelmRelease.
	ManagedByFluxHelmRelease string `json:"managedByFluxHelmRelease,omitempty"`
}

// FluxRevision is one entry of a Flux HelmRelease's status.history: what Flux
// recorded, not what Helm's storage says now.
type FluxRevision struct {
	Revision     int    `json:"revision"`
	Status       string `json:"status,omitempty"`
	ChartName    string `json:"chartName,omitempty"`
	ChartVersion string `json:"chartVersion,omitempty"`
	AppVersion   string `json:"appVersion,omitempty"`
	// Digest is Flux's checksum of the stored Helm release, not an image or
	// OCI artifact digest.
	Digest       string     `json:"digest,omitempty"`
	LastDeployed *time.Time `json:"lastDeployed,omitempty"`
}

// ObservedReleaseDetail is one observed release and what the read covered.
type ObservedReleaseDetail struct {
	ObservedRelease
	Resources []OwnedResource `json:"resources,omitempty"`
	// FluxHistory is newest first. Empty means Radar got no Flux record, not
	// that the release has no revisions.
	FluxHistory    []FluxRevision             `json:"fluxHistory,omitempty"`
	ObjectCoverage resourceapi.SearchCoverage `json:"objectCoverage"`
}

// ObjectScope is a caller's view of the cached objects, built by each surface
// from its own per-user checks.
type ObjectScope struct {
	Provider objectProvider
	Access   search.ScanAccess
	// Requested is what the caller narrowed to; see RequestedNamespaces.
	Requested []string
}

type objectProvider interface {
	search.Provider
	// Current reports whether the caches the read used are still the live ones.
	Current() bool
}

type stampedObject struct {
	kind, group, apiVersion string
	namespace, name         string
	release, releaseNS      string
	chartLabel, appVersion  string
}

type fluxEvidence struct {
	ref               string
	releaseName       string
	releaseNamespace  string
	storageNamespace  string
	history           []FluxRevision
	historyChart      string
	historyVersion    string
	historyAppVersion string
}

// skipScanKind leaves out what must not or need not be read. Secrets are never
// read here: that is this path's whole contract. Nodes and Events never carry
// stamps, and a chart can't deploy them in any useful way.
func skipScanKind(kind, group string) bool {
	switch group {
	case "":
		return kind == "Secret" || kind == "Event" || kind == "Node"
	case "events.k8s.io":
		return kind == "Event"
	}
	return false
}

// stampScan is the object evidence of one read.
type stampScan struct {
	stamped  []stampedObject
	flux     []fluxEvidence
	coverage resourceapi.SearchCoverage
	seen     map[string]bool
}

// add records what one object says about Helm. A controller-owned object is
// skipped because controllers copy their parent's annotations onto what they
// create, as a Deployment does to its ReplicaSets, and Helm didn't deploy those.
func (s *stampScan) add(o search.ScannedObject) {
	if o.Kind == fluxHelmReleaseKind && o.Group == fluxHelmReleaseGroup {
		if u, ok := o.Object.(*unstructured.Unstructured); ok {
			if evidence, ok := parseFluxEvidence(u); ok {
				s.flux = append(s.flux, evidence)
			}
		}
	}
	annotations := o.Object.GetAnnotations()
	release, releaseNS := annotations[releaseNameAnnotation], annotations[releaseNamespaceAnnotation]
	if release == "" || releaseNS == "" || metav1.GetControllerOfNoCopy(o.Object) != nil {
		return
	}
	key := o.Kind + "/" + o.Group + "/" + o.Object.GetNamespace() + "/" + o.Object.GetName()
	if s.seen[key] {
		return
	}
	s.seen[key] = true
	labels := o.Object.GetLabels()
	stamped := stampedObject{
		kind: o.Kind, group: o.Group, namespace: o.Object.GetNamespace(), name: o.Object.GetName(),
		release: release, releaseNS: releaseNS, chartLabel: labels[chartLabelKey], appVersion: labels[appVersionLabelKey],
	}
	if u, ok := o.Object.(*unstructured.Unstructured); ok {
		stamped.apiVersion = u.GetAPIVersion()
	}
	s.stamped = append(s.stamped, stamped)
}

// scanObservedReleases reads the stamps the caller can see. HelmReleases are
// warmed because the scan reads only watched kinds and Radar may not have
// started this one; the scan still decides whether this caller may read them.
func scanObservedReleases(ctx context.Context, scope *ObjectScope) (*stampScan, error) {
	warmFluxHelmReleases(ctx, scope.Provider)

	scan := &stampScan{seen: map[string]bool{}}
	coverage, err := search.Scan(ctx, scope.Provider, scope.Access, skipScanKind, scan.add)
	if errors.Is(err, search.ErrConnectionChanged) {
		return nil, ErrConnectionChanged
	}
	if err != nil {
		return nil, err
	}
	scan.coverage = coverage
	return scan, nil
}

func warmFluxHelmReleases(ctx context.Context, provider search.Provider) {
	discovery := k8s.GetResourceDiscovery()
	if discovery == nil {
		return
	}
	if gvr, ok := discovery.GetGVRWithGroup(fluxHelmReleaseKind, fluxHelmReleaseGroup); ok {
		_ = provider.WarmDynamic(ctx, gvr, "")
	}
}

func parseFluxEvidence(hr *unstructured.Unstructured) (fluxEvidence, bool) {
	// A release installed through spec.kubeConfig lives in another cluster; a
	// same-named local release is not Flux's.
	if !gitops.FluxTargetsLocalCluster(hr) {
		return fluxEvidence{}, false
	}
	name, namespace, storage := gitops.FluxHelmTarget(hr)
	if recorded, _, _ := unstructured.NestedString(hr.Object, "status", "storageNamespace"); recorded != "" {
		storage = recorded
	}
	evidence := fluxEvidence{
		ref:              hr.GetNamespace() + "/" + hr.GetName(),
		releaseName:      name,
		releaseNamespace: namespace,
		storageNamespace: storage,
	}
	entries, _, _ := unstructured.NestedSlice(hr.Object, "status", "history")
	for i, entry := range entries {
		snapshot, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		revision, _, _ := unstructured.NestedInt64(snapshot, "version")
		text := func(key string) string {
			value, _ := snapshot[key].(string)
			return value
		}
		evidence.history = append(evidence.history, FluxRevision{
			Revision:     int(revision),
			Status:       text("status"),
			ChartName:    text("chartName"),
			ChartVersion: text("chartVersion"),
			AppVersion:   text("appVersion"),
			Digest:       text("digest"),
			LastDeployed: parseFluxTime(text("lastDeployed")),
		})
		// Flux keeps the newest snapshot first, and it names the release
		// exactly, where the spec only implies it.
		if i == 0 {
			if v := text("name"); v != "" {
				evidence.releaseName = v
			}
			if v := text("namespace"); v != "" {
				evidence.releaseNamespace = v
			}
			evidence.historyChart, evidence.historyVersion, evidence.historyAppVersion = text("chartName"), text("chartVersion"), text("appVersion")
		}
	}
	return evidence, true
}

func parseFluxTime(value string) *time.Time {
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return nil
	}
	return &parsed
}

type releaseKey struct{ namespace, name string }

func groupStamped(stamped []stampedObject) map[releaseKey][]stampedObject {
	groups := map[releaseKey][]stampedObject{}
	for _, s := range stamped {
		key := releaseKey{s.releaseNS, s.release}
		groups[key] = append(groups[key], s)
	}
	return groups
}

// observedRow builds one release's row. Flux evidence replaces what labels say
// about its chart, because Flux recorded it from the release itself.
func observedRow(key releaseKey, objects []stampedObject, flux []fluxEvidence) ObservedRelease {
	row := ObservedRelease{Name: key.name, Namespace: key.namespace, ResourceCount: len(objects)}
	choice := chooseChart(key.name, objects)
	row.Chart, row.ChartVersion, row.AppVersion, row.OtherCharts = choice.chart, choice.version, choice.appVersion, choice.others
	row.ChartVersions, row.AppVersions = choice.versions, choice.appVersions
	if f, ok := fluxRecordFor(key, flux); ok {
		row.ManagedByFluxHelmRelease = f.ref
		if f.storageNamespace != key.namespace {
			row.StorageNamespace = f.storageNamespace
		}
		if f.historyChart != "" {
			row.Chart, row.ChartVersion, row.AppVersion = f.historyChart, f.historyVersion, f.historyAppVersion
			row.ChartVersions, row.AppVersions = nil, nil
			row.OtherCharts = slices.DeleteFunc(allChartLabels(objects), func(label string) bool {
				name, _ := parseChartLabel(label)
				return name == f.historyChart
			})
		}
	}
	return row
}

// fluxRecordFor is the one readable Flux HelmRelease that deploys the release.
// Stamps record only the release's own namespace and name, so when two deploy
// it (storing it in different namespaces) neither is credited.
func fluxRecordFor(key releaseKey, flux []fluxEvidence) (fluxEvidence, bool) {
	var found []fluxEvidence
	for _, f := range flux {
		if f.releaseNamespace == key.namespace && f.releaseName == key.name {
			found = append(found, f)
		}
	}
	if len(found) != 1 {
		return fluxEvidence{}, false
	}
	return found[0], true
}

// chartChoice is what a release's object labels say about its chart.
type chartChoice struct {
	chart, version, appVersion string
	// versions and appVersions are set only when the chosen chart's objects
	// disagree; version or appVersion is then empty.
	versions, appVersions []string
	others                []string
}

// chooseChart names the release's chart from its objects' labels. Subcharts
// label their own objects, and they often outnumber the parent's (21 clickhouse
// objects to 8 signoz in one real release), so a count can't identify the
// parent. With several chart names, only one named exactly like the release is
// chosen; otherwise none is, and all are returned for the reader to see. A name
// match is a convention, not proof: a release can be named after a subchart.
//
// A version is reported only when every object of the chosen chart that has
// one agrees. Objects that disagree are what an interrupted upgrade leaves, and
// a majority would hide the minority exactly when it matters; voting on chart
// and app version separately could also pair two values no object carries.
func chooseChart(release string, objects []stampedObject) chartChoice {
	names := map[string]bool{}
	for _, o := range objects {
		if o.chartLabel != "" {
			name, _ := parseChartLabel(o.chartLabel)
			names[name] = true
		}
	}
	var choice chartChoice
	switch {
	case len(names) == 1:
		for name := range names {
			choice.chart = name
		}
	case names[release]:
		choice.chart = release
	default:
		choice.others = allChartLabels(objects)
		return choice
	}
	var versions, appVersions []string
	for _, o := range objects {
		if o.chartLabel == "" {
			continue
		}
		name, v := parseChartLabel(o.chartLabel)
		if name != choice.chart {
			choice.others = append(choice.others, o.chartLabel)
			continue
		}
		if v != "" {
			versions = append(versions, v)
		}
		if o.appVersion != "" {
			appVersions = append(appVersions, o.appVersion)
		}
	}
	slices.Sort(choice.others)
	choice.others = slices.Compact(choice.others)
	choice.version, choice.versions = agreed(versions)
	choice.appVersion, choice.appVersions = agreed(appVersions)
	return choice
}

func allChartLabels(objects []stampedObject) []string {
	labels := []string{}
	for _, o := range objects {
		if o.chartLabel != "" {
			labels = append(labels, o.chartLabel)
		}
	}
	slices.Sort(labels)
	return slices.Compact(labels)
}

// parseChartLabel splits a helm.sh/chart label. Charts replace a version's "+"
// with "_", which semver doesn't allow, because labels can't hold "+". A label
// at or one short of the length limit may be cut off, so its version isn't
// trusted.
func parseChartLabel(label string) (name, version string) {
	name, version = packages.SplitChart(label)
	if len(label) >= maxChartLabelLength-1 {
		return name, ""
	}
	return name, strings.ReplaceAll(version, "_", "+")
}

// agreed returns the one value seen, or, when values differ, no value and
// every distinct one, sorted.
func agreed(values []string) (string, []string) {
	slices.Sort(values)
	values = slices.Compact(values)
	switch len(values) {
	case 0:
		return "", nil
	case 1:
		return values[0], nil
	}
	return "", values
}

// addObservedHealth summarises health from the objects the caller read. Only a
// built-in kind can be looked up: the status lookup ignores API group, so a
// custom kind named like a built-in would read the wrong object. With a gap in
// what was read, an unread part could be the broken one, so "healthy" isn't
// claimed.
func addObservedHealth(row *ObservedRelease, objects []stampedObject, partial bool) []OwnedResource {
	resources := make([]OwnedResource, 0, len(objects))
	for _, o := range objects {
		resource := OwnedResource{Kind: o.kind, APIVersion: objectAPIVersion(o), Name: o.name, Namespace: o.namespace}
		resources = append(resources, resource)
	}
	enrichResourcesWithStatus(resources, scannedObjects{})
	row.ResourceHealth, row.HealthIssue, row.HealthSummary = computeResourceHealth(resources)
	if partial && row.ResourceHealth == "healthy" {
		row.ResourceHealth = "unknown"
	}
	return resources
}

func objectAPIVersion(o stampedObject) string {
	if o.apiVersion != "" {
		return o.apiVersion
	}
	builtin, ok := resourceid.BuiltinForKind(o.kind)
	if !ok || builtin.Group != o.group || builtin.Version == "" {
		return ""
	}
	if o.group == "" {
		return builtin.Version
	}
	return o.group + "/" + builtin.Version
}

// coverageMaySettle reports a gap that can close by itself, so finding no object
// doesn't yet mean the release is gone. Gaps that need someone to change access
// or configuration are the caller's stable answer.
func coverageMaySettle(gaps []resourceapi.Gap) bool {
	for _, gap := range gaps {
		switch action, _ := readreason.ActionFor(gap.Code); action {
		case readreason.ActionRetry, readreason.ActionWait:
			return true
		}
	}
	return false
}

// GetObservedRelease reads one release from its stamps, with its resources and
// any Flux history. It returns nil, nil only for a settled answer that no object
// the caller can read counts toward the release. A read that may still settle
// is ErrObjectsUnavailable, and a changed connection is ErrConnectionChanged.
func GetObservedRelease(ctx context.Context, scope *ObjectScope, namespace, name string) (*ObservedReleaseDetail, error) {
	scan, err := scanObservedReleases(ctx, scope)
	if err != nil {
		return nil, err
	}
	return observedDetail(scope, scan, releaseKey{namespace, name}, scan.flux)
}

// ErrAmbiguousStorage means more than one readable Flux HelmRelease matches
// the release asked for, and its objects can't say which one they belong to.
var ErrAmbiguousStorage = errors.New("more than one Flux HelmRelease matches this release, so Radar won't pick one")

// ErrStoredElsewhere means the namespace asked for is the release's own, but a
// readable Flux HelmRelease stores it in another one, which is the namespace
// to ask with. Its message names that namespace.
var ErrStoredElsewhere = errors.New("a Flux HelmRelease stores this release in another namespace")

// GetObservedReleaseInStorage is GetObservedRelease for a caller that names the
// release by the namespace Helm stores it in, as get_helm_release does. The two
// differ only when a Flux HelmRelease sets a storage namespace, so a readable
// one that maps the name decides. Otherwise the namespace is taken as the
// release's own, as Helm stores a release in its own namespace.
func GetObservedReleaseInStorage(ctx context.Context, scope *ObjectScope, storageNamespace, name string) (*ObservedReleaseDetail, error) {
	scan, err := scanObservedReleases(ctx, scope)
	if err != nil {
		return nil, err
	}
	return observedInStorage(scope, scan, storageNamespace, name)
}

func observedInStorage(scope *ObjectScope, scan *stampScan, storageNamespace, name string) (*ObservedReleaseDetail, error) {
	key, flux, err := resolveStoredRelease(scan.flux, storageNamespace, name)
	if err != nil {
		return nil, err
	}
	return observedDetail(scope, scan, key, flux)
}

// resolveStoredRelease finds the release Helm stores as storageNamespace/name,
// and the Flux evidence to describe it with. Object stamps record only the
// release's own namespace and name, so when two readable HelmReleases deploy
// the same one from different storage namespaces its objects could belong to
// either, and it refuses rather than credit them to one.
func resolveStoredRelease(flux []fluxEvidence, storageNamespace, name string) (releaseKey, []fluxEvidence, error) {
	var stored []fluxEvidence
	for _, f := range flux {
		if f.storageNamespace == storageNamespace && f.releaseName == name {
			stored = append(stored, f)
		}
	}
	if len(stored) > 1 {
		return releaseKey{}, nil, ambiguousStorage(stored)
	}
	key := releaseKey{storageNamespace, name}
	if len(stored) == 1 {
		key.namespace = stored[0].releaseNamespace
	}
	// HelmReleases deploying this release that store it somewhere other than
	// the namespace asked for.
	var elsewhere []fluxEvidence
	for _, f := range flux {
		if f.releaseNamespace == key.namespace && f.releaseName == name && f.storageNamespace != storageNamespace {
			elsewhere = append(elsewhere, f)
		}
	}
	switch {
	case len(stored) == 1 && len(elsewhere) > 0:
		return releaseKey{}, nil, ambiguousStorage(append(stored, elsewhere...))
	case len(stored) == 1:
		return key, stored, nil
	case len(elsewhere) > 0:
		namespaces := make([]string, 0, len(elsewhere))
		refs := make([]string, 0, len(elsewhere))
		for _, f := range elsewhere {
			namespaces = append(namespaces, f.storageNamespace)
			refs = append(refs, f.ref)
		}
		slices.Sort(namespaces)
		sort.Strings(refs)
		return releaseKey{}, nil, fmt.Errorf("%w: pass namespace=%s (HelmRelease %s)", ErrStoredElsewhere, strings.Join(slices.Compact(namespaces), " or "), strings.Join(refs, ", "))
	}
	return key, flux, nil
}

func ambiguousStorage(matches []fluxEvidence) error {
	refs := make([]string, len(matches))
	for i, f := range matches {
		refs[i] = f.ref
	}
	sort.Strings(refs)
	return fmt.Errorf("%w: %s", ErrAmbiguousStorage, strings.Join(refs, ", "))
}

func observedDetail(scope *ObjectScope, scan *stampScan, key releaseKey, flux []fluxEvidence) (*ObservedReleaseDetail, error) {
	objects := groupStamped(scan.stamped)[key]
	if len(objects) == 0 {
		if !scope.Provider.Current() {
			return nil, ErrConnectionChanged
		}
		if coverageMaySettle(scan.coverage.Gaps) {
			return nil, ErrObjectsUnavailable
		}
		return nil, nil
	}
	sort.Slice(objects, func(i, j int) bool {
		if objects[i].kind != objects[j].kind {
			return objects[i].kind < objects[j].kind
		}
		if objects[i].namespace != objects[j].namespace {
			return objects[i].namespace < objects[j].namespace
		}
		return objects[i].name < objects[j].name
	})
	detail := &ObservedReleaseDetail{ObservedRelease: observedRow(key, objects, flux), ObjectCoverage: scan.coverage}
	detail.Resources = addObservedHealth(&detail.ObservedRelease, objects, scan.coverage.Status != resourceapi.CoverageComplete)
	// Health reads the current cache, so this has to come after it.
	if !scope.Provider.Current() {
		return nil, ErrConnectionChanged
	}
	if f, ok := fluxRecordFor(key, flux); ok {
		detail.FluxHistory = f.history
	}
	return detail, nil
}
