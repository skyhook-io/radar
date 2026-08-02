package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/envresolve"
)

func TestPodEnvironmentDescriptionNeverContainsSecretValues(t *testing.T) {
	pod := podEnvironmentTestPod()
	client := fake.NewSimpleClientset(
		&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: "shop"}, Data: map[string]string{"HOST": "db.internal"}},
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "db", Namespace: "shop"}, Data: map[string][]byte{"PASSWORD": []byte("sentinel-secret-value")}},
	)
	request := httptest.NewRequest("GET", "/", nil)

	sources, partial, truncated := loadPodEnvironmentSources(request, client, pod, false)
	if partial || truncated {
		t.Fatalf("unexpected partial=%v truncated=%v", partial, truncated)
	}
	response := envresolve.ResolvePod(pod, sources, envresolve.NodeData{})
	encoded, err := json.Marshal(response)
	if err != nil {
		t.Fatalf("marshal response: %v", err)
	}
	if strings.Contains(string(encoded), "sentinel-secret-value") {
		t.Fatalf("Secret value leaked into environment description: %s", encoded)
	}
	rows := serverRowsByName(response.Containers[0].Rows)
	if rows["HOST"].Value != "db.internal" {
		t.Fatalf("ConfigMap value was not resolved: %+v", rows["HOST"])
	}
	if !rows["PASSWORD"].Sensitive || rows["PASSWORD"].State != envresolve.ValueMasked || rows["PASSWORD"].Value != "" {
		t.Fatalf("Secret value was not masked: %+v", rows["PASSWORD"])
	}
}

func TestPodEnvironmentRevealResolvesOnlyAfterValueLoad(t *testing.T) {
	pod := podEnvironmentTestPod()
	client := fake.NewSimpleClientset(
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "db", Namespace: "shop"}, Data: map[string][]byte{"PASSWORD": []byte("sentinel-secret-value")}},
	)
	request := httptest.NewRequest("POST", "/", nil)

	descriptionSources, _, _ := loadPodEnvironmentSources(request, client, pod, false)
	_, value, available, found := envresolve.ResolveVariable(pod, "app", "PASSWORD", descriptionSources, envresolve.NodeData{})
	if !found || available || value != "" {
		t.Fatalf("description unexpectedly exposed reveal value: found=%v available=%v value=%q", found, available, value)
	}

	revealSources, _, _ := loadPodEnvironmentSources(request, client, pod, true)
	row, value, available, found := envresolve.ResolveVariable(pod, "app", "PASSWORD", revealSources, envresolve.NodeData{})
	if !found || !available || !row.Sensitive || value != "sentinel-secret-value" {
		t.Fatalf("reveal resolution failed: row=%+v found=%v available=%v value=%q", row, found, available, value)
	}
}

func TestPodEnvironmentSourceIDsAreDeduplicatedAndStable(t *testing.T) {
	pod := podEnvironmentTestPod()
	pod.Spec.InitContainers = []corev1.Container{{
		Name: "setup",
		Env: []corev1.EnvVar{{Name: "TOKEN", ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
			LocalObjectReference: corev1.LocalObjectReference{Name: "db"}, Key: "TOKEN",
		}}}},
	}}

	got := podEnvironmentSourceIDs(pod)
	want := []envresolve.SourceID{{Kind: "ConfigMap", Name: "app"}, {Kind: "Secret", Name: "db"}}
	if len(got) != len(want) {
		t.Fatalf("source IDs = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("source IDs = %+v, want %+v", got, want)
		}
	}
}

