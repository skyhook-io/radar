package upgradereadiness

const ReviewedThrough = "1.36"

type rule struct {
	id            string
	title         string
	deprecatedIn  string
	appliesFrom   string
	scan          func(*Input, *workloadIndex, rule, Level) []Finding
	impact        string
	warningImpact string
	remediation   string
	references    []Reference
}

var catalog = []rule{
	{
		id:            "gitrepo-volume-removed",
		title:         "gitRepo volume driver removed",
		deprecatedIn:  "1.11",
		appliesFrom:   "1.36",
		scan:          scanGitRepo,
		impact:        "Pods that reference a gitRepo volume cannot start because Kubernetes no longer includes the volume driver.",
		warningImpact: "gitRepo is deprecated and will be removed in Kubernetes 1.36; this workload will break on that upgrade.",
		remediation:   "Clone the repository into an emptyDir from an init container, then mount that emptyDir into the workload containers.",
		references: []Reference{
			{Title: "Kubernetes volumes: gitRepo", URL: "https://kubernetes.io/docs/concepts/storage/volumes/#gitrepo"},
			{Title: "Kubernetes v1.36 removal announcement", URL: "https://kubernetes.io/blog/2026/03/30/kubernetes-v1-36-sneak-peek/#removal-of-gitrepo-volume-driver"},
		},
	},
}
