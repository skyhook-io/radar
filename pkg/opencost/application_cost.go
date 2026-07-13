package opencost

import "sort"

func BuildApplicationCostResponse(inputs []ApplicationWorkloadCostInput, unavailable []ApplicationWorkloadStatus, unsupported []ApplicationWorkloadRef, namespaceCosts map[string]*WorkloadCostResponse) *ApplicationCostResponse {
	out := &ApplicationCostResponse{
		Coverage: ApplicationCostCoverage{
			Total:       len(inputs) + len(unavailable) + len(unsupported),
			Unavailable: append([]ApplicationWorkloadStatus(nil), unavailable...),
			Unsupported: append([]ApplicationWorkloadRef(nil), unsupported...),
		},
	}

	for _, status := range unavailable {
		out.Workloads = append(out.Workloads, ApplicationWorkloadCost{
			ApplicationWorkloadRef: status.ApplicationWorkloadRef,
			Reason:                 status.Reason,
			ScaledToZero:           status.ScaledToZero,
		})
	}

	for _, input := range inputs {
		row := focusApplicationWorkloadCost(input, namespaceCosts[input.Namespace])
		out.Workloads = append(out.Workloads, row)
		if row.Available && row.Current != nil {
			out.Coverage.Included++
			addApplicationCostTotal(&out.Totals, *row.Current)
			continue
		}
		out.Coverage.Unavailable = append(out.Coverage.Unavailable, ApplicationWorkloadStatus{
			ApplicationWorkloadRef: input.ApplicationWorkloadRef,
			Reason:                 row.Reason,
			ScaledToZero:           row.ScaledToZero,
		})
	}

	finalizeApplicationCostTotals(&out.Totals)
	sort.Slice(out.Workloads, func(i, j int) bool {
		left, right := out.Workloads[i], out.Workloads[j]
		leftCost, rightCost := 0.0, 0.0
		if left.Current != nil {
			leftCost = left.Current.HourlyCost
		}
		if right.Current != nil {
			rightCost = right.Current.HourlyCost
		}
		if leftCost != rightCost {
			return leftCost > rightCost
		}
		return applicationRefSortKey(left.ApplicationWorkloadRef) < applicationRefSortKey(right.ApplicationWorkloadRef)
	})
	sortApplicationRefs(out.Coverage.Unsupported)
	sortApplicationStatuses(out.Coverage.Unavailable)

	out.Available = out.Coverage.Included > 0
	out.Partial = len(out.Coverage.Unsupported) > 0 || len(out.Coverage.Unavailable) > 0
	if !out.Available {
		out.Reason = applicationUnavailableReason(out.Coverage.Unavailable)
	}
	return out
}

func UnavailableApplicationCostResponse(inputs []ApplicationWorkloadCostInput, unavailable []ApplicationWorkloadStatus, unsupported []ApplicationWorkloadRef, reason string) *ApplicationCostResponse {
	statuses := make([]ApplicationWorkloadStatus, 0, len(inputs)+len(unavailable))
	statuses = append(statuses, unavailable...)
	for _, input := range inputs {
		statuses = append(statuses, ApplicationWorkloadStatus{
			ApplicationWorkloadRef: input.ApplicationWorkloadRef,
			Reason:                 reason,
		})
	}
	sortApplicationStatuses(statuses)
	unsupportedCopy := append([]ApplicationWorkloadRef(nil), unsupported...)
	sortApplicationRefs(unsupportedCopy)
	return &ApplicationCostResponse{
		Available: false,
		Reason:    reason,
		Partial:   len(unsupportedCopy) > 0 || len(unavailable) > 0,
		Coverage: ApplicationCostCoverage{
			Total:       len(inputs) + len(unavailable) + len(unsupportedCopy),
			Unavailable: statuses,
			Unsupported: unsupportedCopy,
		},
	}
}

