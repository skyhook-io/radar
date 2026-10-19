package helm

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"

	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/release"
)

// releaseSummary is what the release list and issue detection read from one
// stored release revision.
type releaseSummary struct {
	// release holds the name, namespace, version, info times, description
	// and status, and chart name, version and app version. It has no
	// manifest, values, chart files, hooks or labels. Readers copy it before
	// setting labels.
	release *release.Release

	// resources lists the objects the manifest declares, with the namespace
	// as written there. Only recorded for revisions that are not superseded,
	// since only a release's newest revision is checked for resource health;
	// resourcesRecorded says whether it was.
	resources         []ResourceRef
	resourcesRecorded bool

	// skip marks a Secret a full read skips: no release value, or one that
	// doesn't decode.
	skip bool

	// tooLarge marks a release past the limits below. Readers read it in
	// full every time instead.
	tooLarge bool
}

// Anyone who may create a Secret can write one that looks like a release,
// and summaries outlive the read that made them, so summarizing bounds what
// it decodes and what it keeps. Kubernetes caps a Secret at 1MiB, but gzip
// can expand that a thousandfold. Real releases stay far below both limits.
const (
	maxSummarizedRelease = 16 << 20
	maxSummarySize       = 64 << 10
)

var errReleaseTooLarge = errors.New("release too large to summarize")

// decodeReleaseJSON returns the JSON encoding of the Helm release stored in a
// release Secret's value: base64, usually gzip-compressed. Past limit bytes
// of JSON it returns errReleaseTooLarge without reading further. A negative
// limit reads it all.
func decodeReleaseJSON(value []byte, limit int64) ([]byte, error) {
	b := make([]byte, base64.StdEncoding.DecodedLen(len(value)))
	n, err := base64.StdEncoding.Decode(b, value)
	if err != nil {
		return nil, err
	}
	b = b[:n]
	if len(b) <= 3 || !bytes.Equal(b[0:3], []byte{0x1f, 0x8b, 0x08}) {
		if limit >= 0 && int64(len(b)) > limit {
			return nil, errReleaseTooLarge
		}
		return b, nil
	}
	gz, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	defer gz.Close()
	var r io.Reader = gz
	if limit >= 0 {
		r = io.LimitReader(gz, limit+1)
	}
	b, err = io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	if limit >= 0 && int64(len(b)) > limit {
		return nil, errReleaseTooLarge
	}
	return b, nil
}

// summarizeRelease summarizes a release Secret's release value, decoding it
// exactly as a full read does. The error says why a value a full read would
// also skip didn't decode.
func summarizeRelease(value []byte) (releaseSummary, error) {
	if len(value) == 0 {
		return releaseSummary{skip: true}, nil
	}
	raw, err := decodeReleaseJSON(value, maxSummarizedRelease)
	if errors.Is(err, errReleaseTooLarge) {
		return releaseSummary{tooLarge: true}, nil
	}
	if err != nil {
		return releaseSummary{skip: true}, err
	}
	var rel release.Release
	if err := json.Unmarshal(raw, &rel); err != nil {
		return releaseSummary{skip: true}, err
	}
	summary := releaseSummary{release: &release.Release{Name: rel.Name, Namespace: rel.Namespace, Version: rel.Version}}
	if rel.Info != nil {
		summary.release.Info = &release.Info{
			LastDeployed: rel.Info.LastDeployed,
			Deleted:      rel.Info.Deleted,
			Description:  rel.Info.Description,
			Status:       rel.Info.Status,
		}
	}
	if rel.Chart != nil {
		summary.release.Chart = &chart.Chart{}
		if md := rel.Chart.Metadata; md != nil {
			summary.release.Chart.Metadata = &chart.Metadata{Name: md.Name, Version: md.Version, AppVersion: md.AppVersion}
		}
	}
	if rel.Info == nil || rel.Info.Status != release.StatusSuperseded {
		// No default namespace: the one a resource lands in depends on
		// whether its kind is cluster-scoped, which readers decide when
		// they read the summary.
		rendered, _ := parseManifestResourceObjects(rel.Manifest, "")
		summary.resources = make([]ResourceRef, 0, len(rendered))
		for _, r := range rendered {
			summary.resources = append(summary.resources, r.Ref)
		}
		summary.resourcesRecorded = true
	}
	if summary.size() > maxSummarySize {
		return releaseSummary{tooLarge: true}, nil
	}
	return summary, nil
}

// size approximates the bytes a summary keeps.
func (s releaseSummary) size() int {
	n := 0
	if rel := s.release; rel != nil {
		n += len(rel.Name) + len(rel.Namespace)
		if rel.Info != nil {
			n += len(rel.Info.Description) + len(rel.Info.Status)
		}
		if rel.Chart != nil && rel.Chart.Metadata != nil {
			n += len(rel.Chart.Metadata.Name) + len(rel.Chart.Metadata.Version) + len(rel.Chart.Metadata.AppVersion)
		}
	}
	for _, r := range s.resources {
		n += len(r.APIVersion) + len(r.Kind) + len(r.Name) + len(r.Namespace)
	}
	return n
}

// releaseWithLabels returns a copy of the summarized release carrying the
// Secret's labels, as a full read restores them.
func (s releaseSummary) releaseWithLabels(labels map[string]string) *release.Release {
	rel := *s.release
	rel.Labels = labels
	return &rel
}

// ownedResources resolves the summary's resources as a full read of the
// release's manifest does.
func (s releaseSummary) ownedResources(releaseNamespace string) []OwnedResource {
	refs := make([]ResourceRef, 0, len(s.resources))
	for _, r := range s.resources {
		r.Namespace = manifestResourceNamespace(r.Kind, r.APIVersion, r.Namespace, releaseNamespace)
		refs = append(refs, r)
	}
	return ownedResourcesFromRefs(refs)
}
