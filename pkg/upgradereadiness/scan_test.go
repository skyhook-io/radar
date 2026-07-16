package upgradereadiness

import (
	"errors"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func completeInput() *Input {
	return &Input{
		Pods:         []*corev1.Pod{},
		Deployments:  []*appsv1.Deployment{},
		ReplicaSets:  []*appsv1.ReplicaSet{},
		StatefulSets: []*appsv1.StatefulSet{},
		DaemonSets:   []*appsv1.DaemonSet{},
		Jobs:         []*batchv1.Job{},
		CronJobs:     []*batchv1.CronJob{},
	}
}

func gitRepoSpec() corev1.PodSpec {
	return corev1.PodSpec{Volumes: []corev1.Volume{{
		Name: "source",
		VolumeSource: corev1.VolumeSource{GitRepo: &corev1.GitRepoVolumeSource{
			Repository: "https://example.com/repo.git",
		}},
	}}}
}

func TestScanGitRepoAcrossWorkloadKinds(t *testing.T) {
	input := completeInput()
	input.Pods = []*corev1.Pod{{ObjectMeta: metav1.ObjectMeta{Name: "pod", Namespace: "ns"}, Spec: gitRepoSpec()}}
	input.Deployments = []*appsv1.Deployment{{ObjectMeta: metav1.ObjectMeta{Name: "deploy", Namespace: "ns"}, Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: gitRepoSpec()}}}}
	input.ReplicaSets = []*appsv1.ReplicaSet{{ObjectMeta: metav1.ObjectMeta{Name: "replica", Namespace: "ns"}, Spec: appsv1.ReplicaSetSpec{Template: corev1.PodTemplateSpec{Spec: gitRepoSpec()}}}}
	input.StatefulSets = []*appsv1.StatefulSet{{ObjectMeta: metav1.ObjectMeta{Name: "stateful", Namespace: "ns"}, Spec: appsv1.StatefulSetSpec{Template: corev1.PodTemplateSpec{Spec: gitRepoSpec()}}}}
	input.DaemonSets = []*appsv1.DaemonSet{{ObjectMeta: metav1.ObjectMeta{Name: "daemon", Namespace: "ns"}, Spec: appsv1.DaemonSetSpec{Template: corev1.PodTemplateSpec{Spec: gitRepoSpec()}}}}
	input.Jobs = []*batchv1.Job{{ObjectMeta: metav1.ObjectMeta{Name: "job", Namespace: "ns"}, Spec: batchv1.JobSpec{Template: corev1.PodTemplateSpec{Spec: gitRepoSpec()}}}}
	input.CronJobs = []*batchv1.CronJob{{ObjectMeta: metav1.ObjectMeta{Name: "cron", Namespace: "ns"}, Spec: batchv1.CronJobSpec{JobTemplate: batchv1.JobTemplateSpec{Spec: batchv1.JobSpec{Template: corev1.PodTemplateSpec{Spec: gitRepoSpec()}}}}}}

	got, err := Scan(input, "v1.35.7", "1.36")
	if err != nil {
		t.Fatal(err)
	}
	if got.Verdict != VerdictBlocked || got.Summary.Blockers != 7 || got.Summary.Scanned != 7 {
		t.Fatalf("unexpected result: verdict=%s summary=%+v", got.Verdict, got.Summary)
	}

	wantPaths := map[string]string{
		"Pod":         "spec.volumes[0].gitRepo",
		"Deployment":  "spec.template.spec.volumes[0].gitRepo",
		"ReplicaSet":  "spec.template.spec.volumes[0].gitRepo",
		"StatefulSet": "spec.template.spec.volumes[0].gitRepo",
		"DaemonSet":   "spec.template.spec.volumes[0].gitRepo",
		"Job":         "spec.template.spec.volumes[0].gitRepo",
		"CronJob":     "spec.jobTemplate.spec.template.spec.volumes[0].gitRepo",
	}
	for _, finding := range got.Findings {
		if want := wantPaths[finding.Resource.Kind]; finding.Evidence.Path != want {
			t.Errorf("%s path = %q, want %q", finding.Resource.Kind, finding.Evidence.Path, want)
		}
		if finding.Level != LevelBlocker || finding.AppliesFrom != "1.36" {
			t.Errorf("unexpected boundary for %s: %+v", finding.Resource.Kind, finding)
		}
	}
}

func TestScanTreatsPreRemovalUseAsReview(t *testing.T) {
	input := completeInput()
	input.Deployments = []*appsv1.Deployment{{ObjectMeta: metav1.ObjectMeta{Name: "deploy", Namespace: "ns"}, Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: gitRepoSpec()}}}}

	got, err := Scan(input, "1.34", "1.35")
	if err != nil {
		t.Fatal(err)
	}
	if got.Verdict != VerdictReview || got.Summary.Warnings != 1 || got.Findings[0].Level != LevelWarning {
		t.Fatalf("unexpected pre-removal result: %+v", got)
	}
	if got.Findings[0].Impact != catalog[0].warningImpact {
		t.Fatalf("warning impact = %q", got.Findings[0].Impact)
	}
}

