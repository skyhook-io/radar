package context

import (
	"encoding/json"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/skyhook-io/radar/pkg/prune"
	"github.com/skyhook-io/radar/pkg/resourceid"
)

// minifyDetail strips metadata noise and per-type status noise, but keeps full spec
// and all annotations/labels. For MCP get_resource and YAML review.
func minifyDetail(obj runtime.Object) (map[string]any, error) {
	// Handle Secrets specially — never include data/stringData
	if secret, ok := obj.(*corev1.Secret); ok {
		return minifySecretDetail(secret), nil
	}

	data, err := json.Marshal(obj)
	if err != nil {
		return nil, err
	}

	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}

	pruneMapDetail(m)
	return m, nil
}

// minifyDetailUnstructured applies Detail-level pruning to an unstructured resource.
func minifyDetailUnstructured(obj map[string]any) map[string]any {
	pruneMapDetail(obj)
	redactUnstructuredSecrets(obj)
	return obj
}

// redactUnstructuredSecrets redacts inline secret-shaped values in a CRD's
// spec/status. Core Secret bodies are handled structurally elsewhere; this is
// the only value-level pass that reaches arbitrary CRD specs (e.g. a Traefik
// Middleware basicAuth user) before they go to an LLM.
func redactUnstructuredSecrets(obj map[string]any) {
	redactUpgradePlanEnvs(obj)
	if spec, ok := obj["spec"]; ok {
		RedactInlineSecrets(spec)
	}
	if status, ok := obj["status"]; ok {
		RedactInlineSecrets(status)
	}
	redactSecretFindings(obj)
}

// redactUpgradePlanEnvs blanks every env value of a system-upgrade-controller
// Plan (spec.upgrade.envs, spec.prepare.envs): they are passed to upgrade Jobs
// and can be credentials under any name, so pattern redaction is not enough.
// Names stay, so the model still sees what is configured.
func redactUpgradePlanEnvs(obj map[string]any) {
	apiVersion, _ := obj["apiVersion"].(string)
	if resourceid.GroupFromAPIVersion(apiVersion) != "upgrade.cattle.io" {
		return
	}
	spec, _ := obj["spec"].(map[string]any)
	for _, container := range []string{"upgrade", "prepare"} {
		c, _ := spec[container].(map[string]any)
		envs, _ := c["envs"].([]any)
		for _, e := range envs {
			if env, ok := e.(map[string]any); ok {
				if _, has := env["value"]; has {
					env["value"] = "[REDACTED]"
				}
			}
		}
	}
}

// redactSecretFindings blanks the matched text of secret-scan findings (Trivy
// Operator's ExposedSecretReport: report.secrets[].match, plus the code lines
// around it). The finding's rule, target and severity stay: they say what was
// found where without repeating the secret.
func redactSecretFindings(obj map[string]any) {
	report, _ := obj["report"].(map[string]any)
	secrets, _ := report["secrets"].([]any)
	for _, item := range secrets {
		finding, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if _, ok := finding["match"]; ok {
			finding["match"] = "[REDACTED]"
		}
		delete(finding, "code")
	}
}

func pruneMapDetail(m map[string]any) {
	// Metadata/spec/container key drops via the shared profiles (annotations
	// NOT filtered at Detail level). Per-container conditional pruning
	// (imagePullPolicy rule, env redaction) can't be a path drop — below.
	prune.ApplyInPlace(m, detailBaseProfile)
	pruneSpecElements(m)

	kind, _ := m["kind"].(string)
	if p, ok := statusProfileByKind[strings.ToLower(kind)]; ok {
		prune.ApplyInPlace(m, p)
	}
	decodeCertificateSigningRequest(m)
}

// pruneSpecElements handles what the shared profiles can't: conditional
// per-element pruning inside container slices (unconditional key drops ride
// detailBaseProfile's Drop + ElementDrops).
func pruneSpecElements(m map[string]any) {
	forEachContainer(m, pruneContainerConditional)
	sanitizeSpecEnvLists(m, false)
}

func minifySecretDetail(secret *corev1.Secret) map[string]any {
	result := map[string]any{
		"kind":      "Secret",
		"name":      secret.Name,
		"namespace": secret.Namespace,
		"type":      string(secret.Type),
		"keys":      secretKeyNames(secret),
	}

	if len(secret.Labels) > 0 {
		result["labels"] = secret.Labels
	}
	if len(secret.Annotations) > 0 {
		result["annotations"] = secret.Annotations
	}

	return result
}
