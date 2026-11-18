package k8s

import (
	"encoding/json"
	"maps"
	"os"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/skyhook-io/radar/pkg/k8score"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/intstr"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

type longhornPDBFixture struct {
	PDB policyv1.PodDisruptionBudget `json:"pdb"`
	IM  unstructured.Unstructured    `json:"im"`
	Pod corev1.Pod                   `json:"pod"`
}

func readLonghornFixture(t *testing.T, version string) *longhornPDBFixture {
	t.Helper()
	data, err := os.ReadFile("testdata/longhorn-pdb/v" + version + ".json")
	if err != nil {
		t.Fatal(err)
	}
	var f longhornPDBFixture
	if err := json.Unmarshal(data, &f); err != nil {
		t.Fatal(err)
	}
	return &f
}

func (f *longhornPDBFixture) context(otherPDBs ...*policyv1.PodDisruptionBudget) longhornPDBContext {
	return longhornPDBContext{
		managers: map[string]*unstructured.Unstructured{f.PDB.Namespace + "/" + f.PDB.Name: &f.IM},
		pods:     map[string][]*corev1.Pod{f.PDB.Namespace: {&f.Pod}},
		pdbs:     longhornPDBSelectors(append([]*policyv1.PodDisruptionBudget{&f.PDB}, otherPDBs...)),
	}
}

func TestLonghornPDBSourceFamilies(t *testing.T) {
	for _, version := range []string{"1.10.2", "1.11.3", "1.12.2", "1.13.0"} {
		for _, processState := range []string{"empty", "attached", "frontend", "failed-replica", "legacy-stopped"} {
			t.Run(version+"/"+processState, func(t *testing.T) {
				f := readLonghornFixture(t, version)
				switch processState {
				case "attached":
					_ = unstructured.SetNestedField(f.IM.Object, map[string]any{"engine": map[string]any{"status": map[string]any{"state": "running"}}}, "status", "instanceEngines")
				case "frontend":
					if version == "1.10.2" || version == "1.11.3" {
						t.Skip("schema predates engine frontends")
					}
					_ = unstructured.SetNestedField(f.IM.Object, map[string]any{"frontend": map[string]any{"status": map[string]any{"state": "running"}}}, "status", "instanceEngineFrontends")
				case "failed-replica":
					_ = unstructured.SetNestedField(f.IM.Object, map[string]any{"replica": map[string]any{"status": map[string]any{"state": "error"}}}, "status", "instanceReplicas")
				case "legacy-stopped":
					if version != "1.10.2" {
						t.Skip("schema no longer has deprecated instances")
					}
					_ = unstructured.SetNestedField(f.IM.Object, map[string]any{"engine": map[string]any{"status": map[string]any{"state": "stopped"}}}, "status", "instances")
				}
				before := f.IM.DeepCopy()
				if !isLonghornManagedPDB(&f.PDB, f.context()) {
					t.Fatal("native identity not recognized")
				}
				if k8score.PodDisruptionBudgetEvictionState(&f.PDB) != k8score.PDBEvictionBlocked {
					t.Fatal("raw budget must stay blocked")
				}
				decision := k8score.ClassifyPodForDrain(f.Pod, k8score.DrainOptions{}, []policyv1.PodDisruptionBudget{f.PDB})
				if decision.Outcome != k8score.DrainOutcomeMayBlock || !decision.PDBChecked || decision.PDB == "" {
					t.Fatalf("drain lost real zero budget: %+v", decision)
				}
				if !reflect.DeepEqual(before, &f.IM) {
					t.Fatal("classifier mutated informer object")
				}
			})
		}
	}
}

func TestLonghornPDBRejectsUnverifiedIdentity(t *testing.T) {
	for name, mutate := range map[string]func(*longhornPDBFixture, *longhornPDBContext){
		"labels only":         func(f *longhornPDBFixture, c *longhornPDBContext) { c.managers = nil },
		"no pods":             func(f *longhornPDBFixture, c *longhornPDBContext) { c.pods = nil },
		"foreign group":       func(f *longhornPDBFixture, c *longhornPDBContext) { f.IM.SetAPIVersion("other.io/v1beta2") },
		"unknown schema":      func(f *longhornPDBFixture, c *longhornPDBContext) { f.IM.SetAPIVersion("longhorn.io/v9") },
		"wrong uid":           func(f *longhornPDBFixture, c *longhornPDBContext) { f.IM.SetUID("new-uid") },
		"empty uid":           func(f *longhornPDBFixture, c *longhornPDBContext) { f.IM.SetUID("") },
		"wrong name":          func(f *longhornPDBFixture, c *longhornPDBContext) { f.IM.SetName("another") },
		"wrong namespace":     func(f *longhornPDBFixture, c *longhornPDBContext) { f.IM.SetNamespace("another") },
		"wrong pod namespace": func(f *longhornPDBFixture, c *longhornPDBContext) { f.Pod.Namespace = "another" },
		"pdb owner":           func(f *longhornPDBFixture, c *longhornPDBContext) { f.PDB.OwnerReferences = f.Pod.OwnerReferences },
		"pod owner missing":   func(f *longhornPDBFixture, c *longhornPDBContext) { f.Pod.OwnerReferences = nil },
		"pod not controlled": func(f *longhornPDBFixture, c *longhornPDBContext) {
			no := false
			f.Pod.OwnerReferences[0].Controller = &no
		},
		"pod wrong kind": func(f *longhornPDBFixture, c *longhornPDBContext) { f.Pod.OwnerReferences[0].Kind = "Deployment" },
		"pod wrong group": func(f *longhornPDBFixture, c *longhornPDBContext) {
			f.Pod.OwnerReferences[0].APIVersion = "other.io/v1beta2"
		},
		"pod wrong name": func(f *longhornPDBFixture, c *longhornPDBContext) { f.Pod.OwnerReferences[0].Name = "another" },
		"conflicting owner": func(f *longhornPDBFixture, c *longhornPDBContext) {
			f.Pod.OwnerReferences = append(f.Pod.OwnerReferences, metav1.OwnerReference{Kind: "Other"})
		},
		"pod wrong node": func(f *longhornPDBFixture, c *longhornPDBContext) { f.Pod.Spec.NodeName = "worker-b" },
		"missing node association": func(f *longhornPDBFixture, c *longhornPDBContext) {
			unstructured.RemoveNestedField(f.IM.Object, "spec", "nodeID")
		},
		"pod not ready": func(f *longhornPDBFixture, c *longhornPDBContext) {
			f.Pod.Status.Conditions[0].Status = corev1.ConditionFalse
		},
		"pod pending":  func(f *longhornPDBFixture, c *longhornPDBContext) { f.Pod.Status.Phase = corev1.PodPending },
		"pod deleting": func(f *longhornPDBFixture, c *longhornPDBContext) { ts := metav1.Now(); f.Pod.DeletionTimestamp = &ts },
		"im deleting":  func(f *longhornPDBFixture, c *longhornPDBContext) { ts := metav1.Now(); f.IM.SetDeletionTimestamp(&ts) },
		"im not running": func(f *longhornPDBFixture, c *longhornPDBContext) {
			_ = unstructured.SetNestedField(f.IM.Object, "error", "status", "currentState")
		},
		"im state unknown": func(f *longhornPDBFixture, c *longhornPDBContext) {
			unstructured.RemoveNestedField(f.IM.Object, "status", "currentState")
		},
		"malformed im": func(f *longhornPDBFixture, c *longhornPDBContext) {
			_ = unstructured.SetNestedField(f.IM.Object, int64(1), "spec", "image")
		},
		"unknown type": func(f *longhornPDBFixture, c *longhornPDBContext) {
			_ = unstructured.SetNestedField(f.IM.Object, "other", "spec", "type")
		},
		"string budget": func(f *longhornPDBFixture, c *longhornPDBContext) {
			v := intstr.FromString("100%")
			f.PDB.Spec.MinAvailable = &v
		},
		"max unavailable": func(f *longhornPDBFixture, c *longhornPDBContext) {
			v := intstr.FromInt32(0)
			f.PDB.Spec.MaxUnavailable = &v
		},
		"explicit eviction policy": func(f *longhornPDBFixture, c *longhornPDBContext) {
			v := policyv1.AlwaysAllow
			f.PDB.Spec.UnhealthyPodEvictionPolicy = &v
		},
		"stale budget":   func(f *longhornPDBFixture, c *longhornPDBContext) { f.PDB.Generation++ },
		"extra selector": func(f *longhornPDBFixture, c *longhornPDBContext) { f.PDB.Spec.Selector.MatchLabels["extra"] = "yes" },
		"selector expressions": func(f *longhornPDBFixture, c *longhornPDBContext) {
			f.PDB.Spec.Selector.MatchExpressions = []metav1.LabelSelectorRequirement{{Key: "extra", Operator: metav1.LabelSelectorOpDoesNotExist}}
		},
		"empty selector": func(f *longhornPDBFixture, c *longhornPDBContext) { f.PDB.Spec.Selector = &metav1.LabelSelector{} },
		"nil selector":   func(f *longhornPDBFixture, c *longhornPDBContext) { f.PDB.Spec.Selector = nil },
		"multiple pods": func(f *longhornPDBFixture, c *longhornPDBContext) {
			p := f.Pod.DeepCopy()
			p.Name = "another"
			c.pods[f.Pod.Namespace] = append(c.pods[f.Pod.Namespace], p)
		},
		"wrong checksum": func(f *longhornPDBFixture, c *longhornPDBContext) {
			f.PDB.Spec.Selector.MatchLabels["longhorn.io/instance-manager-image"] = "imi-spoof"
		},
		"unexpected count": func(f *longhornPDBFixture, c *longhornPDBContext) { f.PDB.Status.ExpectedPods = 2 },
	} {
		t.Run(name, func(t *testing.T) {
			f := readLonghornFixture(t, "1.13.0")
			c := f.context()
			mutate(f, &c)
			if isLonghornManagedPDB(&f.PDB, c) {
				t.Fatal("unverified graph recognized")
			}
		})
	}
	f := readLonghornFixture(t, "1.13.0")
	overlap := f.PDB.DeepCopy()
	overlap.Name = "custom"
	if isLonghornManagedPDB(&f.PDB, f.context(overlap)) {
		t.Fatal("overlap must retain generic warning")
	}
}

func TestLonghornPDBConditionalLabels(t *testing.T) {
	for _, imType := range []string{"aio", "engine", "replica"} {
		for _, engine := range []string{"", "all", "v1", "v2"} {
			for _, image := range []string{"", "mirror.example/storage/im@sha256:123", "unknown-registry/custom-future:v99"} {
				f := readLonghornFixture(t, "1.10.2")
				_ = unstructured.SetNestedField(f.IM.Object, image, "spec", "image")
				_ = unstructured.SetNestedField(f.IM.Object, imType, "spec", "type")
				_ = unstructured.SetNestedField(f.IM.Object, engine, "spec", "dataEngine")
				f.PDB.Spec.Selector.MatchLabels = longhornInstanceManagerLabels("worker-a", image, imType, engine)
				f.Pod.Labels = maps.Clone(f.PDB.Spec.Selector.MatchLabels)
				if !isLonghornManagedPDB(&f.PDB, f.context()) {
					t.Fatalf("identity failed for %s/%s/%s", imType, engine, image)
				}
			}
		}
	}
	labels := longhornInstanceManagerLabels("", "", "engine", "all")
	if len(labels) != 3 {
		t.Fatalf("conditional labels unexpectedly required: %+v", labels)
	}
	if got := longhornInstanceManagerLabels("worker-a", "docker.io/longhornio/longhorn-instance-manager:v1.13.0", "aio", "v1")["longhorn.io/instance-manager-image"]; got != "imi-2bdbe6d9" {
		t.Fatalf("source-derived checksum = %s", got)
	}
}

func TestLonghornPDBOtherBudgetSelectors(t *testing.T) {
	for _, test := range []struct {
		name    string
		mutate  func(*policyv1.PodDisruptionBudget)
		managed bool
	}{
		{"overlap", func(*policyv1.PodDisruptionBudget) {}, false},
		{"unrelated", func(p *policyv1.PodDisruptionBudget) {
			p.Spec.Selector.MatchLabels = map[string]string{"app": "unrelated"}
		}, true},
		{"other namespace", func(p *policyv1.PodDisruptionBudget) { p.Namespace = "other" }, true},
		{"deleting", func(p *policyv1.PodDisruptionBudget) { ts := metav1.Now(); p.DeletionTimestamp = &ts }, true},
		{"no selector", func(p *policyv1.PodDisruptionBudget) { p.Spec.Selector = nil }, true},
		{"invalid selector", func(p *policyv1.PodDisruptionBudget) {
			p.Spec.Selector.MatchExpressions = []metav1.LabelSelectorRequirement{{Key: "app", Operator: "invalid"}}
		}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := readLonghornFixture(t, "1.13.0")
			other := f.PDB.DeepCopy()
			other.Name = "other-budget"
			test.mutate(other)
			if got := isLonghornManagedPDB(&f.PDB, f.context(other)); got != test.managed {
				t.Fatalf("managed=%v, want %v", got, test.managed)
			}
		})
	}
}

