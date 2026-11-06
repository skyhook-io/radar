package helm

import (
	"context"
	"errors"
	"slices"
	"sort"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/search"
	"github.com/skyhook-io/radar/pkg/gitops"
	"github.com/skyhook-io/radar/pkg/k8score"
	"github.com/skyhook-io/radar/pkg/packages"
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

	// A chart that follows `helm create` truncates this label to 63
	// characters, so a label of that length may have lost part of its version.
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
	// that Helm stores this release in another namespace.
	StorageNamespace string `json:"storageNamespace,omitempty"`
	Chart            string `json:"chart,omitempty"`
	ChartVersion     string `json:"chartVersion,omitempty"`
	AppVersion       string `json:"appVersion,omitempty"`
	// OtherCharts lists the other helm.sh/chart labels on the release's
	// objects, as written. When Chart is empty they are the candidates Radar
	// wouldn't choose between; otherwise they are normally subcharts.
	OtherCharts []string `json:"otherCharts,omitempty"`
	// ResourceCount is how many of the caller's readable objects carry the
	// release's stamps, which can be fewer than the release has.
	ResourceCount  int    `json:"resourceCount"`
	ResourceHealth string `json:"resourceHealth,omitempty"`
	HealthIssue    string `json:"healthIssue,omitempty"`
	HealthSummary  string `json:"healthSummary,omitempty"`
	// ManagedByFluxHelmRelease is "namespace/name" of a HelmRelease the caller
	// can read that records this release.
	ManagedByFluxHelmRelease string `json:"managedByFluxHelmRelease,omitempty"`
	// Resources and FluxHistory are filled on the single-release read only.
	Resources   []OwnedResource `json:"resources,omitempty"`
	FluxHistory []FluxRevision  `json:"fluxHistory,omitempty"`
}

// FluxRevision is one entry of a Flux HelmRelease's status.history: what Flux
// recorded, not what Helm's storage says now.
type FluxRevision struct {
	Revision      int        `json:"revision"`
	Status        string     `json:"status,omitempty"`
	ChartName     string     `json:"chartName,omitempty"`
	ChartVersion  string     `json:"chartVersion,omitempty"`
	AppVersion    string     `json:"appVersion,omitempty"`
	Digest        string     `json:"digest,omitempty"`
	ConfigDigest  string     `json:"configDigest,omitempty"`
	FirstDeployed *time.Time `json:"firstDeployed,omitempty"`
	LastDeployed  *time.Time `json:"lastDeployed,omitempty"`
}

// ObservedReleaseDetail is one observed release and what the read covered.
type ObservedReleaseDetail struct {
	ObservedRelease
	ObjectCoverage resourceapi.SearchCoverage `json:"objectCoverage"`
}

// ObjectScope is a caller's view of the cached objects, built by each surface
// from its own per-user checks.
type ObjectScope struct {
	Provider *search.CacheProvider
	Access   search.ScanAccess
	// Requested is what the caller narrowed to; see RequestedNamespaces.
	Requested []string
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

func warmFluxHelmReleases(ctx context.Context, provider *search.CacheProvider) {
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
			Revision:      int(revision),
			Status:        text("status"),
			ChartName:     text("chartName"),
			ChartVersion:  text("chartVersion"),
			AppVersion:    text("appVersion"),
			Digest:        text("digest"),
			ConfigDigest:  text("configDigest"),
			FirstDeployed: parseFluxTime(text("firstDeployed")),
			LastDeployed:  parseFluxTime(text("lastDeployed")),
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
	row.Chart, row.ChartVersion, row.AppVersion, row.OtherCharts = chooseChart(key.name, objects)
	for _, f := range flux {
		if f.releaseNamespace != key.namespace || f.releaseName != key.name {
			continue
		}
		row.ManagedByFluxHelmRelease = f.ref
		if f.storageNamespace != key.namespace {
			row.StorageNamespace = f.storageNamespace
		}
		if f.historyChart != "" {
			row.Chart, row.ChartVersion, row.AppVersion = f.historyChart, f.historyVersion, f.historyAppVersion
			row.OtherCharts = slices.DeleteFunc(allChartLabels(objects), func(label string) bool {
				name, _ := parseChartLabel(label)
				return name == f.historyChart
			})
		}
		break
	}
	return row
}

// chooseChart names the release's chart from its objects' labels. Subcharts
// label their own objects, and they often outnumber the parent's (21 clickhouse
// objects to 8 signoz in one real release), so a count can't identify the
// parent. With several chart names, only one named exactly like the release is
// chosen; otherwise none is, and all are returned for the reader to see.
func chooseChart(release string, objects []stampedObject) (chart, version, appVersion string, others []string) {
	names := map[string]bool{}
	for _, o := range objects {
		if o.chartLabel != "" {
			name, _ := parseChartLabel(o.chartLabel)
			names[name] = true
		}
	}
	switch {
	case len(names) == 1:
		for name := range names {
			chart = name
		}
	case names[release]:
		chart = release
	default:
		return "", "", "", allChartLabels(objects)
	}
	versions := map[string]int{}
	appVersions := map[string]int{}
	for _, o := range objects {
		if o.chartLabel == "" {
			continue
		}
		name, v := parseChartLabel(o.chartLabel)
		if name != chart {
			others = append(others, o.chartLabel)
			continue
		}
		if v != "" {
			versions[v]++
		}
		if o.appVersion != "" {
			appVersions[o.appVersion]++
		}
	}
	slices.Sort(others)
	return chart, mostCommon(versions), mostCommon(appVersions), slices.Compact(others)
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
// at the length limit may be cut off, so its version isn't trusted.
func parseChartLabel(label string) (name, version string) {
	name, version = packages.SplitChart(label)
	if len(label) >= maxChartLabelLength {
		return name, ""
	}
	return name, strings.ReplaceAll(version, "_", "+")
}

// mostCommon returns the value seen most, or "" on a tie: a tie is two
// answers, and picking one would be a guess.
func mostCommon(counts map[string]int) string {
	best, bestCount, tied := "", 0, false
	for value, count := range counts {
		switch {
		case count > bestCount:
			best, bestCount, tied = value, count, false
		case count == bestCount:
			tied = true
		}
	}
	if tied {
		return ""
	}
	return best
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
		if k8score.IsBuiltInAPIGroup(o.group) {
			one := []OwnedResource{resource}
			enrichResourcesWithStatus(one)
			resource = one[0]
		}
		resources = append(resources, resource)
	}
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

// GetObservedRelease reads one release from its stamps, with its resources and
// any Flux history. It returns nil when no object the caller can read carries
// the release's stamps.
func GetObservedRelease(ctx context.Context, scope *ObjectScope, namespace, name string) (*ObservedReleaseDetail, error) {
	scan, err := scanObservedReleases(ctx, scope)
	if err != nil {
		return nil, err
	}
	key := releaseKey{namespace, name}
	objects := groupStamped(scan.stamped)[key]
	if len(objects) == 0 {
		if !scope.Provider.Current() {
			return nil, ErrConnectionChanged
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
	detail := &ObservedReleaseDetail{ObservedRelease: observedRow(key, objects, scan.flux), ObjectCoverage: scan.coverage}
	detail.Resources = addObservedHealth(&detail.ObservedRelease, objects, scan.coverage.Status != resourceapi.CoverageComplete)
	// Health reads the current cache, so this has to come after it.
	if !scope.Provider.Current() {
		return nil, ErrConnectionChanged
	}
	for _, f := range scan.flux {
		if f.releaseNamespace == namespace && f.releaseName == name {
			detail.FluxHistory = f.history
			break
		}
	}
	return detail, nil
}
