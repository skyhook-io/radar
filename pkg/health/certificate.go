package health

import (
	"fmt"
	"strconv"
	"time"
)

type CertificateExpiryPolicy struct {
	WarningPercent int
}

type CertificateExpiryWindows struct {
	WarningSeconds     float64 `json:"warningSeconds"`
	HighSeconds        float64 `json:"highSeconds"`
	LiveWarningSeconds float64 `json:"liveWarningSeconds"`
	Automatic          bool    `json:"automatic,omitempty"`
	WarningPercent     int     `json:"warningPercent,omitempty"`
}

type CertificateExpiryThresholds struct {
	WarningWindow     time.Duration
	HighWindow        time.Duration
	LiveWarningWindow time.Duration
}

func (p CertificateExpiryPolicy) Validate() error {
	if p.WarningPercent < 0 || p.WarningPercent > 100 {
		return fmt.Errorf("certificate warning percent must be a whole number from 1 to 100, or auto")
	}
	return nil
}

// Resolve uses actual validity, never the age of a Secret or a requested duration.
// Unknown or invalid validity retains the conservative 30-day warning.
func (p CertificateExpiryPolicy) Resolve(notBefore, notAfter *time.Time) CertificateExpiryThresholds {
	warning := 30 * 24 * time.Hour
	if notBefore != nil && notAfter != nil && !notBefore.IsZero() && notAfter.After(*notBefore) {
		lifetime := notAfter.Sub(*notBefore)
		portion := lifetime / 3
		if p.WarningPercent != 0 {
			percent := time.Duration(p.WarningPercent)
			// Divide first so long validity periods cannot overflow during scaling.
			portion = lifetime/100*percent + lifetime%100*percent/100
		}
		warning = min(warning, portion)
	}
	high := warning * 7 / 30
	return CertificateExpiryThresholds{WarningWindow: warning, HighWindow: high, LiveWarningWindow: min(24*time.Hour, warning)}
}

// Windows describes the global policy. Per-certificate metadata uses Resolve.Windows.
func (p CertificateExpiryPolicy) Windows() CertificateExpiryWindows {
	windows := p.Resolve(nil, nil).Windows()
	windows.Automatic = true
	windows.WarningPercent = p.WarningPercent
	return windows
}

func (p CertificateExpiryPolicy) Setting() string {
	if p.WarningPercent == 0 {
		return "auto"
	}
	return strconv.Itoa(p.WarningPercent)
}

func (p CertificateExpiryThresholds) Classify(expiry *time.Time, now time.Time) Level {
	if expiry == nil {
		return LevelUnknown
	}
	remaining := expiry.Sub(now)
	switch {
	case remaining <= 0 || remaining < p.HighWindow:
		return LevelUnhealthy
	case remaining < p.WarningWindow:
		return LevelDegraded
	default:
		return LevelHealthy
	}
}

func (p CertificateExpiryThresholds) Windows() CertificateExpiryWindows {
	return CertificateExpiryWindows{WarningSeconds: p.WarningWindow.Seconds(), HighSeconds: p.HighWindow.Seconds(), LiveWarningSeconds: p.LiveWarningWindow.Seconds()}
}
