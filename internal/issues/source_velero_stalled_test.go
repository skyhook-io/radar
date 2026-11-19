package issues

import (
	"strings"
	"testing"
	"time"

	"github.com/skyhook-io/radar/pkg/issuesapi"
)

// A phase means "in flight" only while something is advancing it. Velero moves
// these phases from its own controller, so once a run has sat in one for longer
// than Velero allows a single operation, "in progress" and "stopped" are the
// same picture — and every screen renders it as work under way.
//
// The demo cluster is exactly that shape: six backups mid-phase with the
// controller scaled to zero, one of them In progress for over a day, and nothing
// anywhere saying so.
//
// The supersession grouping cannot cover this: it considers only DECIDED runs,
// so a backup that never reaches a verdict is invisible to it by design.

func TestVeleroStalledRun(t *testing.T) {
	const overBudget = 5 * 60 // minutes; the default budget is 4h
	const wellUnder = 30

	t.Run("reports a run stuck past the time Velero allows", func(t *testing.T) {
		got := detectBackups(veleroObj{
			name: "phase-inprogress", phase: "InProgress",
			startedMinsAgo: overBudget, createdMinsAgo: overBudget,
		})
		if len(got) != 1 {
			t.Fatalf("issues = %d, want 1 — a backup in flight for 5h is past the 4h budget", len(got))
		}
		if got[0].Reason != ReasonVeleroRunStalled {
			t.Errorf("reason = %q, want %q", got[0].Reason, ReasonVeleroRunStalled)
		}
		if got[0].Severity != SeverityWarning {
			t.Errorf("severity = %v, want warning", got[0].Severity)
		}
		// The message has to carry both numbers, or it is an assertion without
		// its evidence.
		if !strings.Contains(got[0].Message, "5h") || !strings.Contains(got[0].Message, "4h") {
			t.Errorf("message = %q, want the elapsed time and the budget", got[0].Message)
		}
	})

	// A backup legitimately running for twenty minutes is not news, and a note on
	// every in-flight run would be noise on every healthy cluster.
	t.Run("stays quiet while a run is still within budget", func(t *testing.T) {
		if got := detectBackups(veleroObj{
			name: "fresh", phase: "InProgress", startedMinsAgo: wellUnder, createdMinsAgo: wellUnder,
		}); len(got) != 0 {
			t.Errorf("issues = %d, want 0 for a run 30m in: %+v", len(got), got)
		}
	})

	// Every phase Velero advances from its controller, and ONLY those. Asserting
	// presence alone is what let a second, contradicting issue through on the
	// partially-failed phases.
	t.Run("covers the phases a controller has to move, and only those", func(t *testing.T) {
		for _, phase := range []string{"WaitingForPluginOperations", "Finalizing"} {
			got := detectBackups(veleroObj{
				name: "x", phase: phase, startedMinsAgo: overBudget, createdMinsAgo: overBudget,
			})
			if len(got) != 1 || got[0].Reason != ReasonVeleroRunStalled {
				t.Errorf("phase %s stuck past budget gave %v, want exactly one stall issue", phase, reasonsOf(got))
			}
		}
	})

	// These already carry an outcome — the partial failure is a fact, even though
	// Velero is still finalizing. veleroPhasesWithOutcome reports them as
	// partially failed, and counting them as in flight too put two issues on one
	// object saying opposite things.
	//
	// The cost is a real gap, and it is on the record rather than hidden: a run
	// wedged in one of these forever is never reported as not progressing.
	t.Run("does not also call a partially-failed run stalled", func(t *testing.T) {
		for _, phase := range []string{"FinalizingPartiallyFailed", "WaitingForPluginOperationsPartiallyFailed"} {
			got := detectBackups(veleroObj{
				name: "p", phase: phase, startedMinsAgo: overBudget, createdMinsAgo: overBudget,
			})
			if len(got) != 1 {
				t.Errorf("phase %s gave %v, want exactly one issue", phase, reasonsOf(got))
			}
			for _, iss := range got {
				if iss.Reason == ReasonVeleroRunStalled {
					t.Errorf("phase %s reported as stalled as well as decided", phase)
				}
			}
		}
	})

	// Velero only deletes backups that already ran, and startTimestamp still holds
	// when that original run began — it is never rewritten. Timing a deletion
	// against it announces that a deletion which started seconds ago has been
	// stuck for as long as the backup has existed.
	t.Run("does not time a deletion against the original run", func(t *testing.T) {
		got := detectBackups(veleroObj{
			name: "d", phase: "Deleting",
			startedMinsAgo: 14 * 24 * 60, createdMinsAgo: 14 * 24 * 60,
		})
		for _, iss := range got {
			if iss.Reason == ReasonVeleroRunStalled {
				t.Error("a deletion of a two-week-old backup reported as stalled on the backup's own age")
			}
		}
	})

	// A run that reached a terminal phase is not in flight however old it is —
	// the supersession logic owns those, and reporting them here would double up.
	t.Run("says nothing about a run that already finished", func(t *testing.T) {
		for _, phase := range []string{"Completed", "Failed", "FailedValidation"} {
			for _, iss := range detectBackups(veleroObj{
				name: "old", phase: phase, startedMinsAgo: 10 * 24 * 60, createdMinsAgo: 10 * 24 * 60,
			}) {
				if iss.Reason == ReasonVeleroRunStalled {
					t.Errorf("phase %s reported as stalled", phase)
				}
			}
		}
	})

	// Velero stamps startTimestamp only once it picks a backup up. Anchoring on
	// creationTimestamp instead would call a backup stalled while it is still
	// sitting in the queue, which is a different problem with a different fix.
	t.Run("does not start the clock before Velero picked the run up", func(t *testing.T) {
		if got := detectBackups(veleroObj{
			name: "queued", phase: "InProgress", createdMinsAgo: overBudget,
		}); len(got) != 0 {
			t.Errorf("issues = %d, want 0 for a run with no startTimestamp: %+v", len(got), got)
		}
	})
}

