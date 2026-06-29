package resourcecontextrefs

import (
	"testing"

	"github.com/skyhook-io/radar/internal/k8s"
)

func TestAppReferencesFromEnvServiceChecks(t *testing.T) {
	if got := AppReferencesFromEnvServiceChecks(nil); got != nil {
		t.Fatalf("empty checks should return nil, got %+v", got)
	}

	got := AppReferencesFromEnvServiceChecks([]k8s.EnvServiceRefCheck{{
		Status:           "port_mismatch",
		Container:        "app",
		EnvName:          "PAYMENTS_URL",
		Value:            "http://payments:8080?password=supersecret",
		ServiceNamespace: "shop",
		ServiceName:      "payments",
		ReferencedPort:   8080,
		ServicePorts:     []string{"80/TCP"},
		Message:          "env var references Service port 8080 with password=supersecret, but Service exposes 80/TCP",
	}})
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
