package helm

import (
	"encoding/json"
	"strings"
	"testing"
)

const redactionManifestRev1 = `---
# Source: app/templates/secret.yaml
apiVersion: v1
kind: Secret
metadata:
  name: app-creds
type: Opaque
data:
  password: b2xkLXBhc3N3b3JkLXZhbHVl
stringData:
  apiKey: old-api-key-value
---
# Source: app/templates/configmap.yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: app-config
data:
  replicas: "1"`

const redactionManifestRev2 = `---
# Source: app/templates/secret.yaml
apiVersion: v1
kind: Secret
metadata:
  name: app-creds
type: Opaque
data:
  password: bmV3LXBhc3N3b3JkLXZhbHVl
  dbUrl: cG9zdGdyZXM6Ly91c2VyOnB3QGRi
stringData:
  apiKey: new-api-key-value
---
# Source: app/templates/configmap.yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: app-config
data:
  replicas: "2"
  note: "token ghp_ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghij"`

var redactionSecretValues = []string{
	"b2xkLXBhc3N3b3JkLXZhbHVl", "bmV3LXBhc3N3b3JkLXZhbHVl", "cG9zdGdyZXM6Ly91c2VyOnB3QGRi",
	"old-api-key-value", "new-api-key-value", "ghp_ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghij",
}

func TestComputeRedactedManifestDiffHidesSecretValuesAndKeepsTheRest(t *testing.T) {
	diff := computeRedactedManifestDiff(redactionManifestRev1, redactionManifestRev2, 1, 2)

	for _, v := range redactionSecretValues {
		if strings.Contains(diff, v) {
			t.Errorf("diff leaks %q:\n%s", v, diff)
		}
	}
	for _, want := range []string{
		"+  dbUrl: '[REDACTED]'", // a key added to the Secret still shows
		"  password: ",           // an unchanged key stays as context
		"-  replicas: \"1\"",     // non-Secret documents diff as before
		"+  replicas: \"2\"",
		"# Source: app/templates/secret.yaml",
	} {
		if !strings.Contains(diff, want) {
			t.Errorf("diff missing %q:\n%s", want, diff)
		}
	}
}

func TestRedactSecretManifestsLeavesNonSecretDocumentsUntouched(t *testing.T) {
	manifest := "---\n# Source: a.yaml\napiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: c\ndata:\n  k:   \"spaced\"   # comment\n"
	if got := redactSecretManifests(manifest); got != manifest {
		t.Fatalf("non-Secret manifest changed:\n%q\nwant\n%q", got, manifest)
	}
}

func TestRedactSecretManifestsBlanksUnparseableSecret(t *testing.T) {
	manifest := "apiVersion: v1\nkind: Secret\ndata:\n  password: [unclosed s3cr3t-value\n"
	got := redactSecretManifests(manifest)
	if strings.Contains(got, "s3cr3t-value") {
		t.Fatalf("unparseable Secret leaked: %q", got)
	}
}

// Regression pin: the structured resource_diff already masks Secret values,
// so get_helm_release include=resource_diff needs no extra redaction.
func TestResourceDiffMasksSecretValues(t *testing.T) {
	left, _ := parseManifestResourceObjects(redactionManifestRev1, "default")
	right, _ := parseManifestResourceObjects(redactionManifestRev2, "default")
	_, _, common := diffResourceRefs(resourceRefsFromRendered(left), resourceRefsFromRendered(right))
	modified, _ := diffRenderedResourceObjects(common, left, right)

	var secretChanged bool
	for _, change := range modified {
		if change.Kind == "Secret" {
			secretChanged = true
		}
	}
	if !secretChanged {
		t.Fatalf("want a modified Secret in %#v", modified)
	}
	b, err := json.Marshal(modified)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range redactionSecretValues[:5] {
		if strings.Contains(string(b), v) {
			t.Errorf("resource diff leaks %q: %s", v, b)
		}
	}
}
