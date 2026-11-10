package helm

import (
	"strings"
	"testing"
)

func TestComputeRedactedValuesDiff(t *testing.T) {
	for _, allValues := range []bool{false, true} {
		name := "user-supplied"
		if allValues {
			name = "computed"
		}
		t.Run(name, func(t *testing.T) {
			left := map[string]any{
				"dbPassword":                "OLD_PASSWORD_SENTINEL",
				"apiKey":                    "sk-live-OLD_APIKEY_SENTINEL",
				"auth":                      map[string]any{"postgresPassword": "OLD_NESTED_SENTINEL"},
				"image":                     map[string]any{"tag": "1.0.0"},
				"replicaCount":              1,
				"existingSecretPasswordKey": "old-password-key",
			}
			right := map[string]any{
				"dbPassword":                "NEW_PASSWORD_SENTINEL",
				"apiKey":                    "sk-live-NEW_APIKEY_SENTINEL",
				"auth":                      map[string]any{"postgresPassword": "NEW_NESTED_SENTINEL"},
				"credentials":               []any{"NEW_CREDENTIALS_SENTINEL"},
				"image":                     map[string]any{"tag": "2.0.0"},
				"replicaCount":              2,
				"existingSecretPasswordKey": "new-password-key",
			}
			values1, values2 := &HelmValues{UserSupplied: left}, &HelmValues{UserSupplied: right}
			if allValues {
				values1 = &HelmValues{Computed: left}
				values2 = &HelmValues{Computed: right}
			}
			rawDiff, err := computeValuesDiff(values1, values2, 1, 2, allValues)
			if err != nil {
				t.Fatal(err)
			}
			for _, sentinel := range []string{"OLD_PASSWORD_SENTINEL", "NEW_PASSWORD_SENTINEL", "sk-live-OLD_APIKEY_SENTINEL", "sk-live-NEW_APIKEY_SENTINEL", "OLD_NESTED_SENTINEL", "NEW_NESTED_SENTINEL", "NEW_CREDENTIALS_SENTINEL"} {
				if !strings.Contains(rawDiff, sentinel) {
					t.Fatalf("unredacted diff missing %q:\n%s", sentinel, rawDiff)
				}
			}
			diff, err := computeRedactedValuesDiff(values1, values2, 1, 2, allValues)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(diff, "SENTINEL") {
				t.Fatalf("redacted diff leaks a revision's credential:\n%s", diff)
			}
			for _, want := range []string{"[REDACTED]", "-replicaCount: 1", "+replicaCount: 2", "-  tag: 1.0.0", "+  tag: 2.0.0", "-existingSecretPasswordKey: old-password-key", "+existingSecretPasswordKey: new-password-key"} {
				if !strings.Contains(diff, want) {
					t.Errorf("redacted diff missing %q:\n%s", want, diff)
				}
			}
		})
	}
}

func TestComputeRedactedValuesDiffNonStringCredentialsAndNestedReferences(t *testing.T) {
	for _, allValues := range []bool{false, true} {
		name := "user-supplied"
		if allValues {
			name = "computed"
		}
		t.Run(name, func(t *testing.T) {
			left := map[string]any{
				"dbPassword": 12345678, "adminPassword": true,
				"credentials": map[string]any{
					"existingSecret": "old-secret", "secretName": "old-secret",
					"tokenSecretRef": "old-secret", "existingSecretPasswordKey": "old-key",
					"password": 87654321, "replicaCount": 1,
				},
			}
			right := map[string]any{
				"dbPassword": 23456789, "adminPassword": false,
				"credentials": map[string]any{
					"existingSecret": "new-secret", "secretName": "new-secret",
					"tokenSecretRef": "new-secret", "existingSecretPasswordKey": "new-key",
					"password": 98765432, "replicaCount": 2,
				},
				"newPassword": true,
			}
			values1, values2 := &HelmValues{UserSupplied: left}, &HelmValues{UserSupplied: right}
			if allValues {
				values1, values2 = &HelmValues{Computed: left}, &HelmValues{Computed: right}
			}
			diff, err := computeRedactedValuesDiff(values1, values2, 1, 2, allValues)
			if err != nil {
				t.Fatal(err)
			}
			for _, leak := range []string{"12345678", "23456789", "87654321", "98765432", "true", "false"} {
				if strings.Contains(diff, leak) {
					t.Fatalf("redacted diff leaks %q:\n%s", leak, diff)
				}
			}
			for _, want := range []string{
				"[REDACTED]", "-  replicaCount: 1", "+  replicaCount: 2",
				"-  existingSecret: old-secret", "+  existingSecret: new-secret",
				"-  secretName: old-secret", "+  secretName: new-secret",
				"-  tokenSecretRef: old-secret", "+  tokenSecretRef: new-secret",
				"-  existingSecretPasswordKey: old-key", "+  existingSecretPasswordKey: new-key",
			} {
				if !strings.Contains(diff, want) {
					t.Errorf("redacted diff missing %q:\n%s", want, diff)
				}
			}
		})
	}
}
