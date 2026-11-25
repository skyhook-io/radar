package k8s

import "testing"

// The capability JSON and the namespace merge depend on each check writing to
// its own field; a swapped accessor would grant one action from another's check.
func TestWorkloadWriteChecksMapToTheirFields(t *testing.T) {
	var p WorkloadWritePermissions
	var e WorkloadWriteCapabilityErrors
	want := []struct {
		group, resource, verb string
		permission, apiError  *bool
	}{
		{"apps", "deployments", "patch", &p.Deployments, &e.Deployments},
		{"apps", "daemonsets", "patch", &p.DaemonSets, &e.DaemonSets},
		{"apps", "statefulsets", "patch", &p.StatefulSets, &e.StatefulSets},
		{"argoproj.io", "rollouts", "patch", &p.Rollouts, &e.Rollouts},
		{"argoproj.io", "workflows", "patch", &p.Workflows, &e.Workflows},
		{"argoproj.io", "workflows", "create", &p.CreateWorkflows, &e.CreateWorkflows},
		{"batch", "cronjobs", "patch", &p.CronJobs, &e.CronJobs},
		{"batch", "jobs", "create", &p.CreateJobs, &e.CreateJobs},
	}
	if len(workloadWriteChecks) != len(want) {
		t.Fatalf("workloadWriteChecks has %d entries, want %d", len(workloadWriteChecks), len(want))
	}
	for i, w := range want {
		c := workloadWriteChecks[i]
		if c.group != w.group || c.resource != w.resource || c.verb != w.verb {
			t.Errorf("check %d = %s/%s %s, want %s/%s %s", i, c.group, c.resource, c.verb, w.group, w.resource, w.verb)
		}
		if c.permission(&p) != w.permission || c.apiError(&e) != w.apiError {
			t.Errorf("check %d (%s %s) writes to the wrong permission or error field", i, w.verb, w.resource)
		}
	}
}

func TestMergeWorkloadWritesKeepsEachFieldSeparate(t *testing.T) {
	fields := []func(*WorkloadWritePermissions) *bool{
		func(p *WorkloadWritePermissions) *bool { return &p.Deployments },
		func(p *WorkloadWritePermissions) *bool { return &p.DaemonSets },
		func(p *WorkloadWritePermissions) *bool { return &p.StatefulSets },
		func(p *WorkloadWritePermissions) *bool { return &p.Rollouts },
		func(p *WorkloadWritePermissions) *bool { return &p.Workflows },
		func(p *WorkloadWritePermissions) *bool { return &p.CreateWorkflows },
		func(p *WorkloadWritePermissions) *bool { return &p.CronJobs },
		func(p *WorkloadWritePermissions) *bool { return &p.CreateJobs },
	}
	errFields := []func(*WorkloadWriteCapabilityErrors) *bool{
		func(e *WorkloadWriteCapabilityErrors) *bool { return &e.Deployments },
		func(e *WorkloadWriteCapabilityErrors) *bool { return &e.DaemonSets },
		func(e *WorkloadWriteCapabilityErrors) *bool { return &e.StatefulSets },
		func(e *WorkloadWriteCapabilityErrors) *bool { return &e.Rollouts },
		func(e *WorkloadWriteCapabilityErrors) *bool { return &e.Workflows },
		func(e *WorkloadWriteCapabilityErrors) *bool { return &e.CreateWorkflows },
		func(e *WorkloadWriteCapabilityErrors) *bool { return &e.CronJobs },
		func(e *WorkloadWriteCapabilityErrors) *bool { return &e.CreateJobs },
	}
	for i := range fields {
		// The namespaced answer reaches only its own field.
		var global, ns WorkloadWritePermissions
		*fields[i](&ns) = true
		MergeWorkloadWrites(&global, ns, WorkloadWriteCapabilityErrors{}, func(_, namespaced, _ bool) bool { return namespaced })
		for j := range fields {
			if *fields[j](&global) != (i == j) {
				t.Errorf("namespaced field %d set field %d to %v", i, j, *fields[j](&global))
			}
		}
		// Each field's error flag reaches only its own field.
		var global2 WorkloadWritePermissions
		var errs WorkloadWriteCapabilityErrors
		*errFields[i](&errs) = true
		MergeWorkloadWrites(&global2, WorkloadWritePermissions{}, errs, func(_, _, errored bool) bool { return errored })
		for j := range fields {
			if *fields[j](&global2) != (i == j) {
				t.Errorf("error flag %d set field %d to %v", i, j, *fields[j](&global2))
			}
		}
	}
}
