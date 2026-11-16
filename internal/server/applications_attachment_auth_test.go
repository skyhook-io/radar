package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"context"
	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/relations"
	"github.com/skyhook-io/radar/pkg/topology"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
)

func TestApplicationsAttachmentAccessInBothCacheFillOrders(t *testing.T) {
	deployment := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "team", UID: "api"}, Spec: appsv1.DeploymentSpec{
		Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "api"}},
		Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "api"}}, Spec: corev1.PodSpec{Volumes: []corev1.Volume{{Name: "credentials", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: "private-secret"}}}}}},
	}}
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "private-secret", Namespace: "team"}}
	service := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "team"}, Spec: corev1.ServiceSpec{Selector: map[string]string{"app": "api"}}}
	useTestResourceCache(t, fake.NewClientset(append(testNamespaces("team"), deployment, secret, service)...))
	var objects []runtime.Object
	var resources []k8s.APIResource
	kinds := map[schema.GroupVersionResource]string{}
	for _, kind := range []string{"ScaledJob", "TriggerAuthentication", "ClusterTriggerAuthentication"} {
		resource := strings.ToLower(kind) + "s"
		ns, name := "team", "private-"+strings.ToLower(kind)
		if kind == "ClusterTriggerAuthentication" {
			ns = ""
		}
		u := &unstructured.Unstructured{}
		u.SetAPIVersion("keda.sh/v1alpha1")
		u.SetKind(kind)
		u.SetNamespace(ns)
		u.SetName(name)
		if kind == "ScaledJob" {
			u.SetName("jobs")
			u.Object["spec"] = map[string]any{"triggers": []any{
				map[string]any{"type": "queue", "authenticationRef": map[string]any{"name": "private-triggerauthentication"}},
				map[string]any{"type": "queue", "authenticationRef": map[string]any{"kind": "ClusterTriggerAuthentication", "name": "private-clustertriggerauthentication"}},
			}}
		}
		objects = append(objects, u)
		gvr := schema.GroupVersionResource{Group: "keda.sh", Version: "v1alpha1", Resource: resource}
		kinds[gvr] = kind + "List"
		resources = append(resources, k8s.APIResource{Group: gvr.Group, Version: gvr.Version, Name: resource, Kind: kind, Namespaced: ns != "", IsCRD: true, Verbs: []string{"get", "list", "watch"}})
	}
	if err := k8s.InitTestDynamicResourceCache(dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), kinds, objects...), resources); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k8s.ResetTestDynamicState)
	addApplicationTypedDiscovery()
	for gvr := range kinds {
		dc := k8s.GetDynamicResourceCache()
		if err := dc.EnsureWatching(gvr); err != nil || !dc.WaitForSync(gvr, 2*time.Second) {
			t.Fatalf("watch %v: %v", gvr, err)
		}
	}
	session, err := k8s.BeginOwnershipRead(k8s.GetResourceCache())
	if err != nil {
		t.Fatal(err)
	}
	batch := buildAppRelationBatch(context.Background(), session.(relations.Session), []string{"team"})
	wantDeployment := &appRelationships{Services: []string{"api"}, serviceRefs: refsByKey([]topology.ResourceRef{{Kind: "Service", Namespace: "team", Name: "api"}})}
	if got := batch.relationshipsForRef("apps", "Deployment", "team", "api"); !reflect.DeepEqual(got, wantDeployment) {
		t.Fatalf("deployment attachments=%#v want=%#v", got, wantDeployment)
	}
	wantJob := &appRelationships{Configs: 2, configRefs: refsByKey([]topology.ResourceRef{{Group: "keda.sh", Kind: "TriggerAuthentication", Namespace: "team", Name: "private-triggerauthentication"}, {Group: "keda.sh", Kind: "ClusterTriggerAuthentication", Name: "private-clustertriggerauthentication"}})}
	if got := batch.relationshipsForRef("keda.sh", "ScaledJob", "team", "jobs"); !reflect.DeepEqual(got, wantJob) {
		t.Fatalf("ScaledJob attachments=%#v want=%#v", got, wantJob)
	}
	t.Cleanup(clearApplicationsCache)
	s := &Server{permCache: auth.NewPermissionCache()}
	for _, name := range []string{"reader", "restricted"} {
		allowed := name == "reader"
		p := &auth.UserPermissions{AllowedNamespaces: []string{"team"}}
		p.SetCanI("list", "", "secrets", "team", allowed)
		p.SetCanI("get", "keda.sh", "triggerauthentications", "team", allowed)
		p.SetCanI("get", "keda.sh", "clustertriggerauthentications", "", allowed)
		p.SetCanI("list", "keda.sh", "clustertriggerauthentications", "", allowed)
		s.permCache.Set(name, nil, p)
	}
	want := map[string][]byte{}
	for _, order := range [][]string{{"reader", "restricted", "reader"}, {"restricted", "reader", "restricted"}} {
		clearApplicationsCache()
		for _, name := range order {
			r := httptest.NewRequest(http.MethodGet, "/api/applications?namespaces=team", nil)
			r = r.WithContext(auth.ContextWithUser(r.Context(), &auth.User{Username: name}))
			shared, err := listApplicationsForRequest(r.Context(), []string{"team"}, false)
			if err != nil {
				t.Fatal(err)
			}
			before, _ := json.Marshal(shared)
			got := s.filterApplicationsForUser(r, shared)
			data, _ := json.Marshal(got)
			after, _ := json.Marshal(shared)
			if string(before) != string(after) {
				t.Fatal("request mutated shared rows")
			}
			if prev, ok := want[name]; ok && string(prev) != string(data) {
				t.Fatalf("%s response depends on cache-fill order", name)
			}
			want[name] = data
			for _, token := range []string{"private-triggerauthentication", "private-clustertriggerauthentication"} {
				if strings.Contains(string(data), token) != (name == "reader") {
					t.Fatalf("%s: visibility of %s incorrect: %s", name, token, data)
				}
			}
			secretRefs := refsByKey([]topology.ResourceRef{{Kind: "Secret", Namespace: "team", Name: "private-secret"}})
			secretRows := &applicationsResponse{Applications: []appRow{{Key: "team/Deployment/secret-consumer", Relationships: &appRelationships{configRefs: secretRefs, Configs: 1, ConfigRefs: sortedRefs(secretRefs, 20)}}}}
			// A workload's own references are listed by name for anyone who may
			// see the workload; reading the referenced object stays gated.
			secretResponse := s.filterApplicationsForUser(r, secretRows)
			secretJSON, _ := json.Marshal(secretResponse)
			if secretResponse.Applications[0].Relationships.Configs != 1 || !strings.Contains(string(secretJSON), "private-secret") {
				t.Fatalf("%s: a referenced Secret disappeared: %s", name, secretJSON)
			}
			for _, row := range got.Applications {
				if row.Name == "api" && (row.WorkloadClass != "service" || row.Relationships.Configs != 0) {
					t.Fatalf("%s: API row = %+v", name, row)
				}
			}
		}
	}
}

func TestApplicationsIncompleteExposureIsNotAWorker(t *testing.T) {
	for _, kind := range []string{"Deployment", "StatefulSet", "DaemonSet", "Rollout"} {
		if got := classifyWorkload(kind, &appRelationships{exposureIncomplete: true}); got != "unknown" {
			t.Fatalf("%s classified %s", kind, got)
		}
		if got := classifyWorkload(kind, &appRelationships{exposureIncomplete: true, Services: []string{"api"}}); got != "service" {
			t.Fatalf("positive exposure classified %s", got)
		}
	}
	g := &appRelationBatch{exposureIncomplete: true}
	if got := classifyWorkload("Deployment", g.relationshipsForRef("apps", "Deployment", "team", "api")); got != "unknown" {
		t.Fatalf("incomplete batch classified %s", got)
	}
}
