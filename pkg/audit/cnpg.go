package audit

import (
	"github.com/skyhook-io/radar/pkg/cnpg"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

const (
	checkCNPGNoDeclarativeBackup    = "cnpgNoDeclarativeBackup"
	checkCNPGNoBackupDestination    = "cnpgNoBackupDestination"
	checkCNPGNoWALArchiving         = "cnpgNoWALArchiving"
	checkCNPGSingleInstance         = "cnpgSingleInstance"
	checkCNPGInTreeBackupDeprecated = "cnpgInTreeBackupDeprecated"
)

// cnpgBarmanPluginName is the barman-cloud CNPG-I plugin. Clusters that have
// migrated to it keep their backup config in an ObjectStore CR in a different
// API group, and CNPG stops publishing the in-tree RPO status fields.
const cnpgBarmanPluginName = cnpg.BarmanPluginName

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
		// A schedule can't run without a destination; that Cluster is the
		// destination rule's finding, one cause, one finding.
		d := cnpg.ParseBackupDeclaration(c)
		if !d.HasDestination() {
			continue
		}
		ns, name := c.GetNamespace(), c.GetName()
		tr.record(checkCNPGNoDeclarativeBackup, ns)
		if scheduled[ns+"\x00"+name] {
			continue
		}
		findings = append(findings, Finding{
			Kind:      "Cluster",
			Group:     c.GroupVersionKind().Group,
			Namespace: ns,
			Name:      name,
			CheckID:   checkCNPGNoDeclarativeBackup,
			Category:  CategoryReliability,
			Severity:  SeverityWarning,
			Message:   cnpgNoScheduleMessage(d),
		})
	}
	return findings
}

func cnpgNoScheduleMessage(d cnpg.BackupDeclaration) string {
	const base = "No ScheduledBackup targets this PostgreSQL cluster, so no backup schedule is declared in the cluster. " +
		"On-demand Backups or an external scheduler may still cover it — this reports what is declared, not whether backups exist."
	switch {
	case hasBarmanPlugin(d):
		return base + " Backup for this cluster runs through the barman-cloud plugin; a ScheduledBackup with spec.method: plugin is what schedules it."
	case d.InTreeConfigured:
		return base + " A backup destination is configured (spec.backup.barmanObjectStore), but nothing triggers it on a schedule."
	default:
		return base
	}
}

// checkCNPGClusterPosture reports what each CNPG Cluster declares about
// backups, WAL archiving and instances. It reads only the Cluster object, so
// unlike the schedule check it needs no inventory gate. Like that check it
// says what CNPG is configured to do: a dump job or storage snapshots outside
// CNPG are invisible here, so no message claims a database has no backups.
func checkCNPGClusterPosture(tr *evalTracker, input *CheckInput) []Finding {
	if input == nil {
		return nil
	}
	var findings []Finding
	for _, c := range input.CNPGClusters {
		if c == nil {
			continue
		}
		ns := c.GetNamespace()
		d := cnpg.ParseBackupDeclaration(c)
		emit := func(id, message string) {
			findings = append(findings, Finding{
				Kind: "Cluster", Group: c.GroupVersionKind().Group, Namespace: ns, Name: c.GetName(),
				CheckID: id, Category: CategoryReliability, Severity: SeverityWarning, Message: message,
			})
		}

		tr.record(checkCNPGNoBackupDestination, ns)
		if !d.HasDestination() {
			emit(checkCNPGNoBackupDestination, "This PostgreSQL cluster declares no backup destination, so CNPG can't take a backup of it, on demand or on a schedule. "+
				"A dump job or storage snapshots outside CNPG may still cover it; this reports what CNPG is configured to do.")
		} else {
			// Only a Cluster with a destination is evaluated: without one,
			// WAL archiving has nowhere to go and the destination finding
			// already names the cause.
			tr.record(checkCNPGNoWALArchiving, ns)
			if !d.ArchivesWAL() {
				emit(checkCNPGNoWALArchiving, "This PostgreSQL cluster has a backup destination, but CNPG archives no WAL for it, so it can be restored only to a backup, not to a point in time between backups. "+
					"WAL shipped by a tool outside CNPG isn't visible here; this reports what CNPG is configured to do.")
			}
		}

		tr.record(checkCNPGSingleInstance, ns)
		if cnpgDesiredInstances(c) <= 1 {
			emit(checkCNPGSingleInstance, "This PostgreSQL cluster runs one instance, so CNPG has no standby to fail over to: while its node or volume is down, the database is down.")
		}

		tr.record(checkCNPGInTreeBackupDeprecated, ns)
		if _, plugin := d.BarmanPlugin(); d.InTreeDestination != "" && !plugin {
			emit(checkCNPGInTreeBackupDeprecated, "This PostgreSQL cluster backs up through CNPG's in-tree spec.backup.barmanObjectStore, which CNPG deprecated in 1.26 in favour of the barman-cloud plugin. "+
				"Its backups keep running for now; this reports the configuration, not a failure.")
		}
	}
	return findings
}

// cnpgDesiredInstances reads spec.instances; unset means CNPG's default of 1.
func cnpgDesiredInstances(c *unstructured.Unstructured) int64 {
	v, _, _ := unstructured.NestedFieldNoCopy(c.Object, "spec", "instances")
	switch v := v.(type) {
	case int64:
		return v
	case int:
		return int64(v)
	case float64:
		return int64(v)
	}
	return 1
}

func hasBarmanPlugin(d cnpg.BackupDeclaration) bool {
	_, present := d.BarmanPlugin()
	return present
}
