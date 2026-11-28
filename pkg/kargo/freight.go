package kargo

import (
	"cmp"
	"slices"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// Image is one container image version a Freight carries.
type Image struct {
	RepoURL string
	Tag     string
	Digest  string
}

// Commit is one Git commit a Freight carries.
type Commit struct {
	RepoURL string
	ID      string
	Branch  string
	Tag     string
	Message string
}

// Chart is one Helm chart version a Freight carries.
type Chart struct {
	RepoURL string
	Name    string
	Version string
}

// Artifact is a version of another kind of artifact a Freight carries.
type Artifact struct {
	Type             string
	SubscriptionName string
	Version          string
}

// Versions is what a Freight carries: the content that identifies a version
// to a person, where the Freight's name is a content hash.
type Versions struct {
	Images    []Image
	Commits   []Commit
	Charts    []Chart
	Artifacts []Artifact
}

// FreightRef is a Freight as a Stage or Promotion records it: name, origin
// and versions, without the alias, which only the Freight object holds.
type FreightRef struct {
	Name   string
	Origin Origin
	Versions
}

// Freight is a Freight object's identity and content.
type Freight struct {
	Namespace string
	Name      string
	// Alias is the human name Kargo generates, such as "singing-octopus".
	Alias  string
	Origin Origin
	// DiscoveredAt is when the Warehouse found this content, falling back to
	// the object's creation, as Kargo orders Freight.
	DiscoveredAt time.Time
	Versions
}

// ParseFreight reads a Freight object.
func ParseFreight(u *unstructured.Unstructured) Freight {
	if u == nil {
		return Freight{}
	}
	discovered := timeAt(u.Object, "discoveredAt")
	if discovered.IsZero() {
		discovered = u.GetCreationTimestamp().Time
	}
	return Freight{
		Namespace:    u.GetNamespace(),
		Name:         u.GetName(),
		Alias:        Alias(u),
		Origin:       parseOrigin(object(u.Object, "origin")),
		DiscoveredAt: discovered,
		Versions:     parseVersions(u.Object),
	}
}

// Alias is a Freight's alias: the alias field, else the alias label, as
// Kargo's own UI reads it.
func Alias(u *unstructured.Unstructured) string {
	if u == nil {
		return ""
	}
	if alias := str(u.Object, "alias"); alias != "" {
		return alias
	}
	return u.GetLabels()[AliasLabel]
}

func parseFreightRef(m map[string]any) FreightRef {
	return FreightRef{Name: str(m, "name"), Origin: parseOrigin(object(m, "origin")), Versions: parseVersions(m)}
}

func parseVersions(m map[string]any) Versions {
	var v Versions
	for _, o := range objects(m, "images") {
		v.Images = append(v.Images, Image{RepoURL: str(o, "repoURL"), Tag: str(o, "tag"), Digest: str(o, "digest")})
	}
	for _, o := range objects(m, "commits") {
		v.Commits = append(v.Commits, Commit{RepoURL: str(o, "repoURL"), ID: str(o, "id"), Branch: str(o, "branch"), Tag: str(o, "tag"), Message: str(o, "message")})
	}
	for _, o := range objects(m, "charts") {
		v.Charts = append(v.Charts, Chart{RepoURL: str(o, "repoURL"), Name: str(o, "name"), Version: str(o, "version")})
	}
	for _, o := range objects(m, "artifacts") {
		v.Artifacts = append(v.Artifacts, Artifact{Type: str(o, "artifactType"), SubscriptionName: str(o, "subscriptionName"), Version: str(o, "version")})
	}
	return v
}

// Labels are short version labels in Kargo's order (images, commits,
// charts, other artifacts): "nginx:1.31.9", "app@v1.2.0" or "app@3f2a9c1",
// "podinfo 6.5.0".
func (v Versions) Labels() []string {
	var out []string
	for _, i := range v.Images {
		ref := i.Tag
		sep := ":"
		if ref == "" {
			ref, sep = shortDigest(i.Digest), "@"
		}
		out = append(out, lastSegment(i.RepoURL)+sep+ref)
	}
	for _, c := range v.Commits {
		ref := c.Tag
		if ref == "" {
			ref = shortSHA(c.ID)
		}
		out = append(out, strings.TrimSuffix(lastSegment(c.RepoURL), ".git")+"@"+ref)
	}
	for _, c := range v.Charts {
		name := c.Name
		if name == "" {
			name = lastSegment(c.RepoURL)
		}
		out = append(out, name+" "+c.Version)
	}
	for _, a := range v.Artifacts {
		out = append(out, a.SubscriptionName+" "+a.Version)
	}
	return out
}

// Empty reports whether the Freight carries nothing this package reads.
func (v Versions) Empty() bool {
	return len(v.Images) == 0 && len(v.Commits) == 0 && len(v.Charts) == 0 && len(v.Artifacts) == 0
}

func lastSegment(url string) string {
	url = strings.TrimRight(url, "/")
	if i := strings.LastIndex(url, "/"); i >= 0 {
		return url[i+1:]
	}
	return url
}

func shortSHA(id string) string {
	if len(id) > 7 {
		return id[:7]
	}
	return id
}

func shortDigest(digest string) string {
	algo, hex, ok := strings.Cut(digest, ":")
	if !ok {
		return shortSHA(digest)
	}
	if len(hex) > 12 {
		hex = hex[:12]
	}
	return algo + ":" + hex
}

// FreightIndex finds a project's Freight objects by name, for the alias,
// discovery time and status a Stage's record of a Freight doesn't carry.
type FreightIndex struct {
	parsed  map[string]Freight
	objects map[string]*unstructured.Unstructured
}

// IndexFreight indexes the Freight objects of one project.
func IndexFreight(items []*unstructured.Unstructured) FreightIndex {
	x := FreightIndex{parsed: make(map[string]Freight, len(items)), objects: make(map[string]*unstructured.Unstructured, len(items))}
	for _, u := range items {
		if Is(u, KindFreight) {
			x.parsed[u.GetName()] = ParseFreight(u)
			x.objects[u.GetName()] = u
		}
	}
	return x
}

// Get returns the named Freight.
func (x FreightIndex) Get(name string) (Freight, bool) {
	f, ok := x.parsed[name]
	return f, ok
}

// Object returns the named Freight object.
func (x FreightIndex) Object(name string) *unstructured.Unstructured {
	return x.objects[name]
}

// Newest is the index's Freight from origin, newest first by discovery time
// and then by name, descending, the order Kargo's auto-promotion picks in.
func (x FreightIndex) Newest(origin Origin) []Freight {
	var out []Freight
	for _, f := range x.parsed {
		if f.Origin == origin {
			out = append(out, f)
		}
	}
	slices.SortFunc(out, newestFirst)
	return out
}

func newestFirst(a, b Freight) int {
	if c := b.DiscoveredAt.Compare(a.DiscoveredAt); c != 0 {
		return c
	}
	// Kargo breaks a tie by name, descending (pkg/api/auto_promotion.go).
	return cmp.Compare(b.Name, a.Name)
}
