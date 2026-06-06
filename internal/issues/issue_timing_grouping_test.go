package issues

import (
	"testing"
	"time"
)

func TestFoldGroupIssueTimingMerge(t *testing.T) {
	base := Issue{
		Kind:      "Pod",
		Namespace: "default",
		Severity:  SeverityCritical,
		Source:    SourceProblem,
		Reason:    "CrashLoopBackOff",
		FirstSeen: time.Now().Add(-1 * time.Hour),
		LastSeen:  time.Now(),
		Count:     1,
	}
	withID := func(i Issue, name string) Issue {
		i.Name = name
		classifyIssue(&i)
		enrichIdentity(&i)
		return i
	}

	t.Run("all members started_at_resource_creation → grouped started_at_resource_creation", func(t *testing.T) {
		m1 := withID(base, "pod-1")
		m1.IssueTiming, m1.IssueTimingBasis = "started_at_resource_creation", "owner_condition"
		m2 := withID(base, "pod-2")
		m2.IssueTiming, m2.IssueTimingBasis = "started_at_resource_creation", "owner_condition"

		g := foldGroup([]Issue{m1, m2})
		if g.IssueTiming != "started_at_resource_creation" {
			t.Errorf("IssueTiming = %q, want started_at_resource_creation", g.IssueTiming)
		}
		if g.IssueTimingBasis != "owner_condition" {
			t.Errorf("IssueTimingBasis = %q, want owner_condition", g.IssueTimingBasis)
		}
	})

	t.Run("all members started_after_resource_was_healthy → grouped started_after_resource_was_healthy", func(t *testing.T) {
		m1 := withID(base, "pod-1")
		m1.IssueTiming, m1.IssueTimingBasis = "started_after_resource_was_healthy", "owner_condition"
		m2 := withID(base, "pod-2")
		m2.IssueTiming, m2.IssueTimingBasis = "started_after_resource_was_healthy", "owner_condition"

		g := foldGroup([]Issue{m1, m2})
		if g.IssueTiming != "started_after_resource_was_healthy" {
			t.Errorf("IssueTiming = %q, want started_after_resource_was_healthy", g.IssueTiming)
		}
	})

	t.Run("mixed started_at_resource_creation+started_after_resource_was_healthy → omit", func(t *testing.T) {
		m1 := withID(base, "pod-1")
		m1.IssueTiming = "started_at_resource_creation"
		m2 := withID(base, "pod-2")
		m2.IssueTiming = "started_after_resource_was_healthy"

		g := foldGroup([]Issue{m1, m2})
		if g.IssueTiming != "" {
			t.Errorf("IssueTiming = %q, want empty (omit on disagreement)", g.IssueTiming)
		}
		if g.IssueTimingBasis != "" {
			t.Errorf("IssueTimingBasis = %q, want empty", g.IssueTimingBasis)
		}
	})

	t.Run("members without issue_timing don't contribute", func(t *testing.T) {
		m1 := withID(base, "pod-1")
		m1.IssueTiming = "" // no signal
		m2 := withID(base, "pod-2")
		m2.IssueTiming = "started_after_resource_was_healthy"
		m2.IssueTimingBasis = "condition"

		g := foldGroup([]Issue{m1, m2})
		if g.IssueTiming != "started_after_resource_was_healthy" {
			t.Errorf("IssueTiming = %q, want started_after_resource_was_healthy (unknown member shouldn't block)", g.IssueTiming)
		}
	})

	t.Run("all members without issue_timing → omit", func(t *testing.T) {
		m1 := withID(base, "pod-1")
		m2 := withID(base, "pod-2")

		g := foldGroup([]Issue{m1, m2})
		if g.IssueTiming != "" {
			t.Errorf("IssueTiming = %q, want empty", g.IssueTiming)
		}
	})

	t.Run("single member with issue_timing → inherit", func(t *testing.T) {
		m1 := withID(base, "pod-1")
		m1.IssueTiming, m1.IssueTimingBasis = "started_at_resource_creation", "phase"

		g := foldGroup([]Issue{m1})
		if g.IssueTiming != "started_at_resource_creation" {
			t.Errorf("IssueTiming = %q, want started_at_resource_creation", g.IssueTiming)
		}
		if g.IssueTimingBasis != "phase" {
			t.Errorf("IssueTimingBasis = %q, want phase", g.IssueTimingBasis)
		}
	})

	t.Run("agreeing issue_timing with mixed bases → keep issue_timing, drop basis", func(t *testing.T) {
		m1 := withID(base, "pod-1")
		m1.IssueTiming, m1.IssueTimingBasis = "started_after_resource_was_healthy", "condition"
		m2 := withID(base, "pod-2")
		m2.IssueTiming, m2.IssueTimingBasis = "started_after_resource_was_healthy", "owner_condition"

		g := foldGroup([]Issue{m1, m2})
		if g.IssueTiming != "started_after_resource_was_healthy" {
			t.Errorf("IssueTiming = %q, want started_after_resource_was_healthy (bases differ but issue_timings agree)", g.IssueTiming)
		}
		if g.IssueTimingBasis != "" {
			t.Errorf("IssueTimingBasis = %q, want empty — one member's evidence must not be credited for the whole group", g.IssueTimingBasis)
		}
	})
}
