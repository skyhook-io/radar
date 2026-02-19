package context

// ResourceRef identifies a K8s resource.
type ResourceRef struct {
	Kind      string
	Namespace string
	Name      string
}

// Relationships describes how a resource relates to others.
type Relationships struct {
	Owner       *ResourceRef
	Children    []ResourceRef
	Services    []ResourceRef
	Ingresses   []ResourceRef
	Gateways    []ResourceRef
	Routes      []ResourceRef
	ConfigRefs  []ResourceRef
	Consumers   []ResourceRef
	Scalers     []ResourceRef
	ScaleTarget *ResourceRef
	Pods        []ResourceRef
}

// ContainerUsage holds current metrics for a single container.
type ContainerUsage struct {
	Name   string
	CPU    string // e.g., "100m"
	Memory string // e.g., "128Mi"
}

// MetricsDataPoint is a single historical metrics sample.
type MetricsDataPoint struct {
	CPU    int64 // nanocores
	Memory int64 // bytes
}

// ContainerHistory holds historical metrics for a container.
type ContainerHistory struct {
	Name       string
	DataPoints []MetricsDataPoint
}

// PodMetricsInput holds all metrics data needed for formatting.
type PodMetricsInput struct {
	Containers []ContainerUsage
	History    []ContainerHistory // nil if no history
}
