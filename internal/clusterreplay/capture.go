package clusterreplay

import (
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	aicontext "github.com/skyhook-io/radar/pkg/ai/context"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
)

type Header struct {
	CapturedAt  time.Time `json:"captured_at"`
	ContextHash string    `json:"context_hash"`
	Discovery   Discovery `json:"discovery"`
	// FailedGroups are the API groups discovery couldn't answer for when the
	// capture ran; their resources are missing from the capture.
	FailedGroups []string `json:"failed_groups,omitempty"`
}

// Skipped is a resource the capture couldn't list, so the replay lacks it.
type Skipped struct {
	Resource string `json:"resource"`
	Error    string `json:"error"`
}

// liveGroups serve values computed on every read rather than stored objects.
var liveGroups = map[string]bool{"metrics.k8s.io": true, "custom.metrics.k8s.io": true, "external.metrics.k8s.io": true}

type Discovery struct {
	Groups    []*metav1.APIGroup        `json:"groups"`
	Resources []*metav1.APIResourceList `json:"resources"`
}

func Listable(gv schema.GroupVersion, r metav1.APIResource) bool {
	if strings.Contains(r.Name, "/") || r.Kind == "Event" || liveGroups[gv.Group] || (gv.Group == "coordination.k8s.io" && r.Name == "leases") {
		return false
	}
	for _, v := range r.Verbs {
		if v == "list" {
			return true
		}
	}
	return false
}

// Capture writes every listable object discovery serves. A group discovery
// can't answer for, or a resource whose list fails, is left out and reported
// (in the header and in skipped) rather than failing the whole capture.
func Capture(ctx context.Context, d discovery.DiscoveryInterface, c dynamic.Interface, contextName string, dst io.Writer) (count int, skipped []Skipped, err error) {
	groups, resources, derr := d.ServerGroupsAndResources()
	var failedGroups []string
	if derr != nil {
		var partial *discovery.ErrGroupDiscoveryFailed
		if !errors.As(derr, &partial) || len(resources) == 0 {
			return 0, nil, fmt.Errorf("discovery: %w", derr)
		}
		for gv := range partial.Groups {
			if !slices.Contains(failedGroups, gv.Group) {
				failedGroups = append(failedGroups, gv.Group)
			}
		}
		slices.Sort(failedGroups)
	}
	digest := sha256.Sum256([]byte(contextName))
	z := gzip.NewWriter(dst)
	enc := json.NewEncoder(z)
	if err := enc.Encode(Header{time.Now().UTC(), hex.EncodeToString(digest[:]), Discovery{groups, resources}, failedGroups}); err != nil {
		return 0, nil, err
	}
	for _, list := range resources {
		gv, err := schema.ParseGroupVersion(list.GroupVersion)
		if err != nil {
			return count, skipped, err
		}
		for _, r := range list.APIResources {
			if !Listable(gv, r) {
				continue
			}
			gvr := gv.WithResource(r.Name)
			token := ""
			for {
				page, err := c.Resource(gvr).List(ctx, metav1.ListOptions{Limit: 500, Continue: token})
				if err != nil {
					if ctx.Err() != nil {
						return count, skipped, ctx.Err()
					}
					skipped = append(skipped, Skipped{Resource: gvr.String(), Error: err.Error()})
					break
				}
				for _, obj := range page.Items {
					obj.SetAPIVersion(gv.String())
					obj.SetKind(r.Kind)
					Sanitize(obj.Object)
					if err := enc.Encode(obj.Object); err != nil {
						return count, skipped, err
					}
					count++
				}
				next := page.GetContinue()
				if next == "" {
					break
				}
				if next == token {
					return count, skipped, fmt.Errorf("repeated pagination token for %s", gvr)
				}
				token = next
			}
		}
	}
	return count, skipped, z.Close()
}

func Sanitize(obj map[string]any) {
	kind, _ := obj["kind"].(string)
	if kind == "Secret" || kind == "ConfigMap" {
		for _, field := range []string{"data", "stringData", "binaryData"} {
			if values, ok := obj[field].(map[string]any); ok {
				for key := range values {
					values[key] = ""
				}
			}
		}
	}
	scrub(obj)
	aicontext.RedactInlineSecrets(obj)
}

func scrub(value any) {
	switch v := value.(type) {
	case map[string]any:
		if metadata, ok := v["metadata"].(map[string]any); ok {
			delete(metadata, "managedFields")
			if annotations, ok := metadata["annotations"].(map[string]any); ok {
				delete(annotations, "kubectl.kubernetes.io/last-applied-configuration")
			}
		}
		for key, child := range v {
			if key == "env" {
				if env, ok := child.([]any); ok {
					for _, e := range env {
						if entry, ok := e.(map[string]any); ok {
							if _, exists := entry["value"]; exists {
								entry["value"] = ""
							}
						}
					}
				}
			}
			scrub(child)
		}
	case []any:
		for _, child := range v {
			scrub(child)
		}
	}
}

func Read(src io.Reader, visit func(*unstructured.Unstructured) error) (Header, error) {
	var header Header
	z, err := gzip.NewReader(src)
	if err != nil {
		return header, err
	}
	defer z.Close()
	dec := json.NewDecoder(z)
	if err := dec.Decode(&header); err != nil {
		return header, fmt.Errorf("header: %w", err)
	}
	if header.CapturedAt.IsZero() || header.Discovery.Resources == nil {
		return header, fmt.Errorf("missing capture header/discovery")
	}
	for n := 2; ; n++ {
		var obj unstructured.Unstructured
		if err := dec.Decode(&obj.Object); err == io.EOF {
			return header, nil
		} else if err != nil {
			return header, fmt.Errorf("record %d: %w", n, err)
		}
		if obj.GetAPIVersion() == "" || obj.GetKind() == "" || obj.GetName() == "" {
			return header, fmt.Errorf("record %d: missing resource identity", n)
		}
		if err := visit(&obj); err != nil {
			return header, fmt.Errorf("record %d: %w", n, err)
		}
	}
}
