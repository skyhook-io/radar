package k8s

import (
	"testing"
	"time"
)

// withDeadlinesRestored snapshots the package-level deadlines, runs the test
// body, and restores them. Tests in the same package that mutate the globals
// must use this so test order doesn't leak state between cases.
func withDeadlinesRestored(t *testing.T, body func()) {
	t.Helper()
	snap := DeadlineOptions{
		ContextSwitchTimeout: ContextSwitchTimeout,
		FirstPaintBackstop:   FirstPaintBackstop,
		NamespaceListTimeout: NamespaceListTimeout,
		MaxScopeCandidates:   MaxScopeCandidates,
	}
	t.Cleanup(func() {
		ContextSwitchTimeout = snap.ContextSwitchTimeout
		FirstPaintBackstop = snap.FirstPaintBackstop
		NamespaceListTimeout = snap.NamespaceListTimeout
		MaxScopeCandidates = snap.MaxScopeCandidates
	})
	body()
}

// Defaults must match the documented upstream-compatible values so that a
// build without any flag/env override behaves bit-for-bit like before this
// PR. If a default changes, the PR scope changed too — fail loudly.
func TestDeadlineDefaults_PreserveUpstreamBehavior(t *testing.T) {
	if ContextSwitchTimeout != 30*time.Second {
		t.Errorf("ContextSwitchTimeout default = %v, want 30s", ContextSwitchTimeout)
	}
	if FirstPaintBackstop != 5*time.Minute {
		t.Errorf("FirstPaintBackstop default = %v, want 5m", FirstPaintBackstop)
	}
	if NamespaceListTimeout != 5*time.Second {
		t.Errorf("NamespaceListTimeout default = %v, want 5s", NamespaceListTimeout)
	}
	if MaxScopeCandidates != 20 {
		t.Errorf("MaxScopeCandidates default = %d, want 20", MaxScopeCandidates)
	}
}

func TestConfigureDeadlines_AppliesPositiveOverrides(t *testing.T) {
	withDeadlinesRestored(t, func() {
		ConfigureDeadlines(DeadlineOptions{
			ContextSwitchTimeout: 120 * time.Second,
			FirstPaintBackstop:   10 * time.Minute,
			NamespaceListTimeout: 30 * time.Second,
			MaxScopeCandidates:   200,
		})
		if ContextSwitchTimeout != 120*time.Second {
			t.Errorf("ContextSwitchTimeout = %v, want 120s", ContextSwitchTimeout)
		}
		if FirstPaintBackstop != 10*time.Minute {
			t.Errorf("FirstPaintBackstop = %v, want 10m", FirstPaintBackstop)
		}
		if NamespaceListTimeout != 30*time.Second {
			t.Errorf("NamespaceListTimeout = %v, want 30s", NamespaceListTimeout)
		}
		if MaxScopeCandidates != 200 {
			t.Errorf("MaxScopeCandidates = %d, want 200", MaxScopeCandidates)
		}
	})
}

// Zero in an opt field means "the caller didn't override it" — never reset
// the live value to the type zero. Same semantics every other tunable in
// the explorer uses.
func TestConfigureDeadlines_ZeroLeavesDefault(t *testing.T) {
	withDeadlinesRestored(t, func() {
		ConfigureDeadlines(DeadlineOptions{})
		if ContextSwitchTimeout != 30*time.Second {
			t.Errorf("ContextSwitchTimeout mutated by zero opt: got %v", ContextSwitchTimeout)
		}
		if FirstPaintBackstop != 5*time.Minute {
			t.Errorf("FirstPaintBackstop mutated by zero opt: got %v", FirstPaintBackstop)
		}
		if NamespaceListTimeout != 5*time.Second {
			t.Errorf("NamespaceListTimeout mutated by zero opt: got %v", NamespaceListTimeout)
		}
		if MaxScopeCandidates != 20 {
			t.Errorf("MaxScopeCandidates mutated by zero opt: got %d", MaxScopeCandidates)
		}
	})
}