func TestScanCoverageAndTargetHonesty(t *testing.T) {
	t.Run("complete and clear", func(t *testing.T) {
		got, err := Scan(completeInput(), "1.35", "1.36")
		if err != nil {
			t.Fatal(err)
		}
		if got.Verdict != VerdictNoKnownBlockers || got.Coverage.State != "complete" {
			t.Fatalf("unexpected result: %+v", got)
		}
	})

	t.Run("partial is unknown", func(t *testing.T) {
		input := completeInput()
		input.CronJobs = nil
		got, err := Scan(input, "1.35", "1.36")
		if err != nil {
			t.Fatal(err)
		}
		if got.Verdict != VerdictUnknown || got.Coverage.State != "partial" || len(got.Coverage.UnavailableKinds) != 1 || got.Coverage.UnavailableKinds[0] != "cronjobs" {
			t.Fatalf("unexpected result: %+v", got)
		}
	})

	t.Run("partial with warning is still unknown", func(t *testing.T) {
		input := completeInput()
		input.Pods = nil
		input.Deployments = []*appsv1.Deployment{{ObjectMeta: metav1.ObjectMeta{Name: "deploy", Namespace: "ns"}, Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: gitRepoSpec()}}}}
		got, err := Scan(input, "1.34", "1.35")
		if err != nil {
			t.Fatal(err)
		}
		if got.Verdict != VerdictUnknown || got.Summary.Warnings != 1 {
			t.Fatalf("unexpected result: %+v", got)
		}
	})

	t.Run("unsupported target retains definite blockers", func(t *testing.T) {
		input := completeInput()
		input.Pods = []*corev1.Pod{{ObjectMeta: metav1.ObjectMeta{Name: "pod", Namespace: "ns"}, Spec: gitRepoSpec()}}
		got, err := Scan(input, "1.36", "1.37")
		if err != nil {
			t.Fatal(err)
		}
		if got.Verdict != VerdictBlocked || len(got.Findings) != 1 {
			t.Fatalf("unexpected result: %+v", got)
		}
	})

	t.Run("unsupported target without findings is unknown", func(t *testing.T) {
		got, err := Scan(completeInput(), "1.36", "1.37")
		if err != nil {
			t.Fatal(err)
		}
		if got.Verdict != VerdictUnknown {
			t.Fatalf("unexpected result: %+v", got)
		}
	})
}

func TestScanSkipsControllerOwnedPodsAndReportsManager(t *testing.T) {
	controller := true
	input := completeInput()
	input.Pods = []*corev1.Pod{{
		ObjectMeta: metav1.ObjectMeta{
			Name: "deploy-abc", Namespace: "ns",
			OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "ReplicaSet", Name: "deploy-abc", Controller: &controller}},
		},
		Spec: gitRepoSpec(),
	}}
	input.Deployments = []*appsv1.Deployment{{
		ObjectMeta: metav1.ObjectMeta{
			Name: "deploy", Namespace: "ns",
			Annotations: map[string]string{
				"meta.helm.sh/release-name":      "api",
				"meta.helm.sh/release-namespace": "ns",
			},
		},
		Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: gitRepoSpec()}},
	}}
	input.ReplicaSets = []*appsv1.ReplicaSet{{
		ObjectMeta: metav1.ObjectMeta{
			Name: "deploy-abc", Namespace: "ns",
			OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "Deployment", Name: "deploy", Controller: &controller}},
		},
		Spec: appsv1.ReplicaSetSpec{Template: corev1.PodTemplateSpec{Spec: gitRepoSpec()}},
	}}

	got, err := Scan(input, "1.35", "1.36")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Findings) != 1 || got.Summary.Scanned != 1 {
		t.Fatalf("owned pod was not deduplicated: %+v", got)
	}
	if got.Findings[0].ManagedBy == nil || got.Findings[0].ManagedBy.Kind != "HelmRelease" || got.Findings[0].ManagedBy.Name != "api" {
		t.Fatalf("manager = %+v", got.Findings[0].ManagedBy)
	}
}

func TestScanDoesNotSkipPodsOwnedByUnscannedControllers(t *testing.T) {
	controller := true
	input := completeInput()
	input.Pods = []*corev1.Pod{{
		ObjectMeta: metav1.ObjectMeta{
			Name: "rollout-pod", Namespace: "ns",
			OwnerReferences: []metav1.OwnerReference{{APIVersion: "argoproj.io/v1alpha1", Kind: "Rollout", Name: "api", Controller: &controller}},
		},
		Spec: gitRepoSpec(),
	}}

	got, err := Scan(input, "1.35", "1.36")
	if err != nil {
		t.Fatal(err)
	}
	if got.Verdict != VerdictBlocked || len(got.Findings) != 1 || got.Findings[0].Resource.Kind != "Pod" || got.Summary.Scanned != 1 {
		t.Fatalf("unscanned controller pod was lost: %+v", got)
	}
}