func TestDetectProblemsLonghornDefaultCollection(t *testing.T) {
	for _, mode := range []string{"watched", "unwatched", "forbidden", "missing", "stopped", "im-error", "pod-not-ready", "cordoned", "autoscaler-true", "unknown-settings", "unhealthy-storage"} {
		t.Run(mode, func(t *testing.T) {
			defer ResetTestState()
			defer ResetTestDynamicState()
			f := readLonghornFixture(t, "1.13.0")
			if mode == "im-error" {
				_ = unstructured.SetNestedField(f.IM.Object, "error", "status", "currentState")
			}
			if mode == "pod-not-ready" {
				f.Pod.Status.Conditions[0].Status = corev1.ConditionFalse
			}
			node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "worker-a"}, Spec: corev1.NodeSpec{Unschedulable: mode == "cordoned"}}
			if err := InitTestResourceCache(fake.NewClientset(&f.PDB, &f.Pod, node)); err != nil {
				t.Fatal(err)
			}
			objects := []runtime.Object{&f.IM}
			if mode == "missing" {
				objects = nil
			}
			if mode == "unhealthy-storage" {
				_ = unstructured.SetNestedField(f.IM.Object, map[string]any{"replica": map[string]any{"status": map[string]any{"state": "error"}}}, "status", "instanceReplicas")
			}
			// Settings are deliberately unread: neither false, true nor unknown
			// can reduce this warning or assert controller removal eligibility.
			if mode == "autoscaler-true" {
				objects = append(objects, &unstructured.Unstructured{Object: map[string]any{"apiVersion": "longhorn.io/v1beta2", "kind": "Setting", "metadata": map[string]any{"name": "kubernetes-cluster-autoscaler-enabled", "namespace": f.PDB.Namespace}, "value": "true"}})
			}
			dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{longhornInstanceManagerGVR: "InstanceManagerList"}, objects...)
			if mode == "forbidden" {
				dyn.PrependReactor("list", "instancemanagers", func(k8stesting.Action) (bool, runtime.Object, error) {
					return true, nil, apierrors.NewForbidden(schema.GroupResource{Group: "longhorn.io", Resource: "instancemanagers"}, "", nil)
				})
			}
			if err := InitTestDynamicResourceCache(dyn, []APIResource{{Group: "longhorn.io", Version: "v1beta2", Kind: "InstanceManager", Name: "instancemanagers", Namespaced: true, Verbs: []string{"list", "watch"}}}); err != nil {
				t.Fatal(err)
			}
			dynamic := GetDynamicResourceCache()
			if mode != "unwatched" {
				WarmupCommonCRDs()
				if mode != "forbidden" && !dynamic.WaitForSync(longhornInstanceManagerGVR, 3*time.Second) {
					t.Fatal("default warmup did not synchronize IMs")
				}
			}
			if mode == "stopped" {
				dynamic.Stop()
			}
			before := len(dyn.Actions())
			problems := DetectProblems(GetResourceCache(), f.PDB.Namespace)
			problem, ok := lookupProblem(problems, "PodDisruptionBudget", f.PDB.Name, "Voluntary evictions blocked")
			if !ok || problem.Severity != "high" || problem.Fingerprint != "pdb:zero-disruptions" {
				t.Fatalf("warning lost or downgraded: %+v", problem)
			}
			wantManaged := mode != "unwatched" && mode != "forbidden" && mode != "missing" && mode != "stopped" && mode != "im-error" && mode != "pod-not-ready"
			if got := strings.HasPrefix(problem.Message, "Longhorn is protecting"); got != wantManaged {
				t.Fatalf("managed=%v want %v: %+v", got, wantManaged, problem)
			}
			if wantManaged && (problem.Action != longhornPDBAction || len(problem.RequiredReads) != 3 || problem.Degraded == nil) {
				t.Fatalf("missing explanation/access fallback: %+v", problem)
			}
			if !wantManaged && problem.Action != k8score.PDBBlockedEvictionAction {
				t.Fatalf("unverified manager must retain safe conditional advice: %+v", problem)
			}
			if len(dyn.Actions()) != before {
				t.Fatal("detector started API operations")
			}
		})
	}
}