// The docs say Radar reports a Backup OR Restore still in flight. Restores went
// down a different branch that never reached the check, so the claim was true of
// half the kinds it named — and a stuck restore is the more urgent of the two,
// because someone is waiting on it to get their data back.
func TestVeleroStalledRestore(t *testing.T) {
	items := buildVelero("Restore", veleroObj{
		name: "r-inprogress", phase: "InProgress", startedMinsAgo: 5 * 60, createdMinsAgo: 5 * 60,
	})
	got := detectVeleroIssues(veleroGVR("restores"), "Restore", items, nil)

	var found bool
	for _, iss := range got {
		if iss.Reason == ReasonVeleroRunStalled {
			found = true
		}
	}
	if !found {
		t.Errorf("a restore in flight for 5h raised %v, want a stall issue", reasonsOf(got))
	}
}

// The category drives the title the Issues page and the resource drawer print
// above the message. Filed under backup_failed, a stalled run rendered as
// "Backup failed" directly above its own message saying the run was still in
// progress, and directly above a phase badge reading "In progress" — three
// statements on one card, one of them false. It also sends the reader to the
// wrong place: a failed run has an error to read, a stalled one has a
// controller to check.
//
// The category is what the Issues page and the drawer print above the message,
// so it has to be checked here — a test that only asserts the reason never sees
// the word an operator actually reads.
func TestVeleroStalledCategory(t *testing.T) {
	stalled := detectBackups(veleroObj{
		name: "stuck", phase: "InProgress",
		startedMinsAgo: 5 * 60, createdMinsAgo: 5 * 60,
	})
	if len(stalled) != 1 {
		t.Fatalf("issues = %d, want 1", len(stalled))
	}
	if stalled[0].Category != issuesapi.CategoryBackupStalled {
		t.Errorf("category = %q, want %q — a run with no verdict is not a failed run",
			stalled[0].Category, issuesapi.CategoryBackupStalled)
	}

	// A restore that stops moving is the same claim about a different kind, and
	// restores take a separate path through the detector — covering only backups
	// leaves half the docs' promise untested.
	restores := detectVeleroIssues(veleroGVR("restores"), "Restore", buildVelero("Restore", veleroObj{
		name: "r", phase: "InProgress", startedMinsAgo: 5 * 60, createdMinsAgo: 5 * 60,
	}), nil)
	for _, iss := range restores {
		if iss.Reason == ReasonVeleroRunStalled && iss.Category != issuesapi.CategoryBackupStalled {
			t.Errorf("restore category = %q, want %q", iss.Category, issuesapi.CategoryBackupStalled)
		}
	}

	// The split must not swallow the failures it was carved out of.
	failed := detectBackups(veleroObj{
		name: "bad", phase: "Failed", startedMinsAgo: 60, createdMinsAgo: 60,
	})
	if len(failed) != 1 || failed[0].Category != issuesapi.CategoryBackupFailed {
		t.Errorf("a Failed backup got %v, want one issue categorised %q",
			failed, issuesapi.CategoryBackupFailed)
	}
}

