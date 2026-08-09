package audit

import (
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

const checkCNPGNoDeclarativeBackup = "cnpgNoDeclarativeBackup"

// cnpgBarmanPluginName is the barman-cloud CNPG-I plugin. Clusters that have
// migrated to it keep their backup config in an ObjectStore CR in a different
// API group, and CNPG stops publishing the in-tree RPO status fields.
const cnpgBarmanPluginName = "barman-cloud.cloudnative-pg.io"

// checkCNPGDeclarativeBackup flags CNPG Clusters with no ScheduledBackup
// targeting them.
//
// Deliberately narrow: the absence of a ScheduledBackup does NOT prove a
// cluster is unprotected — on-demand Backups, volume snapshots and
// plugin-external schedulers all exist — so the finding asserts only what is
// observable ("no declarative backup schedule"), at posture severity.
//
// Suspended schedules count as present. Suspension is a deliberate operator
// action, and re-reporting it here would duplicate intent as a defect.
func checkCNPGDeclarativeBackup(tr *evalTracker, input *CheckInput) []Finding {
	if input == nil || len(input.CNPGClusters) == 0 {
		return nil
	}
	// Without a synced cluster-wide ScheduledBackup informer the inventory may
	// cover only a subset of namespaces, so "no schedule found" could simply
	// mean "not listed". Assert nothing, and record nothing — a check that
	// couldn't run must not inflate its own passed count.
	if !input.CNPGScheduledBackupsAuthoritative {
		return nil
	}

	// A ScheduledBackup and the Cluster it targets are always in the same
	// namespace; spec.method may be barmanObjectStore, volumeSnapshot or plugin,
	// and all three are declarative schedules.
	scheduled := map[string]bool{}
	for _, sb := range input.CNPGScheduledBackups {
		if sb == nil {
			continue
		}
		target, _, _ := unstructured.NestedString(sb.Object, "spec", "cluster", "name")
		if target == "" {
			continue
		}
		scheduled[sb.GetNamespace()+"\x00"+target] = true
	}

	var findings []Finding
	for _, c := range input.CNPGClusters {
		if c == nil {
			continue
		}
		ns, name := c.GetNamespace(), c.GetName()
		tr.record(checkCNPGNoDeclarativeBackup, ns)
		if scheduled[ns+"\x00"+name] {
			continue
		}
		findings = append(findings, Finding{
			Kind:      "Cluster",
			Namespace: ns,
			Name:      name,
			CheckID:   checkCNPGNoDeclarativeBackup,
			Category:  CategoryReliability,
			Severity:  SeverityWarning,
			Message:   cnpgNoScheduleMessage(c),
		})
	}
	return findings
}

func cnpgNoScheduleMessage(c *unstructured.Unstructured) string {
	const base = "No ScheduledBackup targets this PostgreSQL cluster, so no backup schedule is declared in the cluster. " +
		"On-demand Backups or an external scheduler may still cover it — this reports what is declared, not whether backups exist."
	switch {
	case cnpgHasBarmanPlugin(c):
		return base + " Backup for this cluster runs through the barman-cloud plugin; a ScheduledBackup with spec.method: plugin is what schedules it."
	case cnpgHasInTreeBackup(c):
		return base + " A backup destination is configured (spec.backup.barmanObjectStore), but nothing triggers it on a schedule."
	default:
		return base + " No backup destination is configured on the cluster either."
	}
}

func cnpgHasBarmanPlugin(c *unstructured.Unstructured) bool {
	plugins, found, _ := unstructured.NestedSlice(c.Object, "spec", "plugins")
	if !found {
		return false
	}
	for _, raw := range plugins {
		p, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if name, _ := p["name"].(string); name != cnpgBarmanPluginName {
			continue
		}
		// `enabled` is CRD-defaulted to true, so only an explicit false disables.
		if enabled, present := p["enabled"].(bool); present && !enabled {
			continue
		}
		return true
	}
	return false
}

func cnpgHasInTreeBackup(c *unstructured.Unstructured) bool {
	_, found, _ := unstructured.NestedMap(c.Object, "spec", "backup", "barmanObjectStore")
	return found
}
