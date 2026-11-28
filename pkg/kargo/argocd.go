package kargo

import (
	"encoding/json"
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// ArgoCDApps are the Argo CD Applications a Stage's last Promotion updated
// or waited on, from its argocd-context annotation: a JSON list of
// {name, namespace}. Kargo removes the annotation when the Promotion touched
// none. An entry without a name is skipped; a malformed annotation returns
// an error and no Applications.
func ArgoCDApps(stage *unstructured.Unstructured) ([]ObjectRef, error) {
	if stage == nil {
		return nil, nil
	}
	raw := stage.GetAnnotations()[ArgoCDContextAnnotation]
	if raw == "" {
		return nil, nil
	}
	var apps []struct {
		Name      string `json:"name"`
		Namespace string `json:"namespace"`
	}
	if err := json.Unmarshal([]byte(raw), &apps); err != nil {
		return nil, fmt.Errorf("annotation %s: %w", ArgoCDContextAnnotation, err)
	}
	var out []ObjectRef
	for _, app := range apps {
		ref := ObjectRef{Namespace: app.Namespace, Name: app.Name}
		if ref.Name == "" {
			continue
		}
		duplicate := false
		for _, seen := range out {
			duplicate = duplicate || seen == ref
		}
		if !duplicate {
			out = append(out, ref)
		}
	}
	return out, nil
}

// StageID names a Stage by project (its namespace) and name.
type StageID struct {
	Project string
	Stage   string
}

// AuthorizedStages parses an Argo CD Application's authorized-stage
// annotation, a comma-separated list of "<project>:<stage>". It names the
// Stages allowed to update the Application, which says nothing about whether
// any of them has. Kargo rejects glob entries, and so does this.
func AuthorizedStages(value string) ([]StageID, error) {
	var out []StageID
	for entry := range strings.SplitSeq(value, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		project, stage, ok := strings.Cut(entry, ":")
		project, stage = strings.TrimSpace(project), strings.TrimSpace(stage)
		if !ok || project == "" || stage == "" || strings.Contains(entry, "*") {
			return nil, fmt.Errorf("invalid authorized Stage %q: want <project>:<stage>", entry)
		}
		out = append(out, StageID{Project: project, Stage: stage})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no authorized Stages in %q", value)
	}
	return out, nil
}