func TestAttachPodEnvironmentEvidenceHonorsEffectiveSourceAndAccess(t *testing.T) {
	changedAt := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	response := envresolve.PodEnvironment{Containers: []envresolve.Container{{Name: "app", Rows: []envresolve.Row{
		{Name: "OVERRIDDEN", State: envresolve.ValueResolved, Source: envresolve.SourceRef{Kind: "Direct"}},
		{Name: "ACTIVE", State: envresolve.ValueResolved, Source: envresolve.SourceRef{Kind: "ConfigMap", Name: "app", Key: "ACTIVE"}},
		{Name: "DEPENDENT", State: envresolve.ValueResolved, Source: envresolve.SourceRef{Kind: "Direct"}, Dependencies: []envresolve.SourceRef{{Kind: "Variable", Variable: "ACTIVE"}, {Kind: "ConfigMap", Name: "app", Key: "ACTIVE"}}},
	}}}}
	changes := []k8s.PodEnvChange{
		{Container: "app", Variable: "OVERRIDDEN", Source: envresolve.SourceRef{Kind: "ConfigMap", Name: "app", Key: "OVERRIDDEN"}, Evidence: envresolve.ChangeEvidence{Kind: "modified", ChangedAt: changedAt}},
		{Container: "app", Variable: "ACTIVE", Source: envresolve.SourceRef{Kind: "ConfigMap", Name: "app", Key: "ACTIVE"}, Evidence: envresolve.ChangeEvidence{Kind: "modified", ChangedAt: changedAt}},
		{Container: "app", Variable: "REMOVED", Source: envresolve.SourceRef{Kind: "Secret", Name: "db", Key: "REMOVED"}, Evidence: envresolve.ChangeEvidence{Kind: "removed", ChangedAt: changedAt}},
		{Container: "app", Variable: "DENIED", Source: envresolve.SourceRef{Kind: "Secret", Name: "private", Key: "DENIED"}, Evidence: envresolve.ChangeEvidence{Kind: "removed", ChangedAt: changedAt}},
	}
	sources := map[envresolve.SourceID]envresolve.SourceData{
		{Kind: "ConfigMap", Name: "app"}:  {Kind: "ConfigMap", Name: "app", State: envresolve.SourceAvailable},
		{Kind: "Secret", Name: "db"}:      {Kind: "Secret", Name: "db", State: envresolve.SourceAvailable, Sensitive: true},
		{Kind: "Secret", Name: "private"}: {Kind: "Secret", Name: "private", State: envresolve.SourceDenied, Sensitive: true},
	}

	attachPodEnvironmentEvidence(&response, changes, sources)
	rows := serverRowsByName(response.Containers[0].Rows)
	if rows["OVERRIDDEN"].Evidence != nil {
		t.Fatalf("shadowed source evidence attached to effective direct value: %+v", rows["OVERRIDDEN"])
	}
	if rows["ACTIVE"].Evidence == nil || rows["ACTIVE"].Evidence.Kind != "modified" {
		t.Fatalf("effective source evidence missing: %+v", rows["ACTIVE"])
	}
	if rows["DEPENDENT"].Evidence == nil || rows["DEPENDENT"].Evidence.Kind != "modified" {
		t.Fatalf("dependent value did not inherit source evidence: %+v", rows["DEPENDENT"])
	}
	if rows["REMOVED"].Evidence == nil || !rows["REMOVED"].Sensitive {
		t.Fatalf("removed envFrom row was not synthesized safely: %+v", rows["REMOVED"])
	}
	if _, ok := rows["DENIED"]; ok {
		t.Fatalf("denied source history leaked into response: %+v", rows["DENIED"])
	}
}

func TestTruncatePodEnvironmentRowsKeepsEveryContainerVisible(t *testing.T) {
	response := envresolve.PodEnvironment{Containers: []envresolve.Container{
		{Name: "setup", Rows: []envresolve.Row{{Name: "A"}, {Name: "B"}, {Name: "C"}}},
		{Name: "app", Rows: []envresolve.Row{{Name: "D"}, {Name: "E"}, {Name: "F"}}},
	}}
	if !truncatePodEnvironmentRows(&response, 2) {
		t.Fatal("expected truncation")
	}
	for _, container := range response.Containers {
		if len(container.Rows) != 1 || !container.Truncated {
			t.Fatalf("container disappeared under cap: %+v", container)
		}
	}
}

func TestLoadPodEnvironmentSourcesMarksUninspectedSourcesUnavailable(t *testing.T) {
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "many", Namespace: "shop"}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app"}}}}
	for i := 0; i < maxPodEnvironmentSources+1; i++ {
		pod.Spec.Containers[0].Env = append(pod.Spec.Containers[0].Env, corev1.EnvVar{
			Name: fmt.Sprintf("VAR_%03d", i),
			ValueFrom: &corev1.EnvVarSource{ConfigMapKeyRef: &corev1.ConfigMapKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{Name: fmt.Sprintf("cm-%03d", i)}, Key: "value",
			}},
		})
	}
	request := httptest.NewRequest("GET", "/", nil)
	sources, _, truncated := loadPodEnvironmentSources(request, fake.NewSimpleClientset(), pod, false)
	last := sources[envresolve.SourceID{Kind: "ConfigMap", Name: fmt.Sprintf("cm-%03d", maxPodEnvironmentSources)}]
	if !truncated || last.State != envresolve.SourceUnavailable || !strings.Contains(last.Message, "not inspected") {
		t.Fatalf("truncated source was misclassified: truncated=%v source=%+v", truncated, last)
	}
}

