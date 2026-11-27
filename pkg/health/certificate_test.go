package health

import (
	"encoding/json"
	"os"
	"testing"
	"time"
)

func TestCertificateExpirySharedVectors(t *testing.T) {
	data, err := os.ReadFile("testdata/certificate_expiry.json")
	if err != nil {
		t.Fatal(err)
	}
	var vectors []struct {
		Name                        string
		WarningSeconds, HighSeconds int64
		RemainingNanoseconds        *int64
		Health                      Level
	}
	if err := json.Unmarshal(data, &vectors); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	for _, v := range vectors {
		t.Run(v.Name, func(t *testing.T) {
			p := CertificateExpiryThresholds{WarningWindow: time.Duration(v.WarningSeconds) * time.Second, HighWindow: time.Duration(v.HighSeconds) * time.Second}
			if v.WarningSeconds == 0 {
				p = CertificateExpiryPolicy{}.Resolve(nil, nil)
			}
			var expiry *time.Time
			if v.RemainingNanoseconds != nil {
				at := now.Add(time.Duration(*v.RemainingNanoseconds))
				expiry = &at
			}
			if got := p.Classify(expiry, now); got != v.Health {
				t.Fatalf("health=%s, want %s", got, v.Health)
			}
		})
	}
}

func TestCertificateExpiryAutomaticAndOverride(t *testing.T) {
	now := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name          string
		lifetime      time.Duration
		warning, high time.Duration
	}{
		{"one year", 365 * 24 * time.Hour, 30 * 24 * time.Hour, 7 * 24 * time.Hour},
		{"ninety days", 90 * 24 * time.Hour, 30 * 24 * time.Hour, 7 * 24 * time.Hour},
		{"one day", 24 * time.Hour, 8 * time.Hour, 112 * time.Minute},
		{"one hour", time.Hour, 20 * time.Minute, 280 * time.Second},
		{"fifteen minutes", 15 * time.Minute, 5 * time.Minute, 70 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			expiry := now.Add(tc.lifetime)
			p := CertificateExpiryPolicy{}.Resolve(&now, &expiry)
			if p.WarningWindow != tc.warning || p.HighWindow != tc.high || p.LiveWarningWindow > p.WarningWindow {
				t.Fatalf("thresholds=%+v", p)
			}
			if p.Classify(&expiry, now) != LevelHealthy {
				t.Fatal("new certificate already flagged")
			}
			if p.Classify(&expiry, expiry.Add(-p.WarningWindow)) != LevelHealthy {
				t.Fatal("warning boundary")
			}
			if p.Classify(&expiry, expiry.Add(-p.WarningWindow+time.Nanosecond)) != LevelDegraded {
				t.Fatal("warning boundary after")
			}
			if p.Classify(&expiry, expiry.Add(-p.HighWindow)) != LevelDegraded {
				t.Fatal("High boundary")
			}
			if p.Classify(&expiry, expiry.Add(-p.HighWindow+time.Nanosecond)) != LevelUnhealthy {
				t.Fatal("High boundary after")
			}
			if p.Classify(&expiry, expiry) != LevelUnhealthy {
				t.Fatal("exact expiry")
			}
		})
	}
	expiry := now.Add(time.Hour)
	for _, start := range []*time.Time{nil, &expiry} {
		p := CertificateExpiryPolicy{}.Resolve(start, &expiry)
		if p.WarningWindow != 30*24*time.Hour || p.HighWindow != 7*24*time.Hour {
			t.Fatal("unknown lifetime weakened warnings")
		}
	}
	p := CertificateExpiryPolicy{WarningPercent: 20}.Resolve(&now, &expiry)
	if p.WarningWindow != 12*time.Minute || p.HighWindow != 168*time.Second {
		t.Fatalf("one override=%+v", p)
	}
	if (CertificateExpiryPolicy{}).Windows().Automatic != true {
		t.Fatal("automatic metadata missing")
	}
	if err := (CertificateExpiryPolicy{WarningPercent: -1}).Validate(); err == nil {
		t.Fatal("negative duration accepted")
	}
}

func TestAutomaticCertificateSharedVectors(t *testing.T) {
	data, err := os.ReadFile("testdata/certificate_expiry_automatic.json")
	if err != nil {
		t.Fatal(err)
	}
	var vectors []struct {
		Name                                          string
		LifetimeSeconds                               *int64
		WarningPercent                                int
		WarningSeconds, HighSeconds, RemainingSeconds int64
		Health                                        Level
	}
	if err := json.Unmarshal(data, &vectors); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
	for _, v := range vectors {
		t.Run(v.Name, func(t *testing.T) {
			expiry := now.Add(time.Duration(v.RemainingSeconds) * time.Second)
			var start *time.Time
			if v.LifetimeSeconds != nil {
				at := expiry.Add(-time.Duration(*v.LifetimeSeconds) * time.Second)
				start = &at
			}
			p := CertificateExpiryPolicy{WarningPercent: v.WarningPercent}.Resolve(start, &expiry)
			if p.WarningWindow != time.Duration(v.WarningSeconds)*time.Second || p.HighWindow != time.Duration(v.HighSeconds)*time.Second {
				t.Fatalf("thresholds=%+v", p)
			}
			if got := p.Classify(&expiry, now); got != v.Health {
				t.Fatalf("health=%s want=%s", got, v.Health)
			}
		})
	}
}