// Where the budget came from changes what may be said about it.
//
// A backup that declares itemOperationTimeout gives a fact about itself. A
// backup that declares none is measured against Velero's built-in four hours —
// and that is an assumption, because the controller takes
// --default-item-operation-timeout and distributions ship their own. Saying
// "past the 4h Velero allows" on such a run states an install setting this
// source cannot read.
func TestVeleroStalledBudgetAttribution(t *testing.T) {
	// itemOperationTimeout bounds asynchronous BackupItemAction operations —
	// Velero's own words — so only the phase spent waiting on those may be
	// described as past a limit Velero set.
	t.Run("a declared budget is described as the run's own", func(t *testing.T) {
		got := detectBackups(veleroObj{
			name: "declared", phase: "WaitingForPluginOperations",
			startedMinsAgo: 120, createdMinsAgo: 120,
			itemOperationTimeout: "1m0s",
		})
		if len(got) != 1 {
			t.Fatalf("issues = %d, want 1", len(got))
		}
		if !strings.Contains(got[0].Message, "this run allows for a single operation") {
			t.Errorf("message = %q, want it attributed to the run itself", got[0].Message)
		}
	})

	t.Run("an assumed budget is not stated as what Velero allows here", func(t *testing.T) {
		got := detectBackups(veleroObj{
			name: "assumed", phase: "WaitingForPluginOperations",
			startedMinsAgo: 5 * 60, createdMinsAgo: 5 * 60,
		})
		if len(got) != 1 {
			t.Fatalf("issues = %d, want 1", len(got))
		}
		msg := got[0].Message
		if !strings.Contains(msg, "Velero allows") || !strings.Contains(msg, "unless an install changes it") {
			t.Errorf("message = %q, want the number attributed to Velero's built-in and marked as changeable", msg)
		}
	})
}

// The phase Velero does NOT bound. A backup walking and writing items may
// legitimately run for hours on a large cluster — Velero publishes no limit on
// it — so calling elapsed time a breach would cite a rule that does not exist to
// the one reader equipped to notice.
func TestVeleroStalledDoesNotInventALimitForInProgress(t *testing.T) {
	got := detectBackups(veleroObj{
		name: "big", phase: "InProgress",
		startedMinsAgo: 5 * 60, createdMinsAgo: 5 * 60,
	})
	if len(got) != 1 {
		t.Fatalf("issues = %d, want 1 — a run this long is still worth surfacing", len(got))
	}
	msg := got[0].Message
	if !strings.Contains(msg, "Velero sets no limit on this phase") {
		t.Errorf("message = %q, want it to say the limit is not Velero's", msg)
	}
	// Asserted as an absence, not by repeating the check above. A message that
	// carried both sentences — the yardstick clause appended to the governed
	// one — would satisfy a positive check while still claiming Velero bounds
	// this phase.
	for _, governed := range []string{"past the", "allows for a single operation,"} {
		if strings.Contains(msg, governed) {
			t.Errorf("message = %q applies the operation budget to a phase Velero does not govern", msg)
		}
	}
	// The elapsed time is still the evidence, and still stated.
	if !strings.Contains(msg, "nothing appears to be advancing it") {
		t.Errorf("message = %q, want it to still report the run as not advancing", msg)
	}
}

func TestVeleroStalledWordingFitsRestores(t *testing.T) {
	// A Restore carries its own itemOperationTimeout, and the message must never
	// call a restore a backup. Both phase families, both budget sources.
	for _, tc := range []struct {
		name    string
		phase   string
		timeout string
		want    string
	}{
		{"operation phase, declared budget", "WaitingForPluginOperations", "1m0s", "this run allows for a single operation"},
		{"operation phase, built-in budget", "WaitingForPluginOperations", "", "unless an install changes it"},
		{"unbounded phase", "InProgress", "1m0s", "Velero sets no limit on this phase"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			items := buildVelero("Restore", veleroObj{
				name: "r", phase: tc.phase,
				startedMinsAgo: 5 * 60, createdMinsAgo: 5 * 60,
				itemOperationTimeout: tc.timeout,
			})
			var seen bool
			for _, iss := range detectVeleroIssues(veleroGVR("restores"), "Restore", items, nil) {
				if iss.Reason != ReasonVeleroRunStalled {
					continue
				}
				seen = true
				if strings.Contains(iss.Message, "backup") {
					t.Errorf("restore message = %q, calls the restore a backup", iss.Message)
				}
				if !strings.Contains(iss.Message, tc.want) {
					t.Errorf("restore message = %q, want %q", iss.Message, tc.want)
				}
			}
			if !seen {
				t.Fatal("no stall issue raised for a restore 5h in flight")
			}
		})
	}
}