func TestScanFindsDivergentReplicaSetDuringRollout(t *testing.T) {
	controller := true
	input := completeInput()
	input.Deployments = []*appsv1.Deployment{{
		ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "ns"},
		Spec:       appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{}}},
	}}
	input.ReplicaSets = []*appsv1.ReplicaSet{{
		ObjectMeta: metav1.ObjectMeta{
			Name: "api-old", Namespace: "ns",
			OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "Deployment", Name: "api", Controller: &controller}},
		},
		Spec: appsv1.ReplicaSetSpec{Template: corev1.PodTemplateSpec{Spec: gitRepoSpec()}},
	}}
	input.Pods = []*corev1.Pod{{
		ObjectMeta: metav1.ObjectMeta{
			Name: "api-old-pod", Namespace: "ns",
			OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "ReplicaSet", Name: "api-old", Controller: &controller}},
		},
		Spec: gitRepoSpec(),
	}}

	got, err := Scan(input, "1.35", "1.36")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Findings) != 1 || got.Findings[0].Resource.Kind != "ReplicaSet" || got.Findings[0].Resource.Name != "api-old" {
		t.Fatalf("divergent live ReplicaSet was lost: %+v", got)
	}
}

func TestScanDeduplicatesCronJobOwnedJobs(t *testing.T) {
	controller := true
	input := completeInput()
	input.CronJobs = []*batchv1.CronJob{{
		ObjectMeta: metav1.ObjectMeta{Name: "sync", Namespace: "ns"},
		Spec:       batchv1.CronJobSpec{JobTemplate: batchv1.JobTemplateSpec{Spec: batchv1.JobSpec{Template: corev1.PodTemplateSpec{Spec: gitRepoSpec()}}}},
	}}
	input.Jobs = []*batchv1.Job{{
		ObjectMeta: metav1.ObjectMeta{
			Name: "sync-123", Namespace: "ns",
			OwnerReferences: []metav1.OwnerReference{{APIVersion: "batch/v1", Kind: "CronJob", Name: "sync", Controller: &controller}},
		},
		Spec: batchv1.JobSpec{Template: corev1.PodTemplateSpec{Spec: gitRepoSpec()}},
	}}

	got, err := Scan(input, "1.35", "1.36")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Findings) != 1 || got.Findings[0].Resource.Kind != "CronJob" || got.Summary.Scanned != 1 {
		t.Fatalf("CronJob-owned Job was not deduplicated: %+v", got)
	}
}

func TestScanOwnerMatchingIncludesAPIGroup(t *testing.T) {
	controller := true
	input := completeInput()
	input.Jobs = []*batchv1.Job{{ObjectMeta: metav1.ObjectMeta{Name: "trainer", Namespace: "ns"}}}
	input.Pods = []*corev1.Pod{{
		ObjectMeta: metav1.ObjectMeta{
			Name: "trainer-pod", Namespace: "ns",
			OwnerReferences: []metav1.OwnerReference{{APIVersion: "batch.volcano.sh/v1alpha1", Kind: "Job", Name: "trainer", Controller: &controller}},
		},
		Spec: gitRepoSpec(),
	}}

	got, err := Scan(input, "1.35", "1.36")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Findings) != 1 || got.Findings[0].Resource.Kind != "Pod" {
		t.Fatalf("colliding custom Job owner hid Pod: %+v", got)
	}
}

func TestScanVersionValidationAndDefault(t *testing.T) {
	got, err := Scan(completeInput(), "v1.35.4+k3s1", "")
	if err != nil {
		t.Fatal(err)
	}
	if got.CurrentVersion != "1.35" || got.TargetVersion != "1.36" {
		t.Fatalf("versions = %s -> %s", got.CurrentVersion, got.TargetVersion)
	}

	if _, err := Scan(completeInput(), "not-a-version", "1.36"); !errors.Is(err, ErrInvalidCurrentVersion) {
		t.Fatalf("invalid current error = %v", err)
	}
	if _, err := Scan(completeInput(), "1.35", "wat"); !errors.Is(err, ErrInvalidTargetVersion) {
		t.Fatalf("invalid target error = %v", err)
	}
	if _, err := Scan(completeInput(), "1.35", "1.36-nonsense"); !errors.Is(err, ErrInvalidTargetVersion) {
		t.Fatalf("suffixed target error = %v", err)
	}
	if _, err := Scan(completeInput(), "1.35", "1.35"); !errors.Is(err, ErrNonForwardTarget) {
		t.Fatalf("non-forward error = %v", err)
	}
}

func TestCatalogHasActionableMetadata(t *testing.T) {
	for _, r := range catalog {
		if r.id == "" || r.title == "" || r.deprecatedIn == "" || r.appliesFrom == "" || r.scan == nil || r.impact == "" || r.warningImpact == "" || r.remediation == "" || len(r.references) == 0 {
			t.Fatalf("incomplete rule: %+v", r)
		}
		for _, ref := range r.references {
			if ref.Title == "" || ref.URL == "" {
				t.Fatalf("incomplete reference in %s: %+v", r.id, ref)
			}
		}
	}
}
