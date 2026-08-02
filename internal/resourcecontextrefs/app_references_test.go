package resourcecontextrefs

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/k8s"
)

func TestAppReferencesFromEnvChecks(t *testing.T) {
	if got := AppReferencesFromEnvChecks(nil, nil, nil, nil); got != nil {
		t.Fatalf("empty checks should return nil, got %+v", got)
	}

	got := AppReferencesFromEnvChecks([]k8s.EnvServiceRefCheck{{
		Status:           "port_mismatch",
		Container:        "app",
		EnvName:          "PAYMENTS_URL",
		Value:            "http://payments:8080?password=supersecret",
		ServiceNamespace: "shop",
		ServiceName:      "payments",
		ReferencedPort:   8080,
		ServicePorts:     []string{"80/TCP"},
		Message:          "env var references Service port 8080 with password=supersecret, but Service exposes 80/TCP",
	}}, nil, nil, nil)
	if got == nil || len(got.ServiceEnv) != 1 {
		t.Fatalf("expected one service env reference, got %+v", got)
	}
	ref := got.ServiceEnv[0]
	if ref.Status != "port_mismatch" || ref.Container != "app" || ref.Env != "PAYMENTS_URL" {
		t.Fatalf("basic fields not mapped correctly: %+v", ref)
	}
	if ref.Value == "http://payments:8080?password=supersecret" || ref.Message == "env var references Service port 8080 with password=supersecret, but Service exposes 80/TCP" {
		t.Fatalf("secret-bearing value/message were not redacted: %+v", ref)
	}
	if ref.Service.Kind != "Service" || ref.Service.Namespace != "shop" || ref.Service.Name != "payments" {
		t.Fatalf("service ref not mapped correctly: %+v", ref.Service)
	}
	if ref.ReferencedPort != 8080 || len(ref.ServicePorts) != 1 || ref.ServicePorts[0] != "80/TCP" || ref.Message == "" {
		t.Fatalf("detail fields not mapped correctly: %+v", ref)
	}
}

func TestAppReferencesFromEnvChecks_HistoryFacts(t *testing.T) {
	removedAt := time.Date(2026, time.July, 22, 6, 47, 0, 0, time.UTC)
	startedAt := removedAt.Add(time.Hour)
	changedAt := startedAt.Add(time.Minute)
	got := AppReferencesFromEnvChecks(nil, nil, []k8s.RemovedServiceEnvCheck{{
		Container: "frontend", EnvName: "CART_ADDR", OldValue: "cart:8080?password=sentinel-secret-value",
		ServiceNamespace: "shop", ServiceName: "cart", ReferencedPort: 8080, RemovedAt: removedAt,
		Message: "removed cart:8080 with password=sentinel-secret-value",
	}}, []k8s.StaleSecretEnvCheck{{
		Namespace: "shop", PodName: "catalog-1", Container: "app", EnvName: "DB_PASSWORD", ReferenceKind: "secretKeyRef", EvidenceSource: "observed_change",
		SecretName: "db-conn", Key: "password", ContainerStartedAt: startedAt, SecretChangedAt: changedAt,
		Message: "Secret key changed; password=sentinel-secret-value",
	}})
	if got == nil || len(got.RemovedServiceEnv) != 1 || len(got.StaleSecretEnv) != 1 {
		t.Fatalf("history facts were not mapped: %+v", got)
	}
	removed := got.RemovedServiceEnv[0]
	if removed.Service.Kind != "Service" || removed.Service.Name != "cart" || !removed.RemovedAt.Equal(removedAt) || removed.ReferencedPort != 8080 {
		t.Fatalf("unexpected removed env mapping: %+v", removed)
	}
	stale := got.StaleSecretEnv[0]
	if stale.Pod != "catalog-1" || stale.AffectedPods != 1 || stale.Secret.Kind != "Secret" || stale.Secret.Name != "db-conn" || stale.Key != "password" || stale.ReferenceKind != "secretKeyRef" || stale.EvidenceSource != "observed_change" || !stale.ContainerStartedAt.Equal(startedAt) || !stale.SecretChangedAt.Equal(changedAt) {
		t.Fatalf("unexpected stale Secret mapping: %+v", stale)
	}
	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal AppReferences: %v", err)
	}
	if strings.Contains(string(encoded), "sentinel-secret-value") {
		t.Fatalf("sensitive value leaked through AppReferences: %s", encoded)
	}
}