// A partially-failed run that is STILL RUNNING can wedge, and that was the one
// stalled shape nothing reported: excluded from the stall check to avoid two
// issues on one object, and never mentioned in the issue it does raise. So a
// backup could sit in WaitingForPluginOperationsPartiallyFailed for a week
// reading "Partially failed", which sounds finished.
func TestVeleroPartialRunThatIsAlsoStalled(t *testing.T) {
	inFlightPartials := []string{"FinalizingPartiallyFailed", "WaitingForPluginOperationsPartiallyFailed"}

	for _, phase := range inFlightPartials {
		t.Run(phase+" wedged past its budget", func(t *testing.T) {
			got := detectBackups(veleroObj{
				name: "wedged", phase: phase,
				startedMinsAgo: 5 * 60, createdMinsAgo: 5 * 60,
			})
			// Still exactly one issue — the fact is added, not duplicated.
			if len(got) != 1 {
				t.Fatalf("issues = %v, want exactly one", reasonsOf(got))
			}
			if !strings.Contains(got[0].Message, "nothing appearing to advance it") {
				t.Errorf("message = %q, want it to say the run is not moving", got[0].Message)
			}
			if !strings.Contains(got[0].Message, "some items were not backed up") {
				t.Errorf("message = %q, want it to keep the partial failure", got[0].Message)
			}
		})

		t.Run(phase+" within its budget says nothing about stalling", func(t *testing.T) {
			got := detectBackups(veleroObj{
				name: "fresh", phase: phase,
				startedMinsAgo: 5, createdMinsAgo: 5,
			})
			if len(got) != 1 {
				t.Fatalf("issues = %v, want exactly one", reasonsOf(got))
			}
			if strings.Contains(got[0].Message, "nothing appearing to advance it") {
				t.Errorf("message = %q claims a five-minute-old run is stuck", got[0].Message)
			}
		})
	}

	// Velero stamps startTimestamp only once it picks a run up. Without the
	// guard, time.Since(zero) is about fifty-five years, and the message would
	// announce that a run nothing has started yet has not advanced since 1970.
	t.Run("a partial run Velero has not started yet is not called stuck", func(t *testing.T) {
		got := detectBackups(veleroObj{
			name: "queued", phase: "WaitingForPluginOperationsPartiallyFailed",
			createdMinsAgo: 5 * 60, // no startTimestamp
		})
		if len(got) != 1 {
			t.Fatalf("issues = %v, want exactly one", reasonsOf(got))
		}
		if strings.Contains(got[0].Message, "nothing appearing to advance it") {
			t.Errorf("message = %q times a run that was never started", got[0].Message)
		}
	})

	// Plain PartiallyFailed is terminal. It has stopped because it finished, not
	// because something is wrong, and timing it would report every old one.
	t.Run("a terminal PartiallyFailed is never called stalled", func(t *testing.T) {
		got := detectBackups(veleroObj{
			name: "done", phase: "PartiallyFailed",
			startedMinsAgo: 30 * 24 * 60, createdMinsAgo: 30 * 24 * 60,
		})
		if len(got) != 1 {
			t.Fatalf("issues = %v, want exactly one", reasonsOf(got))
		}
		if strings.Contains(got[0].Message, "nothing appearing to advance it") {
			t.Errorf("message = %q calls a finished run stalled", got[0].Message)
		}
	})
}

