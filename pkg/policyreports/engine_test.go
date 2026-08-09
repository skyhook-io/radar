package policyreports

import "testing"

func TestEngineForSource(t *testing.T) {
	tests := []struct {
		name   string
		source string
		want   Engine
	}{
		// Every one of these three was observed in the results[] of a
		// single Kyverno 1.18.2 cluster — the reason raw-source filtering
		// is not an option.
		{"legacy kyverno", "kyverno", EngineKyverno},
		{"modern validating", "KyvernoValidatingPolicy", EngineKyverno},
		{"modern generating", "KyvernoGeneratingPolicy", EngineKyverno},

		{"modern image validating", "KyvernoImageValidatingPolicy", EngineKyverno},
		{"modern mutating", "KyvernoMutatingPolicy", EngineKyverno},
		{"unreleased kyverno policy type still attributes", "KyvernoSomeFuturePolicy", EngineKyverno},

		{"vap", "ValidatingAdmissionPolicy", EngineVAP},
		{"map", "MutatingAdmissionPolicy", EngineVAP},

		{"trivy vulnerability", "Trivy Vulnerability", EngineTrivy},
		{"trivy operator", "trivy-operator", EngineTrivy},

		{"falco", "falco", EngineFalco},
		{"falcosidekick", "falcosidekick", EngineFalco},

		{"case insensitive", "KYVERNO", EngineKyverno},
		{"whitespace trimmed", "  kyverno  ", EngineKyverno},

		{"empty is unknown not other", "", EngineUnknown},
		{"whitespace only is unknown", "   ", EngineUnknown},
		{"unrecognized producer is other", "some-vendor-scanner", EngineOther},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := EngineForSource(tt.source); got != tt.want {
				t.Errorf("EngineForSource(%q) = %q, want %q", tt.source, got, tt.want)
			}
		})
	}
}

// A Kyverno-owned policy type whose name also ends in "AdmissionPolicy"
// must attribute to Kyverno, not VAP — the prefix rule has to win. Pinned
// separately because the two rules are order-dependent and a reorder would
// otherwise pass every other case.
func TestEngineForSourceKyvernoPrefixBeatsAdmissionPolicySuffix(t *testing.T) {
	if got := EngineForSource("KyvernoValidatingAdmissionPolicy"); got != EngineKyverno {
		t.Errorf("got %q, want %q", got, EngineKyverno)
	}
}

func TestFindingEngineDerivesFromSource(t *testing.T) {
	f := Finding{Source: "KyvernoValidatingPolicy"}
	if got := f.Engine(); got != EngineKyverno {
		t.Errorf("Finding.Engine() = %q, want %q", got, EngineKyverno)
	}

	// A finding from a report that never set source must not masquerade as
	// an attributable one.
	if got := (Finding{}).Engine(); got != EngineUnknown {
		t.Errorf("zero Finding.Engine() = %q, want %q", got, EngineUnknown)
	}
}
