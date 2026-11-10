package context

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestRedactHelmValues(t *testing.T) {
	const sentinel = "SENTINEL"
	tests := []struct {
		name  string
		input map[string]any
		want  map[string]any
	}{
		{"dbPassword", map[string]any{"dbPassword": sentinel}, map[string]any{"dbPassword": "[REDACTED]"}},
		{"auth.postgresPassword", map[string]any{"auth": map[string]any{"postgresPassword": sentinel}}, map[string]any{"auth": map[string]any{"postgresPassword": "[REDACTED]"}}},
		{"redis.password", map[string]any{"redis.password": sentinel}, map[string]any{"redis.password": "[REDACTED]"}},
		{"adminToken", map[string]any{"adminToken": sentinel}, map[string]any{"adminToken": "[REDACTED]"}},
		{"apiKey", map[string]any{"apiKey": "sk-live-SENTINEL"}, map[string]any{"apiKey": "[REDACTED]"}},
		{"aws.secretAccessKey", map[string]any{"aws": map[string]any{"secretAccessKey": sentinel}}, map[string]any{"aws": map[string]any{"secretAccessKey": "[REDACTED]"}}},
		{"oauth.clientSecret", map[string]any{"oauth": map[string]any{"clientSecret": sentinel}}, map[string]any{"oauth": map[string]any{"clientSecret": "[REDACTED]"}}},
		{"credentials subtree", map[string]any{"credentials": []any{sentinel, map[string]any{"username": sentinel, "replicaCount": 2}, []any{sentinel}}}, map[string]any{"credentials": []any{"[REDACTED]", map[string]any{"username": "[REDACTED]", "replicaCount": 2}, []any{"[REDACTED]"}}}},
		{"credentials numeric list", map[string]any{"credentials": []any{12345678}}, map[string]any{"credentials": []any{"[REDACTED]"}}},
		{"password numeric list", map[string]any{"password": []any{1234}}, map[string]any{"password": []any{"[REDACTED]"}}},
		{"credentials nested lists", map[string]any{"credentials": []any{[]any{12345678, true}}}, map[string]any{"credentials": []any{[]any{"[REDACTED]", "[REDACTED]"}}}},
		{"inherited sensitive list", map[string]any{"credentials": map[string]any{"values": []any{sentinel, 2, true}}}, map[string]any{"credentials": map[string]any{"values": []any{"[REDACTED]", 2, true}}}},
		{"existingSecret", map[string]any{"existingSecret": sentinel}, map[string]any{"existingSecret": sentinel}},
		{"passwordSecretName", map[string]any{"passwordSecretName": sentinel}, map[string]any{"passwordSecretName": sentinel}},
		{"tokenSecretRef", map[string]any{"tokenSecretRef": sentinel}, map[string]any{"tokenSecretRef": sentinel}},
		{"existingSecretPasswordKey", map[string]any{"existingSecretPasswordKey": sentinel}, map[string]any{"existingSecretPasswordKey": sentinel}},
		{"secretKeyRef", map[string]any{"secretKeyRef": sentinel}, map[string]any{"secretKeyRef": sentinel}},
		{"passwordFile", map[string]any{"passwordFile": sentinel}, map[string]any{"passwordFile": sentinel}},
		{"tokenPath", map[string]any{"tokenPath": sentinel}, map[string]any{"tokenPath": sentinel}},
		{"username", map[string]any{"username": sentinel}, map[string]any{"username": sentinel}},
		{"replicaCount", map[string]any{"replicaCount": 2}, map[string]any{"replicaCount": 2}},
		{"image.tag", map[string]any{"image": map[string]any{"tag": "1.2.3"}}, map[string]any{"image": map[string]any{"tag": "1.2.3"}}},
		{"pattern under benign key", map[string]any{"description": "Bearer abcdefghijklmnopqrstuvwxyz123456"}, map[string]any{"description": "Bearer [REDACTED]"}},
		{"hash under benign key", map[string]any{"checksum": strings.Repeat("a", 64)}, map[string]any{"checksum": strings.Repeat("a", 64)}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			RedactHelmValues(tt.input)
			if !reflect.DeepEqual(tt.input, tt.want) {
				t.Fatalf("got %#v, want %#v", tt.input, tt.want)
			}
		})
	}
}

func TestRedactHelmValuesCredentialSuffixes(t *testing.T) {
	for _, suffix := range []string{
		"password", "passwd", "passphrase", "token", "apikey", "apitoken",
		"accesskey", "secretkey", "privatekey", "clientsecret", "credentials",
	} {
		t.Run(suffix, func(t *testing.T) {
			key := "AuTh._-" + strings.ToUpper(suffix)
			values := map[string]any{key: "SENTINEL"}
			RedactHelmValues(values)
			if values[key] != "[REDACTED]" {
				t.Fatalf("credential key %q was not redacted", key)
			}
		})
	}
	for key := range sensitiveValueKeys {
		t.Run("exact "+key, func(t *testing.T) {
			values := map[string]any{key: "SENTINEL"}
			RedactHelmValues(values)
			if values[key] != "[REDACTED]" {
				t.Fatalf("exact credential key %q was not redacted", key)
			}
		})
	}
}

