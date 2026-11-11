// Package fieldwriters reads metadata.managedFields: which tool last wrote
// which field of an object, and whether that tool is the object's GitOps
// controller, a person's tool (kubectl, Lens, Radar…) or another controller.
package fieldwriters

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

type Class string

const (
	ClassOwner Class = "owner"
	ClassHand  Class = "hand"
	ClassOther Class = "other"
)

// MaxPaths caps the paths reported for one managedFields entry.
const MaxPaths = 12

var handTools = map[string]string{
	"node-fetch": "Lens", // Lens writes through Node's fetch client
	"k9s":        "k9s",
	"OpenLens":   "OpenLens",
	"Lens":       "Lens",
	"Headlamp":   "Headlamp",
	"dashboard":  "dashboard",
}

// Classify sorts a field manager. owners are the root's controller managers;
// extraHand are Radar's own write managers.
func Classify(manager string, owners, extraHand map[string]bool) Class {
	switch {
	case owners[manager]:
		return ClassOwner
	case extraHand[manager], strings.HasPrefix(manager, "kubectl"):
		return ClassHand
	}
	if _, ok := handTools[manager]; ok {
		return ClassHand
	}
	return ClassOther
}

// ToolName is how a person would name the tool behind a manager.
func ToolName(manager string, extraHand map[string]bool) string {
	switch {
	case extraHand[manager]:
		return "Radar"
	case manager == "kubectl-client-side-apply":
		return "kubectl apply"
	case strings.HasPrefix(manager, "kubectl-"):
		return "kubectl " + strings.TrimPrefix(manager, "kubectl-")
	}
	if name, ok := handTools[manager]; ok {
		return name
	}
	return manager
}

var harmless = []struct {
	match func(string) bool
	label string
}{
	{func(p string) bool {
		return strings.HasSuffix(p, `metadata.annotations["kubectl.kubernetes.io/restartedAt"]`)
	}, "kubectl rollout restart"},
	{func(p string) bool { return p == "metadata.labels.k8slens-edit-resource-version" }, "Lens edit marker"},
	{func(p string) bool { return strings.HasPrefix(p, `metadata.annotations["reconcile.fluxcd.io/`) }, "Flux reconcile request"},
}

// SplitHarmless drops paths that record an action rather than a change of
// desired state (a rollout restart, Lens's edit marker, a Flux reconcile
// request). label names the first kind dropped, "" when nothing was.
func SplitHarmless(paths []string) (kept []string, label string) {
	for _, p := range paths {
		dropped := false
		for _, h := range harmless {
			if h.match(p) {
				dropped = true
				if label == "" {
					label = h.label
				}
				break
			}
		}
		if !dropped {
			kept = append(kept, p)
		}
	}
	return kept, label
}

var plainKey = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// LeafPaths flattens a fieldsV1 set into sorted leaf paths in the form
// `a.b["x/y"][name=app].c`. A node with no field or list-key children is a
// leaf; "." (the element itself), "v:" (set values) and "i:" (indexes) add
// no path of their own.
func LeafPaths(raw []byte) ([]string, error) {
	var root map[string]any
	if err := json.Unmarshal(raw, &root); err != nil {
		return nil, err
	}
	var out []string
	var walk func(node map[string]any, path string) error
	walk = func(node map[string]any, path string) error {
		children := 0
		for member, child := range node {
			var next string
			switch {
			case strings.HasPrefix(member, "f:"):
				name := strings.TrimPrefix(member, "f:")
				if plainKey.MatchString(name) {
					if path == "" {
						next = name
					} else {
						next = path + "." + name
					}
				} else {
					next = fmt.Sprintf("%s[%q]", path, name)
				}
			case strings.HasPrefix(member, "k:"):
				key, err := listKey(strings.TrimPrefix(member, "k:"))
				if err != nil {
					return err
				}
				next = path + "[" + key + "]"
			default:
				continue
			}
			children++
			m, _ := child.(map[string]any)
			if err := walk(m, next); err != nil {
				return err
			}
		}
		if children == 0 && path != "" {
			out = append(out, path)
		}
		return nil
	}
	if err := walk(root, ""); err != nil {
		return nil, err
	}
	sort.Strings(out)
	return out, nil
}

// listKey renders a k:{"name":"engine","port":80} member as name=engine,port=80.
func listKey(raw string) (string, error) {
	var fields map[string]any
	if err := json.Unmarshal([]byte(raw), &fields); err != nil {
		return "", fmt.Errorf("list key %s: %w", raw, err)
	}
	parts := make([]string, 0, len(fields))
	for k, v := range fields {
		parts = append(parts, fmt.Sprintf("%s=%v", k, v))
	}
	sort.Strings(parts)
	return strings.Join(parts, ","), nil
}
