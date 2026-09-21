package resourcecontext

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

type reflectionFixture struct {
	facts *ReflectionFacts
	err   error
}

func (f reflectionFixture) For(string, string, string) (*ReflectionFacts, error) {
	return f.facts, f.err
}

func TestReflectionContextEvidenceAndAccess(t *testing.T) {
	obj := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "mirror", Namespace: "app", Annotations: map[string]string{reflectorPrefix + "reflects": "shared/source", reflectorPrefix + "reflected-version": "copied", reflectorPrefix + "reflected-at": "recorded-time", reflectorPrefix + "auto-reflects": "True"}}, Data: map[string][]byte{"password": []byte("not-for-agents")}}
	facts := &ReflectionFacts{Source: &ContextRef{Kind: "Secret", Namespace: "shared", Name: "source"}, SourceResourceVersion: "opaque-source-version"}
	rc := Build(context.Background(), obj, Options{Reflections: reflectionFixture{facts: facts}})
	if rc.Reflection == nil || rc.Reflection.Source == nil || rc.Reflection.Source.Namespace != "shared" || rc.Reflection.SourceResourceVersion != "opaque-source-version" || rc.Reflection.RecordedSourceVersion != "copied" || rc.Reflection.Automatic == nil || !*rc.Reflection.Automatic {
		t.Fatalf("missing evidence: %+v", rc.Reflection)
	}
	encoded, _ := json.Marshal(rc)
	if strings.Contains(string(encoded), "not-for-agents") || strings.Contains(string(encoded), "password") {
		t.Fatal("secret payload leaked")
	}
	rc = Build(context.Background(), obj, Options{Reflections: reflectionFixture{facts: facts}, AccessChecker: denyChecker{kind: "Secret", namespace: "shared"}})
	if rc.Reflection == nil || rc.Reflection.Source != nil || rc.Reflection.SourceResourceVersion != "" || rc.Reflection.DeclaredSource != "shared/source" {
		t.Fatalf("hidden observation leaked: %+v", rc.Reflection)
	}
	if len(rc.Omitted) != 1 || rc.Omitted[0].Field != "reflection.source" || rc.Omitted[0].Reason != OmittedRBACDenied {
		t.Fatalf("missing omission: %+v", rc.Omitted)
	}
}

func TestReflectionMirrorsCapAfterAuthorization(t *testing.T) {
	obj := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "source", Namespace: "shared"}}
	facts := &ReflectionFacts{}
	for i := 0; i < 25; i++ {
		facts.Mirrors = append(facts.Mirrors, ContextRef{Kind: "ConfigMap", Namespace: "hidden", Name: fmt.Sprint(i)})
	}
	for i := 0; i < 21; i++ {
		facts.Mirrors = append(facts.Mirrors, ContextRef{Kind: "ConfigMap", Namespace: "app", Name: fmt.Sprint(i)})
	}
	rc := Build(context.Background(), obj, Options{Reflections: reflectionFixture{facts: facts}, AccessChecker: denyChecker{kind: "ConfigMap", namespace: "hidden"}})
	if rc.Reflection == nil || len(rc.Reflection.VisibleMirrors) != 20 || !rc.Reflection.Truncated {
		t.Fatalf("bad authorized cap: %+v", rc.Reflection)
	}
	for _, r := range rc.Reflection.VisibleMirrors {
		if r.Namespace != "app" {
			t.Fatal("hidden mirror leaked")
		}
	}
	facts.Mirrors = facts.Mirrors[:25]
	rc = Build(context.Background(), obj, Options{Reflections: reflectionFixture{facts: facts}, AccessChecker: denyChecker{kind: "ConfigMap", namespace: "hidden"}})
	if rc.Reflection != nil || len(rc.Omitted) > 0 {
		t.Fatalf("hidden-only relationship caused noise: %+v", rc)
	}
}

func TestReflectionContextAbsenceAndColdCache(t *testing.T) {
	plain := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "plain", Namespace: "app"}}
	if rc := Build(context.Background(), plain, Options{}); rc.Reflection != nil {
		t.Fatal("ordinary resource noise")
	}
	plain.Annotations = map[string]string{reflectorPrefix + "reflects": "missing/source"}
	rc := Build(context.Background(), plain, Options{Reflections: reflectionFixture{facts: &ReflectionFacts{}}})
	if rc.Reflection == nil || rc.Reflection.DeclaredSource != "missing/source" || rc.Reflection.Source != nil || len(rc.Omitted) > 0 {
		t.Fatalf("missing source must stay unobserved: %+v", rc)
	}
	rc = Build(context.Background(), plain, Options{Reflections: reflectionFixture{err: errors.New("cache not ready")}})
	if rc.Reflection == nil || len(rc.Omitted) != 1 || rc.Omitted[0].Reason != OmittedCacheCold {
		t.Fatalf("cold cache lost: %+v", rc)
	}
	foreign := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "example.io/v1", "kind": "Secret", "metadata": map[string]any{"name": "x", "namespace": "app", "annotations": map[string]any{reflectorPrefix + "reflects": "shared/source"}}}}
	if rc := Build(context.Background(), foreign, Options{}); rc.Reflection != nil {
		t.Fatal("foreign kind got reflection")
	}
}

func TestDeniedReflectionSourceDoesNotDiscloseExistence(t *testing.T) {
	obj := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "mirror", Namespace: "app", Annotations: map[string]string{reflectorPrefix + "reflects": "hidden/source"}}}
	options := Options{AccessChecker: denyChecker{kind: "Secret", namespace: "hidden"}, Reflections: reflectionFixture{facts: &ReflectionFacts{Source: &ContextRef{Kind: "Secret", Namespace: "hidden", Name: "source"}, SourceResourceVersion: "hidden-version"}}}
	present := Build(context.Background(), obj, options)
	options.Reflections = reflectionFixture{facts: &ReflectionFacts{}}
	absent := Build(context.Background(), obj, options)
	a, _ := json.Marshal(present)
	b, _ := json.Marshal(absent)
	if string(a) != string(b) {
		t.Fatalf("source existence changed denied response: %s vs %s", a, b)
	}
	if len(absent.Omitted) != 1 || absent.Omitted[0].Reason != OmittedRBACDenied {
		t.Fatalf("missing permission reason: %+v", absent.Omitted)
	}
}
