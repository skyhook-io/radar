package issues

import (
	batchv1 "k8s.io/api/batch/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/skyhook-io/radar/pkg/issuesapi"
)

const (
	argoGroup                 = "argoproj.io"
	argoCronWorkflowLabel     = "workflows.argoproj.io/cron-workflow"
	argoWorkflowTemplateLabel = "workflows.argoproj.io/workflow-template"
	argoClusterTemplateLabel  = "workflows.argoproj.io/cluster-workflow-template"
)

// runOriginProvider looks up a run subject's origin from the caches. Optional:
// a Provider without it leaves RunOrigin unset.
type runOriginProvider interface {
	RunOrigin(subject Ref) *issuesapi.RunOrigin
}

// isRunSubject reports whether a subject is one whose every run is a new
// object: a batch Job or an Argo Workflow. Other workloads keep one name, and
// so one issue ID, across restarts and rollouts.
func isRunSubject(r Ref) bool {
	return (r.Group == "batch" && r.Kind == "Job") || (r.Group == argoGroup && r.Kind == "Workflow")
}

// enrichRunOrigins sets RunOrigin on every row whose subject is a run. It runs
// after the subject and owner access passes, so the subject it reads is one
// the caller may read, and everything it reports is that object's own
// metadata. Rows of one subject share a single lookup.
func enrichRunOrigins(in []Issue, p Provider) []Issue {
	rp, ok := p.(runOriginProvider)
	if !ok {
		return in
	}
	seen := map[Ref]*issuesapi.RunOrigin{}
	for idx := range in {
		subject := Ref{Group: in[idx].Group, Kind: in[idx].Kind, Namespace: in[idx].Namespace, Name: in[idx].Name}
		if in[idx].Owner.Kind != "" {
			subject = in[idx].Owner
		}
		if !isRunSubject(subject) {
			continue
		}
		origin, done := seen[subject]
		if !done {
			origin = rp.RunOrigin(subject)
			seen[subject] = origin
		}
		in[idx].RunOrigin = origin
	}
	return in
}

// jobRunOrigin is a Job's controller owner (the CronJob that scheduled it, or
// any other controller) and its generateName.
func jobRunOrigin(job *batchv1.Job) *issuesapi.RunOrigin {
	if job == nil {
		return nil
	}
	return newRunOrigin(controllerCreator(job), job.GetGenerateName())
}

// workflowRunOrigin is what an Argo Workflow was submitted from, most specific
// first: its CronWorkflow (controller owner, else Argo's label), then the
// template it references (spec.workflowTemplateRef, else Argo's labels). Argo
// sets the labels on every Workflow it creates from those objects, and the
// owner reference only for a CronWorkflow.
func workflowRunOrigin(u *unstructured.Unstructured) *issuesapi.RunOrigin {
	if u == nil {
		return nil
	}
	ns := u.GetNamespace()
	labels := u.GetLabels()
	creator := controllerCreator(u)
	if creator.Kind == "" && labels[argoCronWorkflowLabel] != "" {
		creator = Ref{Group: argoGroup, Kind: "CronWorkflow", Namespace: ns, Name: labels[argoCronWorkflowLabel]}
	}
	if creator.Kind == "" {
		name, _, _ := unstructured.NestedString(u.Object, "spec", "workflowTemplateRef", "name")
		clusterScope, _, _ := unstructured.NestedBool(u.Object, "spec", "workflowTemplateRef", "clusterScope")
		switch {
		case name != "" && clusterScope:
			creator = Ref{Group: argoGroup, Kind: "ClusterWorkflowTemplate", Name: name}
		case name != "":
			creator = Ref{Group: argoGroup, Kind: "WorkflowTemplate", Namespace: ns, Name: name}
		case labels[argoWorkflowTemplateLabel] != "":
			creator = Ref{Group: argoGroup, Kind: "WorkflowTemplate", Namespace: ns, Name: labels[argoWorkflowTemplateLabel]}
		case labels[argoClusterTemplateLabel] != "":
			creator = Ref{Group: argoGroup, Kind: "ClusterWorkflowTemplate", Name: labels[argoClusterTemplateLabel]}
		}
	}
	return newRunOrigin(creator, u.GetGenerateName())
}

// controllerCreator is an object's controller owner reference as a Ref, in the
// object's namespace: the controllers that create runs (CronJob, CronWorkflow)
// are namespaced, and an owner reference can't point to another namespace.
func controllerCreator(obj metav1.Object) Ref {
	owner := metav1.GetControllerOfNoCopy(obj)
	if owner == nil || owner.Kind == "" || owner.Name == "" {
		return Ref{}
	}
	return Ref{
		Group:     schema.FromAPIVersionAndKind(owner.APIVersion, owner.Kind).Group,
		Kind:      owner.Kind,
		Namespace: obj.GetNamespace(),
		Name:      owner.Name,
	}
}

func newRunOrigin(creator Ref, generateName string) *issuesapi.RunOrigin {
	if creator.Kind == "" && generateName == "" {
		return nil
	}
	return &issuesapi.RunOrigin{Creator: creator, GenerateName: generateName}
}
