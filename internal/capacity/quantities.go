package capacity

import (
	"sort"
	"time"

	"github.com/skyhook-io/radar/pkg/capacityapi"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

func QuantityObservation(resources corev1.ResourceList, certainty capacityapi.Certainty, granularity capacityapi.Granularity, asOf time.Time, sources ...string) capacityapi.QuantityObservation {
	observation := capacityapi.NewQuantityObservation(asOf)
	observation.Resources = ResourceVector(resources)
	observation.Certainty = certainty
	observation.Granularity = granularity
	observation.Sources = append(observation.Sources, sources...)
	return observation
}

func ResourceVector(resources corev1.ResourceList) capacityapi.ResourceVector {
	vector := capacityapi.ResourceVector{}
	for name, quantity := range resources {
		vector[string(name)] = quantity.String()
	}
	return vector
}

func subtractResourceLists(left, right corev1.ResourceList) corev1.ResourceList {
	result := corev1.ResourceList{}
	for name, quantity := range left {
		result[name] = quantity.DeepCopy()
	}
	for name, quantity := range right {
		current := result[name]
		current.Sub(quantity)
		result[name] = current
	}
	return result
}

func subtractLeftResourceLists(left, right corev1.ResourceList) corev1.ResourceList {
	result := make(corev1.ResourceList, len(left))
	for name, quantity := range left {
		current := quantity.DeepCopy()
		current.Sub(right[name])
		result[name] = current
	}
	return result
}

func differenceCertainty(left, right capacityapi.Certainty) capacityapi.Certainty {
	if left == capacityapi.CertaintyUnknown || right == capacityapi.CertaintyUnknown {
		return capacityapi.CertaintyUnknown
	}
	if left == capacityapi.CertaintyExact && right == capacityapi.CertaintyExact {
		return capacityapi.CertaintyExact
	}
	if (left == capacityapi.CertaintyExact || left == capacityapi.CertaintyUpperBound) && (right == capacityapi.CertaintyExact || right == capacityapi.CertaintyLowerBound) {
		return capacityapi.CertaintyUpperBound
	}
	if (left == capacityapi.CertaintyExact || left == capacityapi.CertaintyLowerBound) && (right == capacityapi.CertaintyExact || right == capacityapi.CertaintyUpperBound) {
		return capacityapi.CertaintyLowerBound
	}
	return capacityapi.CertaintyUnknown
}

func limitPressure(provisioned, limits corev1.ResourceList) []capacityapi.LimitPressure {
	resources := make([]string, 0, len(limits))
	for name := range limits {
		resources = append(resources, string(name))
	}
	sort.Strings(resources)

	pressure := make([]capacityapi.LimitPressure, 0, len(resources))
	for _, resourceName := range resources {
		name := corev1.ResourceName(resourceName)
		limit := limits[name]
		current := provisioned[name]
		entry := capacityapi.LimitPressure{
			Resource:    resourceName,
			Provisioned: current.String(),
			Limit:       limit.String(),
			OverLimit:   current.Cmp(limit) > 0,
		}
		if limit.Sign() > 0 {
			percent := current.AsApproximateFloat64() / limit.AsApproximateFloat64() * 100
			entry.Percent = &percent
		}
		pressure = append(pressure, entry)
	}
	return pressure
}

func utilization(usage, allocatable corev1.ResourceList) []capacityapi.ResourceUtilization {
	resources := make([]string, 0, len(usage))
	for name := range usage {
		available := allocatable[name]
		if available.Sign() > 0 {
			resources = append(resources, string(name))
		}
	}
	sort.Strings(resources)

	result := make([]capacityapi.ResourceUtilization, 0, len(resources))
	for _, resourceName := range resources {
		name := corev1.ResourceName(resourceName)
		used := usage[name]
		available := allocatable[name]
		result = append(result, capacityapi.ResourceUtilization{
			Resource:    resourceName,
			Usage:       used.String(),
			Allocatable: available.String(),
			Percent:     used.AsApproximateFloat64() / available.AsApproximateFloat64() * 100,
		})
	}
	return result
}

func MetricsResourceList(cpuNanocores, memoryBytes int64) corev1.ResourceList {
	return corev1.ResourceList{
		corev1.ResourceCPU:    *resource.NewScaledQuantity(cpuNanocores, resource.Nano),
		corev1.ResourceMemory: *resource.NewQuantity(memoryBytes, resource.BinarySI),
	}
}