// The Issues page renders `since` as how long the problem has been true. A run
// is not stalled from the moment it starts — it becomes stalled when it passes
// its budget, and dating the problem to the run's start makes a one-hour problem
// look like a five-hour one on a run with a four-hour budget.
func TestVeleroStalledDatesTheProblemNotTheRun(t *testing.T) {
	const runMins = 5 * 60 // 5h in flight, against the 4h built-in
	got := detectBackups(veleroObj{
		name: "stuck", phase: "WaitingForPluginOperations",
		startedMinsAgo: runMins, createdMinsAgo: runMins,
	})
	if len(got) != 1 {
		t.Fatalf("issues = %d, want 1", len(got))
	}
	problemAge := time.Since(got[0].FirstSeen)
	if problemAge > 90*time.Minute {
		t.Errorf("FirstSeen dates the problem %s back; the run passed its 4h budget about 1h ago",
			problemAge.Round(time.Minute))
	}
	if problemAge < 30*time.Minute {
		t.Errorf("FirstSeen dates the problem only %s back; it has been past budget about 1h",
			problemAge.Round(time.Minute))
	}
}

// Both numbers in a stall message run through the same formatter, and Velero
// accepts a sub-minute itemOperationTimeout — so a formatter that bottoms out at
// minutes turns the whole claim into "for 0m — past the 0m", which reads as a
// bug in Radar rather than a stalled backup.
func TestVeleroDurationWordingCoversEveryScale(t *testing.T) {
	for _, tc := range []struct {
		d    time.Duration
		want string
	}{
		{40 * time.Second, "40s"},
		{90 * time.Second, "1m 30s"},
		{59 * time.Minute, "59m"},
		{90 * time.Minute, "1h 30m"},
		{25 * time.Hour, "1d 1h"},
		{48 * time.Hour, "2d"},
		// The default configuration: a run past Velero's built-in four hours.
		// Truncating both to "4h" would make the message compare a number with
		// itself.
		{4*time.Hour + 30*time.Minute, "4h 30m"},
	} {
		if got := formatVeleroDuration(tc.d); got != tc.want {
			t.Errorf("formatVeleroDuration(%s) = %q, want %q", tc.d, got, tc.want)
		}
	}

	// Elapsed and budget must stay distinguishable in the sentence itself —
	// that difference is the whole claim.
	def := veleroStalledSentence("WaitingForPluginOperations", 4*time.Hour+30*time.Minute, 4*time.Hour, false)
	if !strings.Contains(def, "4h 30m") || !strings.Contains(def, "the 4h Velero allows") {
		t.Errorf("message = %q, want the elapsed time to read as longer than the limit", def)
	}

	// The sentence a sub-minute budget actually produces.
	msg := veleroStalledSentence("WaitingForPluginOperations", 40*time.Second, 30*time.Second, true)
	if strings.Contains(msg, "0m") {
		t.Errorf("message = %q, want real durations rather than a rounded-down zero", msg)
	}
	if !strings.Contains(msg, "40s") || !strings.Contains(msg, "30s") {
		t.Errorf("message = %q, want both the elapsed time and the budget", msg)
	}
}

// The boundary both stall paths share. It was previously written inline at each
// call site against a time.Since result, which is why it went untested: a test
// cannot construct "exactly the budget" out of wall-clock arithmetic. As a
// predicate over two durations it is ordinary to check.
func TestVeleroPastBudgetIsPastNotAt(t *testing.T) {
	const budget = 4 * time.Hour
	for _, c := range []struct {
		name string
		age  time.Duration
		want bool
	}{
		{"well inside", 30 * time.Minute, false},
		// A run that has used exactly the time it was allowed has not outlived
		// it. Velero is entitled to the whole budget.
		{"exactly the budget", budget, false},
		{"a nanosecond past", budget + 1, true},
		{"well past", 9 * time.Hour, true},
	} {
		if got := veleroPastBudget(c.age, budget); got != c.want {
			t.Errorf("%s: veleroPastBudget(%s, %s) = %v, want %v", c.name, c.age, budget, got, c.want)
		}
	}

	// A zero budget would report every in-flight run as stalled the instant it
	// started. veleroInFlightBudget is what keeps one from reaching here.
	if veleroPastBudget(0, 0) {
		t.Error("a zero age against a zero budget must not read as stalled")
	}
	// The guard that keeps a zero budget from ever reaching the comparison.
	if got, _ := veleroInFlightBudget(veleroObj{itemOperationTimeout: "0s"}.build("Backup")); got <= 0 {
		t.Errorf("veleroInFlightBudget = %s, want the built-in rather than zero", got)
	}
}
