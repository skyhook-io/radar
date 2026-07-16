package upgradereadiness

const ReviewedThrough = "1.36"

var (
	versionSkewReferences = []Reference{{
		Title: "Kubernetes version skew policy",
		URL:   "https://kubernetes.io/releases/version-skew-policy/",
	}}
	manifestAPIReferences = []Reference{{
		Title: "Kubernetes deprecated API migration guide",
		URL:   "https://kubernetes.io/docs/reference/using-api/deprecation-guide/",
	}}
	gitRepoReferences = []Reference{
		{Title: "Kubernetes volumes: gitRepo", URL: "https://kubernetes.io/docs/concepts/storage/volumes/#gitrepo"},
		{Title: "Kubernetes 1.36 removal announcement", URL: "https://kubernetes.io/blog/2026/04/22/kubernetes-v1-36-release/#removal-of-the-gitrepo-volume-driver"},
	}
	externalIPReferences = []Reference{{
		Title: "Service externalIPs deprecation",
		URL:   "https://kubernetes.io/blog/2026/05/14/kubernetes-v1-36-deprecation-and-removal-of-service-externalips/",
	}}
	changelog136References = []Reference{{
		Title: "Kubernetes 1.36 urgent upgrade notes",
		URL:   "https://github.com/kubernetes/kubernetes/blob/master/CHANGELOG/CHANGELOG-1.36.md#urgent-upgrade-notes",
	}}
)