func TestLonghornUnsyncedContextKeepsGenericWarning(t *testing.T) {
	defer ResetTestState()
	defer ResetTestDynamicState()
	f := readLonghornFixture(t, "1.13.0")
	if err := InitTestResourceCache(fake.NewClientset(&f.PDB, &f.Pod)); err != nil {
		t.Fatal(err)
	}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{longhornInstanceManagerGVR: "InstanceManagerList"}, &f.IM)
	started, release := make(chan struct{}), make(chan struct{})
	defer close(release)
	var once sync.Once
	var lists atomic.Int32
	dyn.PrependReactor("list", "instancemanagers", func(k8stesting.Action) (bool, runtime.Object, error) {
		if lists.Add(1) == 1 {
			return false, nil, nil // Let the bounded access probe complete.
		}
		once.Do(func() { close(started) })
		<-release
		return false, nil, nil
	})
	if err := InitTestDynamicResourceCache(dyn, []APIResource{{Group: "longhorn.io", Version: "v1beta2", Kind: "InstanceManager", Name: "instancemanagers", Namespaced: true, Verbs: []string{"list", "watch"}}}); err != nil {
		t.Fatal(err)
	}
	if err := GetDynamicResourceCache().EnsureWatching(longhornInstanceManagerGVR); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("informer list did not start")
	}
	if got := GetDynamicResourceCache().NamespaceObservation(longhornInstanceManagerGVR, f.PDB.Namespace).State; got != k8score.DynamicObservationSyncing {
		t.Fatalf("want genuinely unsynced informer, got %s", got)
	}
	problem, ok := lookupProblem(DetectProblems(GetResourceCache(), f.PDB.Namespace), "PodDisruptionBudget", f.PDB.Name, "Voluntary evictions blocked")
	if !ok || problem.Severity != "high" || strings.Contains(problem.Message, "Longhorn is protecting") {
		t.Fatalf("incomplete graph gained managed diagnosis or lost warning: %+v", problem)
	}
	if problem.Action != k8score.PDBBlockedEvictionAction {
		t.Fatalf("unsynced graph must not recommend bypassing a controller: %+v", problem)
	}
}
