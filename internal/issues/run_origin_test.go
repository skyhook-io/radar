package issues

import (
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/issuesapi"
)

func controllerRef(apiVersion, kind, name string) metav1.OwnerReference {
	yes := true
	return metav1.OwnerReference{APIVersion: apiVersion, Kind: kind, Name: name, Controller: &yes}
}

func TestJobRunOrigin(t *testing.T) {
	cases := []struct {
		name string
		job  *batchv1.Job
		want *issuesapi.RunOrigin
	}{
		{
			name: "scheduled by a CronJob",
			job: &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Namespace: "ops", Name: "backup-29340480",
				OwnerReferences: []metav1.OwnerReference{controllerRef("batch/v1", "CronJob", "backup")}}},
			want: &issuesapi.RunOrigin{Creator: Ref{Group: "batch", Kind: "CronJob", Namespace: "ops", Name: "backup"}},
		},
		{
			name: "standalone with a generated name",
			job:  &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Namespace: "ops", Name: "migrate-x7k2p", GenerateName: "migrate-"}},
			want: &issuesapi.RunOrigin{GenerateName: "migrate-"},
		},
		{
			name: "hand-named, even when the name looks generated",
			job:  &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Namespace: "ops", Name: "backup-pgsql"}},
		},
		{
			name: "an owner that is not the controller is not the creator",
			job: &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Namespace: "ops", Name: "report-20261028",
				OwnerReferences: []metav1.OwnerReference{{APIVersion: "v1", Kind: "ConfigMap", Name: "report"}}}},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assertRunOrigin(t, jobRunOrigin(c.job), c.want)
		})
	}
}

func workflow(labels map[string]string, generateName string, owners []metav1.OwnerReference, templateRef map[string]any) *unstructured.Unstructured {
	u := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "argoproj.io/v1alpha1", "kind": "Workflow"}}
	u.SetNamespace("etl")
	u.SetName("nightly-x7k2p")
	u.SetLabels(labels)
	u.SetGenerateName(generateName)
	u.SetOwnerReferences(owners)
	if templateRef != nil {
		_ = unstructured.SetNestedMap(u.Object, templateRef, "spec", "workflowTemplateRef")
	}
	return u
}

func TestWorkflowRunOrigin(t *testing.T) {
	cron := Ref{Group: "argoproj.io", Kind: "CronWorkflow", Namespace: "etl", Name: "nightly"}
	tmpl := Ref{Group: "argoproj.io", Kind: "WorkflowTemplate", Namespace: "etl", Name: "nightly-tmpl"}
	clusterTmpl := Ref{Group: "argoproj.io", Kind: "ClusterWorkflowTemplate", Name: "shared-etl"}
	cases := []struct {
		name string
		wf   *unstructured.Unstructured
		want *issuesapi.RunOrigin
	}{
		{
			name: "CronWorkflow owner reference",
			wf:   workflow(nil, "", []metav1.OwnerReference{controllerRef("argoproj.io/v1alpha1", "CronWorkflow", "nightly")}, nil),
			want: &issuesapi.RunOrigin{Creator: cron},
		},
		{
			name: "CronWorkflow label without an owner reference",
			wf:   workflow(map[string]string{argoCronWorkflowLabel: "nightly"}, "", nil, nil),
			want: &issuesapi.RunOrigin{Creator: cron},
		},
		{
			name: "CronWorkflow wins over the template it uses",
			wf: workflow(map[string]string{argoCronWorkflowLabel: "nightly", argoWorkflowTemplateLabel: "nightly-tmpl"}, "",
				nil, map[string]any{"name": "nightly-tmpl"}),
			want: &issuesapi.RunOrigin{Creator: cron},
		},
		{
			name: "submitted from a WorkflowTemplate",
			wf:   workflow(nil, "nightly-", nil, map[string]any{"name": "nightly-tmpl"}),
			want: &issuesapi.RunOrigin{Creator: tmpl, GenerateName: "nightly-"},
		},
		{
			name: "submitted from a ClusterWorkflowTemplate",
			wf:   workflow(nil, "", nil, map[string]any{"name": "shared-etl", "clusterScope": true}),
			want: &issuesapi.RunOrigin{Creator: clusterTmpl},
		},
		{
			name: "template labels only",
			wf:   workflow(map[string]string{argoWorkflowTemplateLabel: "nightly-tmpl"}, "", nil, nil),
			want: &issuesapi.RunOrigin{Creator: tmpl},
		},
		{
			name: "cluster template label only",
			wf:   workflow(map[string]string{argoClusterTemplateLabel: "shared-etl"}, "", nil, nil),
			want: &issuesapi.RunOrigin{Creator: clusterTmpl},
		},
		{
			name: "argo submit from a file",
			wf:   workflow(nil, "nightly-", nil, nil),
			want: &issuesapi.RunOrigin{GenerateName: "nightly-"},
		},
		{
			name: "nothing recorded",
			wf:   workflow(nil, "", nil, nil),
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assertRunOrigin(t, workflowRunOrigin(c.wf), c.want)
		})
	}
}

