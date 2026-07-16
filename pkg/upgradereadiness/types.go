package upgradereadiness

import (
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
)

type Verdict string

const (
	VerdictBlocked         Verdict = "blocked"
	VerdictReview          Verdict = "review"
	VerdictNoKnownBlockers Verdict = "no_known_blockers"
	VerdictUnknown         Verdict = "unknown"
)

type Level string

const (
	LevelBlocker Level = "blocker"
	LevelWarning Level = "warning"
)

type Input struct {
	Pods         []*corev1.Pod
	Deployments  []*appsv1.Deployment
	ReplicaSets  []*appsv1.ReplicaSet
	StatefulSets []*appsv1.StatefulSet
	DaemonSets   []*appsv1.DaemonSet
	Jobs         []*batchv1.Job
	CronJobs     []*batchv1.CronJob
}

type ResourceRef struct {
	Group     string `json:"group,omitempty"`
	Kind      string `json:"kind"`
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
}

type Evidence struct {
	Source string `json:"source"`
	Path   string `json:"path"`
}

type Reference struct {
	Title string `json:"title"`
	URL   string `json:"url"`
}

type Finding struct {
	RuleID      string       `json:"ruleID"`
	Title       string       `json:"title"`
	Level       Level        `json:"level"`
	Resource    ResourceRef  `json:"resource"`
	ManagedBy   *ResourceRef `json:"managedBy,omitempty"`
	Evidence    Evidence     `json:"evidence"`
	AppliesFrom string       `json:"appliesFrom"`
	Impact      string       `json:"impact"`
	Remediation string       `json:"remediation"`
	References  []Reference  `json:"references"`
}

type Summary struct {
	Blockers int `json:"blockers"`
	Warnings int `json:"warnings"`
	Scanned  int `json:"scanned"`
}

type Coverage struct {
	Source           string   `json:"source"`
	State            string   `json:"state"`
	UnavailableKinds []string `json:"unavailableKinds,omitempty"`
}

type RuleSummary struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	AppliesFrom string `json:"appliesFrom"`
}

type ScanResults struct {
	CurrentVersion  string        `json:"currentVersion"`
	TargetVersion   string        `json:"targetVersion"`
	ReviewedThrough string        `json:"reviewedThrough"`
	Verdict         Verdict       `json:"verdict"`
	Summary         Summary       `json:"summary"`
	Findings        []Finding     `json:"findings"`
	Coverage        Coverage      `json:"coverage"`
	RulesEvaluated  []RuleSummary `json:"rulesEvaluated"`
}
