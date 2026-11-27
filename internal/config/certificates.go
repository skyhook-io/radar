package config

import (
	"flag"
	"fmt"
	"github.com/skyhook-io/radar/pkg/health"
	"os"
	"strconv"
)

type CertificateExpiryFlags struct {
	fs      *flag.FlagSet
	file    Config
	warning *string
}

func RegisterCertificateExpiryFlags(fs *flag.FlagSet, file Config) CertificateExpiryFlags {
	return CertificateExpiryFlags{fs: fs, file: file, warning: fs.String("certificate-warning-percent", "", "Warn when this percentage of certificate validity remains (1-100; default auto: one third; warnings capped at 30 days; env: RADAR_CERTIFICATE_WARNING_PERCENT)")}
}

func (f CertificateExpiryFlags) Resolve() (health.CertificateExpiryPolicy, error) {
	value := f.file.CertificateWarningPercent
	if env := os.Getenv("RADAR_CERTIFICATE_WARNING_PERCENT"); env != "" {
		value = env
	}
	explicitEmpty := false
	f.fs.Visit(func(v *flag.Flag) {
		if v.Name == "certificate-warning-percent" {
			value = *f.warning
			explicitEmpty = value == ""
		}
	})
	if explicitEmpty {
		return health.CertificateExpiryPolicy{}, fmt.Errorf("certificate-warning-percent must be a whole number from 1 to 100, such as 20, or auto")
	}
	return parseCertificateExpiry(value)
}

func (c Config) CertificateExpiryPolicy() (health.CertificateExpiryPolicy, error) {
	return parseCertificateExpiry(c.CertificateWarningPercent)
}

func parseCertificateExpiry(value string) (health.CertificateExpiryPolicy, error) {
	if value == "" || value == "auto" {
		return health.CertificateExpiryPolicy{}, nil
	}
	percent, err := strconv.Atoi(value)
	if err != nil || percent < 1 || percent > 100 {
		return health.CertificateExpiryPolicy{}, fmt.Errorf("certificate-warning-percent must be a whole number from 1 to 100, such as 20, or auto")
	}
	return health.CertificateExpiryPolicy{WarningPercent: percent}, nil
}