// Negative values are bogus (a deployment-manifest typo, almost certainly)
// and the safe behavior is to drop them and keep the default. The setter
// logs the rejection so the typo is visible in startup logs.
func TestConfigureDeadlines_NegativeRejected(t *testing.T) {
	withDeadlinesRestored(t, func() {
		ConfigureDeadlines(DeadlineOptions{
			ContextSwitchTimeout: -1 * time.Second,
			FirstPaintBackstop:   -1 * time.Minute,
			NamespaceListTimeout: -1 * time.Second,
			MaxScopeCandidates:   -1,
		})
		if ContextSwitchTimeout != 30*time.Second {
			t.Errorf("negative ContextSwitchTimeout was applied: %v", ContextSwitchTimeout)
		}
		if FirstPaintBackstop != 5*time.Minute {
			t.Errorf("negative FirstPaintBackstop was applied: %v", FirstPaintBackstop)
		}
		if NamespaceListTimeout != 5*time.Second {
			t.Errorf("negative NamespaceListTimeout was applied: %v", NamespaceListTimeout)
		}
		if MaxScopeCandidates != 20 {
			t.Errorf("negative MaxScopeCandidates was applied: %d", MaxScopeCandidates)
		}
	})
}

func TestEnvDurationOr_UnsetReturnsFallback(t *testing.T) {
	t.Setenv("RADAR_TEST_EMPTY", "")
	got := EnvDurationOr("RADAR_TEST_EMPTY", 7*time.Second)
	if got != 7*time.Second {
		t.Errorf("unset env should return fallback, got %v", got)
	}
}

func TestEnvDurationOr_ParsesValid(t *testing.T) {
	t.Setenv("RADAR_TEST_DUR", "45s")
	got := EnvDurationOr("RADAR_TEST_DUR", 5*time.Second)
	if got != 45*time.Second {
		t.Errorf("valid env should be parsed, got %v", got)
	}
}

func TestEnvDurationOr_InvalidFallsBack(t *testing.T) {
	t.Setenv("RADAR_TEST_DUR_BAD", "not-a-duration")
	got := EnvDurationOr("RADAR_TEST_DUR_BAD", 12*time.Second)
	if got != 12*time.Second {
		t.Errorf("invalid env should fall back to default, got %v", got)
	}
}

func TestEnvIntOr_UnsetReturnsFallback(t *testing.T) {
	t.Setenv("RADAR_TEST_EMPTY_INT", "")
	got := EnvIntOr("RADAR_TEST_EMPTY_INT", 42)
	if got != 42 {
		t.Errorf("unset env should return fallback, got %d", got)
	}
}

func TestEnvIntOr_ParsesValid(t *testing.T) {
	t.Setenv("RADAR_TEST_INT", "200")
	got := EnvIntOr("RADAR_TEST_INT", 20)
	if got != 200 {
		t.Errorf("valid env should be parsed, got %d", got)
	}
}

func TestEnvIntOr_InvalidFallsBack(t *testing.T) {
	t.Setenv("RADAR_TEST_INT_BAD", "twenty")
	got := EnvIntOr("RADAR_TEST_INT_BAD", 20)
	if got != 20 {
		t.Errorf("invalid env should fall back to default, got %d", got)
	}
}

// The precedence rule documented in the README is: flag (when explicitly set)
// > env > default. We can't drive flag.Parse() from here, but we can verify
// the building block: when main.go computes the flag's *default* via
// EnvDurationOr(envVar, hardcodedDefault) and an env var is set, the env
// value flows into the flag's default. The flag.Parse path then either
// keeps that or overrides — both behaviors are stdlib flag, not our code.
func TestEnvPrecedence_EnvOverridesHardcodedDefault(t *testing.T) {
	t.Setenv("RADAR_PREC_DUR", "99s")
	t.Setenv("RADAR_PREC_INT", "999")
	if got := EnvDurationOr("RADAR_PREC_DUR", 1*time.Second); got != 99*time.Second {
		t.Errorf("env should win over hardcoded default, got %v", got)
	}
	if got := EnvIntOr("RADAR_PREC_INT", 1); got != 999 {
		t.Errorf("env should win over hardcoded default, got %d", got)
	}
}