func assertRunOrigin(t *testing.T, got, want *issuesapi.RunOrigin) {
	t.Helper()
	switch {
	case want == nil && got != nil:
		t.Fatalf("RunOrigin = %+v, want none", *got)
	case want != nil && got == nil:
		t.Fatalf("RunOrigin = nil, want %+v", *want)
	case want != nil && *got != *want:
		t.Fatalf("RunOrigin = %+v, want %+v", *got, *want)
	}
}

// runOriginFake answers RunOrigin from a map and counts lookups.
type runOriginFake struct {
	fakeProvider
	origins map[Ref]*issuesapi.RunOrigin
	lookups map[Ref]int
}

func (f *runOriginFake) RunOrigin(subject Ref) *issuesapi.RunOrigin {
	f.lookups[subject]++
	return f.origins[subject]
}

func TestComposeSetsRunOriginOnRunSubjects(t *testing.T) {
	wf := Ref{Group: "argoproj.io", Kind: "Workflow", Namespace: "etl", Name: "nightly-x7k2p"}
	job := Ref{Group: "batch", Kind: "Job", Namespace: "ops", Name: "backup-29340480"}
	wfOrigin := &issuesapi.RunOrigin{Creator: Ref{Group: "argoproj.io", Kind: "CronWorkflow", Namespace: "etl", Name: "nightly"}}
	jobOrigin := &issuesapi.RunOrigin{Creator: Ref{Group: "batch", Kind: "CronJob", Namespace: "ops", Name: "backup"}}
	newProvider := func() *runOriginFake {
		return &runOriginFake{
			fakeProvider: fakeProvider{problems: []k8s.Detection{
				{Kind: "Pod", Namespace: "etl", Name: "nightly-x7k2p-main-1", Severity: "critical", Reason: "Error",
					OwnerGroup: "argoproj.io", OwnerKind: "Workflow", OwnerName: "nightly-x7k2p"},
				{Kind: "Pod", Namespace: "etl", Name: "nightly-x7k2p-main-2", Severity: "critical", Reason: "Error",
					OwnerGroup: "argoproj.io", OwnerKind: "Workflow", OwnerName: "nightly-x7k2p"},
				{Kind: "Job", Group: "batch", Namespace: "ops", Name: "backup-29340480", Severity: "critical", Reason: "BackoffLimitExceeded"},
				{Kind: "Deployment", Group: "apps", Namespace: "web", Name: "api", Severity: "critical", Reason: "ReplicaFailure"},
			}},
			origins: map[Ref]*issuesapi.RunOrigin{wf: wfOrigin, job: jobOrigin},
			lookups: map[Ref]int{},
		}
	}

	for _, grouped := range []bool{false, true} {
		p := newProvider()
		out := Compose(p, Filters{Grouped: grouped, Limit: NoLimit, SkipPodTemplateContext: true})
		for _, iss := range out {
			subject := Ref{Group: iss.Group, Kind: iss.Kind, Namespace: iss.Namespace, Name: iss.Name}
			if iss.Owner.Kind != "" {
				subject = iss.Owner
			}
			var want *issuesapi.RunOrigin
			switch subject {
			case wf:
				want = wfOrigin
			case job:
				want = jobOrigin
			}
			if iss.RunOrigin != want {
				t.Errorf("grouped=%v %s/%s: RunOrigin = %+v, want %+v", grouped, iss.Kind, iss.Name, iss.RunOrigin, want)
			}
		}
		if p.lookups[wf] != 1 || p.lookups[job] != 1 || len(p.lookups) != 2 {
			t.Errorf("grouped=%v lookups = %v, want one per run subject and none for the Deployment", grouped, p.lookups)
		}
	}
}