func TestRedactInlineSecretsKeepsHelmOnlyCredentialKeys(t *testing.T) {
	for _, key := range []string{"dbPassword", "auth.postgresPassword", "credentials"} {
		t.Run(key, func(t *testing.T) {
			spec := map[string]any{key: "SENTINEL"}
			RedactInlineSecrets(spec)
			if spec[key] != "SENTINEL" {
				t.Fatalf("CRD exact-key redaction changed for %q: %#v", key, spec[key])
			}
		})
	}
}

func TestRedactHelmValuesNonStringCredentials(t *testing.T) {
	for _, value := range []any{12345678, int64(12345678), float64(12345678), true, false, json.Number("12345678")} {
		t.Run(fmt.Sprintf("%T/%v", value, value), func(t *testing.T) {
			values := map[string]any{
				"dbPassword":  value,
				"credentials": map[string]any{"password": value, "replicaCount": value},
			}
			RedactHelmValues(values)
			want := map[string]any{
				"dbPassword":  "[REDACTED]",
				"credentials": map[string]any{"password": "[REDACTED]", "replicaCount": value},
			}
			if !reflect.DeepEqual(values, want) {
				t.Fatalf("got %#v, want %#v", values, want)
			}
			spec := map[string]any{"password": value}
			RedactInlineSecrets(spec)
			if !reflect.DeepEqual(spec["password"], value) {
				t.Fatalf("CRD non-string redaction changed: %#v", spec)
			}
		})
	}
}

func TestRedactHelmValuesReferencesUnderCredentials(t *testing.T) {
	for _, key := range []string{
		"existingSecret", "secretName", "tokenSecretRef", "existingSecretPasswordKey",
		"prefixExistingSecret", "existingSecretPassword", "ExIsTiNg._-SeCrEt", "ToKeN._-SeCrEtReF",
		"passwordSecretName", "SeCrEt._-NaMe",
	} {
		t.Run(key, func(t *testing.T) {
			values := map[string]any{"credentials": map[string]any{key: "db-secret", "password": "SENTINEL"}}
			RedactHelmValues(values)
			want := map[string]any{"credentials": map[string]any{key: "db-secret", "password": "[REDACTED]"}}
			if !reflect.DeepEqual(values, want) {
				t.Fatalf("got %#v, want %#v", values, want)
			}
		})
	}
	values := map[string]any{"credentials": map[string]any{
		"tokenSecretRef": []any{map[string]any{
			"key": "db-password", "password": "SENTINEL", "dbPassword": true,
			"description": "Bearer abcdefghijklmnopqrstuvwxyz123456",
		}},
		"secretName": "Bearer abcdefghijklmnopqrstuvwxyz123456",
	}}
	RedactHelmValues(values)
	want := map[string]any{"credentials": map[string]any{
		"tokenSecretRef": []any{map[string]any{
			"key": "db-password", "password": "[REDACTED]", "dbPassword": "[REDACTED]",
			"description": "Bearer [REDACTED]",
		}},
		"secretName": "Bearer [REDACTED]",
	}}
	if !reflect.DeepEqual(values, want) {
		t.Fatalf("got %#v, want %#v", values, want)
	}
}

func TestRedactHelmValuesNameFieldsUnderCredentials(t *testing.T) {
	for _, key := range []string{"name", "username", "userName", "dbName", "passwordName", "user._-name"} {
		t.Run(key, func(t *testing.T) {
			values := map[string]any{"credentials": map[string]any{key: "SENTINEL"}}
			RedactHelmValues(values)
			want := map[string]any{"credentials": map[string]any{key: "[REDACTED]"}}
			if !reflect.DeepEqual(values, want) {
				t.Fatalf("got %#v, want %#v", values, want)
			}
		})
	}
}

func TestRedactHelmValuesExactSensitiveReferenceKey(t *testing.T) {
	const key = "testsecretref"
	sensitiveValueKeys[key] = true
	t.Cleanup(func() { delete(sensitiveValueKeys, key) })
	values := map[string]any{"credentials": map[string]any{key: "SENTINEL"}}
	RedactHelmValues(values)
	want := map[string]any{"credentials": map[string]any{key: "[REDACTED]"}}
	if !reflect.DeepEqual(values, want) {
		t.Fatalf("got %#v, want %#v", values, want)
	}
}