func TestAppReferencesFromEnvChecks_GroupsReplicaFactsAndMarksTruncation(t *testing.T) {
	startedAt := time.Date(2026, time.July, 22, 6, 47, 0, 0, time.UTC)
	changedAt := startedAt.Add(time.Minute)
	var checks []k8s.StaleSecretEnvCheck
	for i := 0; i < maxStaleSecretEnvContextReferences+1; i++ {
		checks = append(checks, k8s.StaleSecretEnvCheck{
			Namespace: "shop", PodName: "catalog-b", Container: "app",
			ReferenceKind: "secretKeyRef", EvidenceSource: "managed_fields_mtime",
			SecretName: fmt.Sprintf("secret-%d", i), ContainerStartedAt: startedAt, SecretChangedAt: changedAt,
		})
	}
	checks = append(checks, k8s.StaleSecretEnvCheck{
		Namespace: "shop", PodName: "catalog-a", Container: "app",
		ReferenceKind: "secretKeyRef", EvidenceSource: "managed_fields_mtime",
		SecretName: "secret-0", ContainerStartedAt: startedAt.Add(time.Second), SecretChangedAt: changedAt,
	})
	got := AppReferencesFromEnvChecks(nil, nil, nil, checks)
	if got == nil || len(got.StaleSecretEnv) != maxStaleSecretEnvContextReferences || !got.StaleSecretEnvTruncated {
		t.Fatalf("unexpected grouped/truncated stale Secret refs: %+v", got)
	}
	first := got.StaleSecretEnv[0]
	if first.Secret.Name != "secret-0" || first.Pod != "catalog-a" || first.AffectedPods != 2 {
		t.Fatalf("replica facts were not grouped with deterministic sample pod: %+v", first)
	}
}

func TestAppReferencesFromEnvChecks_ManagedFieldsFallbackOmitsUnknownKey(t *testing.T) {
	startedAt := time.Date(2026, time.July, 22, 6, 47, 0, 0, time.UTC)
	changedAt := startedAt.Add(time.Minute)
	got := AppReferencesFromEnvChecks(nil, nil, nil, []k8s.StaleSecretEnvCheck{{
		Namespace: "shop", PodName: "catalog-1", Container: "app",
		ReferenceKind: "envFrom", EvidenceSource: "managed_fields_mtime", Prefix: "DB_",
		SecretName: "db-conn", ContainerStartedAt: startedAt, SecretChangedAt: changedAt,
		Message: "if the write touched a consumed key the env value may be stale",
	}})
	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal AppReferences: %v", err)
	}
	text := string(encoded)
	if !strings.Contains(text, `"referenceKind":"envFrom"`) || !strings.Contains(text, `"evidenceSource":"managed_fields_mtime"`) {
		t.Fatalf("managedFields provenance missing from AppReferences: %s", text)
	}
	if strings.Contains(text, `"key"`) || strings.Contains(text, `"env"`) {
		t.Fatalf("unknown fallback key/env must be omitted, not serialized empty: %s", text)
	}
}

func TestAppReferencesFromEnvChecks_DuplicateEnv(t *testing.T) {
	got := AppReferencesFromEnvChecks(nil, []k8s.DuplicateEnvVarCheck{{
		Container:         "app",
		EnvName:           "API_TOKEN",
		LastDeclaredValue: "password=second-secret",
		Occurrences: []k8s.DuplicateEnvVarOccurrence{
			{Position: 1, Value: "password=first-secret"},
			{Position: 3, Value: "password=second-secret"},
		},
		Message: "API_TOKEN appears twice: password=first-secret then password=second-secret",
	}}, nil, nil)
	if got == nil || len(got.DuplicateEnv) != 1 {
		t.Fatalf("expected one duplicate env reference, got %+v", got)
	}
	ref := got.DuplicateEnv[0]
	if ref.Container != "app" || ref.Env != "API_TOKEN" || ref.Count != 2 {
		t.Fatalf("basic fields not mapped correctly: %+v", ref)
	}
	if len(ref.Occurrences) != 2 || ref.Occurrences[0].Position != 1 || ref.Occurrences[1].Position != 3 {
		t.Fatalf("occurrences not mapped correctly: %+v", ref.Occurrences)
	}
	if ref.Occurrences[0].Value == "password=first-secret" || ref.Occurrences[1].Value == "password=second-secret" || ref.LastDeclaredValue == "password=second-secret" || ref.Message == "API_TOKEN appears twice: password=first-secret then password=second-secret" {
		t.Fatalf("duplicate env values/message were not redacted: %+v", ref)
	}
}

func TestAppReferencesFromEnvChecks_BoundsDuplicateOccurrences(t *testing.T) {
	checks := []k8s.DuplicateEnvVarCheck{{
		Container:         "app",
		EnvName:           "MODE",
		LastDeclaredValue: "seven",
		Message:           "7 declarations; ... and 2 more; last definition seven",
	}}
	for i, value := range []string{"one", "two", "three", "four", "five", "six", "seven"} {
		checks[0].Occurrences = append(checks[0].Occurrences, k8s.DuplicateEnvVarOccurrence{Position: i + 1, Value: value})
	}

	got := AppReferencesFromEnvChecks(nil, checks, nil, nil)
	if got == nil || len(got.DuplicateEnv) != 1 {
		t.Fatalf("expected one duplicate env reference, got %+v", got)
	}
	ref := got.DuplicateEnv[0]
	if ref.Count != 7 || len(ref.Occurrences) != 5 || ref.Occurrences[4].Position != 5 || ref.LastDeclaredValue != "seven" {
		t.Fatalf("unexpected bounded duplicate reference: %+v", ref)
	}
}