func TestLoadPodEnvironmentSourcesTreatsMissingSourcesAsComplete(t *testing.T) {
	pod := podEnvironmentTestPod()
	sources, partial, truncated := loadPodEnvironmentSources(
		httptest.NewRequest("GET", "/", nil),
		fake.NewSimpleClientset(),
		pod,
		false,
	)
	if partial || truncated {
		t.Fatalf("known-missing sources should be complete: partial=%v truncated=%v", partial, truncated)
	}
	for _, id := range []envresolve.SourceID{{Kind: "ConfigMap", Name: "app"}, {Kind: "Secret", Name: "db"}} {
		if sources[id].State != envresolve.SourceMissing {
			t.Fatalf("source %v = %+v, want missing", id, sources[id])
		}
	}
}

func TestPartialLaterEnvFromCanLeaveAnUnsafeEarlierReveal(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "shop"},
		Spec: corev1.PodSpec{Containers: []corev1.Container{{
			Name: "app",
			EnvFrom: []corev1.EnvFromSource{
				{SecretRef: &corev1.SecretEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: "earlier"}}},
				{ConfigMapRef: &corev1.ConfigMapEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: "later-unreadable"}}},
			},
		}}},
	}
	client := fake.NewSimpleClientset(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "earlier", Namespace: "shop"},
		Data:       map[string][]byte{"TOKEN": []byte("secret-value")},
	})
	client.PrependReactor("get", "configmaps", func(action k8stesting.Action) (bool, runtime.Object, error) {
		get := action.(k8stesting.GetAction)
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "configmaps"}, get.GetName(), fmt.Errorf("denied"))
	})
	sources, partial, _ := loadPodEnvironmentSources(httptest.NewRequest("POST", "/", nil), client, pod, true)
	_, _, available, found := envresolve.ResolveVariable(pod, "app", "TOKEN", sources, envresolve.NodeData{})
	if !partial || !found || !available {
		t.Fatalf("expected an apparently resolvable earlier Secret under incomplete later precedence: partial=%v found=%v available=%v", partial, found, available)
	}
}

func TestPodEnvironmentReadOutcomeMatchesHTTPErrorClass(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{name: "forbidden", err: apierrors.NewForbidden(schema.GroupResource{Resource: "pods"}, "api", fmt.Errorf("denied")), want: "denied"},
		{name: "unauthorized", err: apierrors.NewUnauthorized("unauthorized"), want: "denied"},
		{name: "not found", err: apierrors.NewNotFound(schema.GroupResource{Resource: "pods"}, "api"), want: "not-found"},
		{name: "infrastructure", err: fmt.Errorf("transport failed"), want: "unavailable"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := podEnvironmentReadOutcome(test.err); got != test.want {
				t.Fatalf("outcome = %q, want %q", got, test.want)
			}
		})
	}
}

func TestWritePodEnvironmentGetErrorSanitizesLog(t *testing.T) {
	var output bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&output)
	defer log.SetOutput(previous)

	recorder := httptest.NewRecorder()
	server := &Server{}
	server.writePodEnvironmentGetError(
		recorder,
		"shop\nforged-namespace",
		"api\rforged-name",
		"Pod",
		fmt.Errorf("transport failed\nforged-error"),
	)

	logged := output.String()
	if strings.Count(logged, "\n") != 1 || strings.Contains(logged, "\nforged-") || strings.Contains(logged, "\rforged-") {
		t.Fatalf("log contains an injected line: %q", logged)
	}
}

func TestPodEnvironmentRevealFailureKeepsStatesDistinct(t *testing.T) {
	tests := []struct {
		state       envresolve.ValueState
		wantStatus  int
		wantOutcome string
	}{
		{state: envresolve.ValueDenied, wantStatus: 403, wantOutcome: "denied"},
		{state: envresolve.ValueMissing, wantStatus: 404, wantOutcome: "not-found"},
		{state: envresolve.ValueUnavailable, wantStatus: 503, wantOutcome: "unavailable"},
	}
	for _, test := range tests {
		status, outcome, message := podEnvironmentRevealFailure(envresolve.Row{State: test.state})
		if status != test.wantStatus || outcome != test.wantOutcome || message == "" {
			t.Fatalf("state %q => status=%d outcome=%q message=%q", test.state, status, outcome, message)
		}
	}
}

func podEnvironmentTestPod() *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "shop"},
		Spec: corev1.PodSpec{Containers: []corev1.Container{{
			Name: "app",
			EnvFrom: []corev1.EnvFromSource{
				{ConfigMapRef: &corev1.ConfigMapEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: "app"}}},
				{SecretRef: &corev1.SecretEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: "db"}}},
			},
		}}},
	}
}

func serverRowsByName(rows []envresolve.Row) map[string]envresolve.Row {
	out := make(map[string]envresolve.Row, len(rows))
	for _, row := range rows {
		out[row.Name] = row
	}
	return out
}
