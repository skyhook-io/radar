package issues

import (
	"testing"
	"time"
)

func TestFoldGroupOnsetMerge(t *testing.T) {
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

	t.Run("all members initial → grouped initial", func(t *testing.T) {
		m1 := withID(base, "pod-1")
		m1.Onset, m1.OnsetBasis = "initial", "owner_condition"
		m2 := withID(base, "pod-2")
		m2.Onset, m2.OnsetBasis = "initial", "owner_condition"

		g := foldGroup([]Issue{m1, m2})
		if g.Onset != "initial" {
			t.Errorf("Onset = %q, want initial", g.Onset)
		}
		if g.OnsetBasis != "owner_condition" {
			t.Errorf("OnsetBasis = %q, want owner_condition", g.OnsetBasis)
		}
	})

	t.Run("all members runtime → grouped runtime", func(t *testing.T) {
		m1 := withID(base, "pod-1")
		m1.Onset, m1.OnsetBasis = "runtime", "owner_condition"
		m2 := withID(base, "pod-2")
		m2.Onset, m2.OnsetBasis = "runtime", "owner_condition"

		g := foldGroup([]Issue{m1, m2})
		if g.Onset != "runtime" {
			t.Errorf("Onset = %q, want runtime", g.Onset)
		}
	})

	t.Run("mixed initial+runtime → omit", func(t *testing.T) {
		m1 := withID(base, "pod-1")
		m1.Onset = "initial"
		m2 := withID(base, "pod-2")
		m2.Onset = "runtime"

		g := foldGroup([]Issue{m1, m2})
		if g.Onset != "" {
			t.Errorf("Onset = %q, want empty (omit on disagreement)", g.Onset)
		}
		if g.OnsetBasis != "" {
			t.Errorf("OnsetBasis = %q, want empty", g.OnsetBasis)
		}
	})

	t.Run("members without onset don't contribute", func(t *testing.T) {
		m1 := withID(base, "pod-1")
		m1.Onset = "" // no signal
		m2 := withID(base, "pod-2")
		m2.Onset = "runtime"
		m2.OnsetBasis = "condition"

		g := foldGroup([]Issue{m1, m2})
		if g.Onset != "runtime" {
			t.Errorf("Onset = %q, want runtime (unknown member shouldn't block)", g.Onset)
		}
	})

	t.Run("all members without onset → omit", func(t *testing.T) {
		m1 := withID(base, "pod-1")
		m2 := withID(base, "pod-2")

		g := foldGroup([]Issue{m1, m2})
		if g.Onset != "" {
			t.Errorf("Onset = %q, want empty", g.Onset)
		}
	})

	t.Run("single member with onset → inherit", func(t *testing.T) {
		m1 := withID(base, "pod-1")
		m1.Onset, m1.OnsetBasis = "initial", "phase"

		g := foldGroup([]Issue{m1})
		if g.Onset != "initial" {
			t.Errorf("Onset = %q, want initial", g.Onset)
		}
		if g.OnsetBasis != "phase" {
			t.Errorf("OnsetBasis = %q, want phase", g.OnsetBasis)
		}
	})

	t.Run("agreeing onset with mixed bases → keep onset, drop basis", func(t *testing.T) {
		m1 := withID(base, "pod-1")
		m1.Onset, m1.OnsetBasis = "runtime", "condition"
		m2 := withID(base, "pod-2")
		m2.Onset, m2.OnsetBasis = "runtime", "owner_condition"

		g := foldGroup([]Issue{m1, m2})
		if g.Onset != "runtime" {
			t.Errorf("Onset = %q, want runtime (bases differ but onsets agree)", g.Onset)
		}
		if g.OnsetBasis != "" {
			t.Errorf("OnsetBasis = %q, want empty — one member's evidence must not be credited for the whole group", g.OnsetBasis)
		}
	})
}
