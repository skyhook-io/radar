package resourcecontext

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestOpenTelemetryAutoInstrumentationJSONContract(t *testing.T) {
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: "team"}}
	rc := Build(context.Background(), pod, Options{
		Tier: TierBasic,
		OpenTelemetryAutoInstrumentation: &OpenTelemetryAutoInstrumentationContext{
			NamespaceDefaultsUnknown: true,
			RequestsIfPodCreatedNow: []OpenTelemetryInjectionRequest{{
				Language: "java", Source: ContextRef{Kind: "Pod", Namespace: "team", Name: "app"},
				Resolution:      OpenTelemetryInjectionUnknown,
				Instrumentation: &ContextRef{Kind: "Instrumentation", Group: "opentelemetry.io", Namespace: "hidden", Name: "agent"},
				AnnotationPath:  "/metadata/annotations/instrumentation.opentelemetry.io~1inject-java",
			}},
		},
	})
	raw, err := json.Marshal(rc)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	const want = `{"openTelemetry":{"namespaceDefaultsUnknown":true,"requestsIfPodCreatedNow":[{"language":"java","source":{"kind":"Pod","namespace":"team","name":"app"},"resolution":"unknown","instrumentation":{"kind":"Instrumentation","namespace":"hidden","name":"agent","group":"opentelemetry.io"},"annotationPath":"/metadata/annotations/instrumentation.opentelemetry.io~1inject-java"}]}}`
	var gotJSON, wantJSON any
	if err := json.Unmarshal(fields["autoInstrumentation"], &gotJSON); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(want), &wantJSON); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(gotJSON, wantJSON) {
		t.Fatalf("autoInstrumentation = %s; want %s", fields["autoInstrumentation"], want)
	}
}

func TestOpenTelemetryAutoInstrumentationOmissionScope(t *testing.T) {
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: "team"}}
	for _, unavailable := range []bool{false, true} {
		rc := Build(context.Background(), pod, Options{Tier: TierBasic, OpenTelemetryAutoInstrumentationUnavailable: unavailable})
		if rc.AutoInstrumentation != nil {
			t.Fatalf("unavailable=%v: an absent provider produced an empty envelope", unavailable)
		}
		found := false
		for _, omission := range rc.Omitted {
			if omission.Field == "autoInstrumentation" {
				t.Fatal("one provider's missing observation marked the whole domain unavailable")
			}
			if omission.Field == "autoInstrumentation.openTelemetry" {
				found = true
				if omission.Reason != OmittedUnavailable {
					t.Fatalf("reason = %q", omission.Reason)
				}
			}
		}
		if found != unavailable {
			t.Fatalf("unavailable=%v: native omission present=%v", unavailable, found)
		}
	}
}
