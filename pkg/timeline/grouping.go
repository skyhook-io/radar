package timeline

import (
	"sort"

	"github.com/skyhook-io/radar/pkg/health"
)

// GroupEvents groups events according to the specified mode.
// Exported so pkg consumers (e.g. SQLite store) can share the same grouping logic.
func GroupEvents(events []TimelineEvent, mode GroupingMode) []EventGroup {
	switch mode {
	case GroupByOwner:
		return groupByOwner(events)
	case GroupByApp:
		return groupByApp(events)
	case GroupByNamespace:
		return groupByNamespace(events)
	default:
		return nil
	}
}

func groupByOwner(events []TimelineEvent) []EventGroup {
	groupMap := make(map[string]*EventGroup)
	resourceEvents := make(map[string][]TimelineEvent)

	for _, event := range events {
		key := ResourceKey(event.Kind, event.Namespace, event.Name)
		resourceEvents[key] = append(resourceEvents[key], event)
	}

	for _, event := range events {
		if event.IsToplevelWorkload() {
			key := ResourceKey(event.Kind, event.Namespace, event.Name)
			if _, exists := groupMap[key]; !exists {
				groupMap[key] = &EventGroup{
					ID:        key,
					Kind:      event.Kind,
					Name:      event.Name,
					Namespace: event.Namespace,
					Events:    resourceEvents[key],
				}
			}
		}
	}

	for _, event := range events {
		if event.Owner != nil {
			ownerKey := ResourceKey(event.Owner.Kind, event.Namespace, event.Owner.Name)
			if group, exists := groupMap[ownerKey]; exists {
				childKey := ResourceKey(event.Kind, event.Namespace, event.Name)
				childEvents := resourceEvents[childKey]

				found := false
				for i := range group.Children {
					if group.Children[i].ID == childKey {
						found = true
						break
					}
				}
				if !found && len(childEvents) > 0 {
					group.Children = append(group.Children, EventGroup{
						ID:        childKey,
						Kind:      event.Kind,
						Name:      event.Name,
						Namespace: event.Namespace,
						Events:    childEvents,
					})
				}
			}
		}
	}

	var groups []EventGroup
	for _, group := range groupMap {
		// Seed with "no opinion" (empty) rather than HealthUnknown: unknown
		// outranks healthy, so seeding it would pin an all-healthy owner group at
		// unknown forever. worseHealth treats "" as no-opinion and the first real
		// event wins.
		worstHealth := HealthState("")
		group.EventCount = len(group.Events)
		for _, event := range group.Events {
			worstHealth = worseHealth(worstHealth, event.HealthState)
		}
		for i := range group.Children {
			group.EventCount += len(group.Children[i].Events)
			for _, event := range group.Children[i].Events {
				worstHealth = worseHealth(worstHealth, event.HealthState)
			}
		}
		if worstHealth == "" {
			worstHealth = HealthUnknown
		}
		group.HealthState = worstHealth
		groups = append(groups, *group)
	}

	sort.Slice(groups, func(i, j int) bool {
		return groups[i].EventCount > groups[j].EventCount
	})

	return groups
}

func groupByApp(events []TimelineEvent) []EventGroup {
	groupMap := make(map[string]*EventGroup)

	for _, event := range events {
		appLabel := event.GetAppLabel()
		if appLabel == "" {
			appLabel = "__ungrouped__"
		}

		key := event.Namespace + "/" + appLabel
		if group, exists := groupMap[key]; exists {
			group.Events = append(group.Events, event)
			group.EventCount++
			group.HealthState = worseHealth(group.HealthState, event.HealthState)
		} else {
			groupMap[key] = &EventGroup{
				ID:          key,
				Kind:        "App",
				Name:        appLabel,
				Namespace:   event.Namespace,
				Events:      []TimelineEvent{event},
				EventCount:  1,
				HealthState: event.HealthState,
			}
		}
	}

	var groups []EventGroup
	for _, group := range groupMap {
		groups = append(groups, *group)
	}

	sort.Slice(groups, func(i, j int) bool {
		return groups[i].EventCount > groups[j].EventCount
	})

	return groups
}

func groupByNamespace(events []TimelineEvent) []EventGroup {
	groupMap := make(map[string]*EventGroup)

	for _, event := range events {
		ns := event.Namespace
		if ns == "" {
			ns = "__cluster__"
		}

		if group, exists := groupMap[ns]; exists {
			group.Events = append(group.Events, event)
			group.EventCount++
			group.HealthState = worseHealth(group.HealthState, event.HealthState)
		} else {
			groupMap[ns] = &EventGroup{
				ID:          ns,
				Kind:        "Namespace",
				Name:        ns,
				Namespace:   ns,
				Events:      []TimelineEvent{event},
				EventCount:  1,
				HealthState: event.HealthState,
			}
		}
	}

	var groups []EventGroup
	for _, group := range groupMap {
		groups = append(groups, *group)
	}

	sort.Slice(groups, func(i, j int) bool {
		return groups[i].EventCount > groups[j].EventCount
	})

	return groups
}

// worseHealth returns the worse of two health states, delegating to the shared
// health.Rank ordering so the timeline, package rollup, and topology all agree on
// what "worse" means (and on neutral aggregating as most-benign).
func worseHealth(a, b HealthState) HealthState {
	return HealthState(health.WorseOf(health.Level(a), health.Level(b)))
}
