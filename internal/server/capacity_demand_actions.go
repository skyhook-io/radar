package server

import (
	capacitymodel "github.com/skyhook-io/radar/internal/capacity"
	"github.com/skyhook-io/radar/pkg/capacityapi"
)

func capacityDemandActions(groups []capacitymodel.DemandGroupModel) []capacityapi.ActionSummary {
	type actionDefinition struct {
		state    capacityapi.DemandState
		code     string
		severity string
	}
	definitions := []actionDefinition{
		{state: capacityapi.DemandBlocked, code: "pending_demand_blocked", severity: "warning"},
		{state: capacityapi.DemandAwaitingCapacity, code: "pending_demand_resource_pressure", severity: "info"},
		{state: capacityapi.DemandUnknown, code: "pending_demand_unclassified", severity: "info"},
	}

	groupIDsByState := make(map[capacityapi.DemandState][]string, len(definitions))
	for _, group := range groups {
		switch group.Group.State {
		case capacityapi.DemandBlocked, capacityapi.DemandAwaitingCapacity, capacityapi.DemandUnknown:
			groupIDsByState[group.Group.State] = append(groupIDsByState[group.Group.State], group.Group.ID)
		}
	}

	actions := make([]capacityapi.ActionSummary, 0, len(definitions))
	for _, definition := range definitions {
		groupIDs := groupIDsByState[definition.state]
		if len(groupIDs) == 0 {
			continue
		}
		action := capacityapi.NewActionSummary()
		action.Code = definition.code
		action.Count = len(groupIDs)
		action.HighestSeverity = definition.severity
		if len(groupIDs) > capacityActionSubjectLimit {
			action.Truncated = true
		}
		actions = append(actions, action)
	}
	return actions
}