func focusApplicationWorkloadCost(input ApplicationWorkloadCostInput, resp *WorkloadCostResponse) ApplicationWorkloadCost {
	row := ApplicationWorkloadCost{ApplicationWorkloadRef: input.ApplicationWorkloadRef}
	if resp == nil {
		row.Reason = ReasonQueryError
		return row
	}
	if resp.Available {
		for i := range resp.Workloads {
			wl := resp.Workloads[i]
			if wl.Kind == input.Kind && wl.Name == input.Name {
				row.Available = true
				row.Current = &wl
				return row
			}
		}
		if input.DesiredReplicas == 0 {
			row.Available = true
			row.ScaledToZero = true
			row.Current = zeroApplicationWorkloadCost(input.Kind, input.Name)
			return row
		}
		row.Reason = ReasonNoMetrics
		return row
	}
	if resp.Reason == ReasonNoMetrics && input.DesiredReplicas == 0 {
		row.Available = true
		row.ScaledToZero = true
		row.Current = zeroApplicationWorkloadCost(input.Kind, input.Name)
		return row
	}
	row.Reason = resp.Reason
	if row.Reason == "" {
		row.Reason = ReasonQueryError
	}
	return row
}

func zeroApplicationWorkloadCost(kind, name string) *WorkloadCost {
	return &WorkloadCost{
		Name:       name,
		Kind:       kind,
		HourlyCost: 0,
		CPUCost:    0,
		MemoryCost: 0,
		Replicas:   0,
		Efficiency: 0,
		IdleCost:   0,
	}
}

func addApplicationCostTotal(total *ApplicationCostTotals, wl WorkloadCost) {
	if wl.CPUCost > 0 {
		if total.CPUCost == 0 {
			total.CPUUsageAvailable = wl.CPUUsageAvailable
		} else {
			total.CPUUsageAvailable = total.CPUUsageAvailable && wl.CPUUsageAvailable
		}
	}
	if wl.MemoryCost > 0 {
		if total.MemoryCost == 0 {
			total.MemoryUsageAvailable = wl.MemoryUsageAvailable
		} else {
			total.MemoryUsageAvailable = total.MemoryUsageAvailable && wl.MemoryUsageAvailable
		}
	}
	total.HourlyCost += wl.HourlyCost
	total.CPUCost += wl.CPUCost
	total.MemoryCost += wl.MemoryCost
	total.Replicas += wl.Replicas
	total.CPUUsageCost += wl.CPUUsageCost
	total.MemoryUsageCost += wl.MemoryUsageCost
}

func finalizeApplicationCostTotals(total *ApplicationCostTotals) {
	if total.CPUUsageAvailable {
		total.CPUAllocationUse = efficiencyPct(total.CPUUsageCost, total.CPUCost)
	}
	if total.MemoryUsageAvailable {
		total.MemoryAllocationUse = efficiencyPct(total.MemoryUsageCost, total.MemoryCost)
	}
	total.HourlyCost = roundTo(total.HourlyCost, 4)
	total.CPUCost = roundTo(total.CPUCost, 4)
	total.MemoryCost = roundTo(total.MemoryCost, 4)
	total.CPUUsageCost = roundTo(total.CPUUsageCost, 4)
	total.MemoryUsageCost = roundTo(total.MemoryUsageCost, 4)
}

func applicationUnavailableReason(statuses []ApplicationWorkloadStatus) string {
	for _, status := range statuses {
		if status.Reason == ReasonQueryError || status.Reason == ReasonNoPrometheus || status.Reason == ReasonAccessDenied {
			return status.Reason
		}
	}
	if len(statuses) > 0 {
		return statuses[0].Reason
	}
	return ReasonNoMetrics
}

func sortApplicationRefs(refs []ApplicationWorkloadRef) {
	sort.Slice(refs, func(i, j int) bool {
		return applicationRefSortKey(refs[i]) < applicationRefSortKey(refs[j])
	})
}

func sortApplicationStatuses(statuses []ApplicationWorkloadStatus) {
	sort.Slice(statuses, func(i, j int) bool {
		return applicationRefSortKey(statuses[i].ApplicationWorkloadRef) < applicationRefSortKey(statuses[j].ApplicationWorkloadRef)
	})
}

func applicationRefSortKey(ref ApplicationWorkloadRef) string {
	return ref.Namespace + "/" + ref.Kind + "/" + ref.Name
}