// A pod whose Workflow the caller can't read loses its owner, so it is its own
// subject and gets no origin: the Workflow's metadata is never read for it.
func TestComposeRunOriginNeedsAReadableSubject(t *testing.T) {
	wf := Ref{Group: "argoproj.io", Kind: "Workflow", Namespace: "etl", Name: "nightly-x7k2p"}
	p := &runOriginFake{
		fakeProvider: fakeProvider{problems: []k8s.Detection{
			{Kind: "Pod", Namespace: "etl", Name: "nightly-x7k2p-main-1", Severity: "critical", Reason: "Error",
				OwnerGroup: "argoproj.io", OwnerKind: "Workflow", OwnerName: "nightly-x7k2p"},
		}},
		origins: map[Ref]*issuesapi.RunOrigin{wf: {GenerateName: "nightly-"}},
		lookups: map[Ref]int{},
	}
	canRead := func(r Ref) bool { return r.Kind != "Workflow" }
	out := Compose(p, Filters{Grouped: true, Limit: NoLimit, SkipPodTemplateContext: true, CanReadSubject: canRead})
	if len(out) != 1 || out[0].Owner.Kind != "" || out[0].RunOrigin != nil {
		t.Fatalf("got %+v, want the pod alone with no owner and no origin", out)
	}
	if len(p.lookups) != 0 {
		t.Fatalf("lookups = %v, want none", p.lookups)
	}
}

// The real lookup reads the typed Job cache and an already-watched Workflow
// informer, and reports nothing (without starting an informer) when Workflows
// aren't watched.
func TestCacheProviderRunOrigin(t *testing.T) {
	defer k8s.ResetTestState()
	defer k8s.ResetTestDynamicState()

	client := fake.NewClientset(&batchv1.Job{ObjectMeta: metav1.ObjectMeta{Namespace: "ops", Name: "backup-29340480",
		OwnerReferences: []metav1.OwnerReference{controllerRef("batch/v1", "CronJob", "backup")}}})
	if err := k8s.InitTestResourceCache(client); err != nil {
		t.Fatalf("InitTestResourceCache: %v", err)
	}
	wfGVR := schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "workflows"}
	wf := workflow(map[string]string{argoCronWorkflowLabel: "nightly"}, "", nil, nil)
	dynClient := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{wfGVR: "WorkflowList"}, wf)
	if err := k8s.InitTestDynamicResourceCache(dynClient, []k8s.APIResource{{Group: wfGVR.Group, Version: wfGVR.Version,
		Name: wfGVR.Resource, Kind: "Workflow", Namespaced: true, Verbs: []string{"list", "watch"}}}); err != nil {
		t.Fatalf("InitTestDynamicResourceCache: %v", err)
	}
	dynamicCache := k8s.GetDynamicResourceCache()
	provider := &CacheProvider{cache: k8s.GetResourceCache(), dynamic: dynamicCache, discovery: k8s.GetResourceDiscovery()}
	wfRef := Ref{Group: "argoproj.io", Kind: "Workflow", Namespace: wf.GetNamespace(), Name: wf.GetName()}

	assertRunOrigin(t, provider.RunOrigin(Ref{Group: "batch", Kind: "Job", Namespace: "ops", Name: "backup-29340480"}),
		&issuesapi.RunOrigin{Creator: Ref{Group: "batch", Kind: "CronJob", Namespace: "ops", Name: "backup"}})
	assertRunOrigin(t, provider.RunOrigin(Ref{Group: "batch", Kind: "Job", Namespace: "ops", Name: "gone"}), nil)

	assertRunOrigin(t, provider.RunOrigin(wfRef), nil)
	for _, watched := range dynamicCache.GetWatchedResources() {
		if watched == wfGVR {
			t.Fatal("RunOrigin started a Workflow informer")
		}
	}

	if err := dynamicCache.EnsureWatching(wfGVR); err != nil || !dynamicCache.WaitForSync(wfGVR, 5*time.Second) {
		t.Fatalf("Workflow informer did not sync: %v", err)
	}
	assertRunOrigin(t, provider.RunOrigin(wfRef),
		&issuesapi.RunOrigin{Creator: Ref{Group: "argoproj.io", Kind: "CronWorkflow", Namespace: "etl", Name: "nightly"}})
}
