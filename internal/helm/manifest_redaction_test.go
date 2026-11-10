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
		"+  dbUrl: '[REDACTED]'",
		"  password: ",
		"-  replicas: \"1\"",
		"+  replicas: \"2\"",
		"# Source: app/templates/secret.yaml",
	} {
		if !strings.Contains(diff, want) {
			t.Errorf("diff missing %q:\n%s", want, diff)
		}
	}
}

func TestRedactSecretManifestsKeepsOnlySourceComments(t *testing.T) {
	const source = "# Source: chart/templates/secret.yaml"
	for _, comments := range []string{
		"# token: short-secret-123\n" + source,
		source + "\n# token: short-secret-123",
	} {
		manifest := comments + "\napiVersion: v1\nkind: Secret\nstringData:\n  password: secret-value\n"
		got := redactSecretManifests(manifest)
		for _, leak := range []string{"# token:", "short-secret-123", "secret-value"} {
			if strings.Contains(got, leak) {
				t.Errorf("redacted Secret contains %q:\n%s", leak, got)
			}
		}
		if !strings.Contains(got, source) {
			t.Errorf("redacted Secret missing source comment:\n%s", got)
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

func TestRedactSecretManifestsCoversCopiesAndLists(t *testing.T) {
	cases := map[string]string{
		"last-applied copy": `apiVersion: v1
kind: Secret
metadata:
  name: db
  annotations:
    kubectl.kubernetes.io/last-applied-configuration: '{"kind":"Secret","data":{"password":"czNjcjN0LXZhbHVl"}}'
data:
  password: czNjcjN0LXZhbHVl
`,
		"Secret inside a List": `apiVersion: v1
kind: List
items:
  - apiVersion: v1
    kind: Secret
    metadata:
      name: db
    stringData:
      password: s3cr3t-value
`,
		"kind-less items in a SecretList": `apiVersion: v1
kind: SecretList
items:
  - metadata:
      name: db
    data:
      password: czNjcjN0LXZhbHVl
  - metadata:
      name: api
    stringData:
      password: s3cr3t-value
`,
		"unparseable with a trailing comment": "apiVersion: v1\nkind: Secret # credential\ndata:\n  password: [unclosed s3cr3t-value\n",
	}
	for name, manifest := range cases {
		t.Run(name, func(t *testing.T) {
			got := redactSecretManifests(manifest)
			for _, leak := range []string{"s3cr3t-value", "czNjcjN0LXZhbHVl"} {
				if strings.Contains(got, leak) {
					t.Fatalf("Secret value leaked: %q", got)
				}
			}
		})
	}
}

func TestRedactSecretManifestsPreservesConfigMapInMixedLists(t *testing.T) {
	const manifest = `apiVersion: v1
kind: List
items:
  - apiVersion: v1
    kind: Secret
    metadata:
      name: db
    data:
      password: czNjcjN0LXZhbHVl
    stringData:
      password: s3cr3t-value
  - apiVersion: v1
    kind: ConfigMap
    metadata:
      name: config
    data:
      setting: config-value
`
	for _, kind := range []string{"List", "SecretList"} {
		t.Run(kind, func(t *testing.T) {
			got := redactSecretManifests(strings.Replace(manifest, "kind: List", "kind: "+kind, 1))
			for _, leak := range []string{"czNjcjN0LXZhbHVl", "s3cr3t-value"} {
				if strings.Contains(got, leak) {
					t.Errorf("Secret value leaked: %q", got)
				}
			}
			if strings.Count(got, "[REDACTED]") != 2 {
				t.Errorf("want two redacted Secret values: %q", got)
			}
			if !strings.Contains(got, "setting: config-value") {
				t.Errorf("ConfigMap value missing: %q", got)
			}
		})
	}
}
