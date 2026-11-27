package config

import (
	"flag"
	"io"
	"testing"
)

func TestCertificateExpiryPrecedenceAndValidation(t *testing.T) {
	for _, tc := range []struct {
		name string
		file Config
		env  string
		args []string
		want int
		bad  bool
	}{
		{name: "automatic default"},
		{name: "one file percentage", file: Config{CertificateWarningPercent: "20"}, want: 20},
		{name: "environment overrides invalid file", file: Config{CertificateWarningPercent: "bad"}, env: "50", want: 50},
		{name: "flag overrides invalid environment", env: "invalid", args: []string{"--certificate-warning-percent=20"}, want: 20},
		{name: "restore automatic over file", file: Config{CertificateWarningPercent: "20"}, args: []string{"--certificate-warning-percent=auto"}},
		{name: "minimum", args: []string{"--certificate-warning-percent=1"}, want: 1},
		{name: "maximum", args: []string{"--certificate-warning-percent=100"}, want: 100},
		{name: "invalid file", file: Config{CertificateWarningPercent: "bad"}, bad: true},
		{name: "invalid environment", env: "bad", bad: true},
		{name: "empty flag", args: []string{"--certificate-warning-percent="}, bad: true},
		{name: "zero", args: []string{"--certificate-warning-percent=0"}, bad: true},
		{name: "negative", args: []string{"--certificate-warning-percent=-1"}, bad: true},
		{name: "above 100", args: []string{"--certificate-warning-percent=101"}, bad: true},
		{name: "fraction", args: []string{"--certificate-warning-percent=20.5"}, bad: true},
		{name: "duration", args: []string{"--certificate-warning-percent=10m"}, bad: true},
		{name: "overflow", args: []string{"--certificate-warning-percent=999999999999999999999"}, bad: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("RADAR_CERTIFICATE_WARNING_PERCENT", tc.env)
			fs := flag.NewFlagSet("certificate", flag.ContinueOnError)
			fs.SetOutput(io.Discard)
			flags := RegisterCertificateExpiryFlags(fs, tc.file)
			if err := fs.Parse(tc.args); err != nil {
				t.Fatal(err)
			}
			p, err := flags.Resolve()
			if (err != nil) != tc.bad {
				t.Fatalf("error=%v wantError=%v", err, tc.bad)
			}
			if err == nil && p.WarningPercent != tc.want {
				t.Fatalf("policy=%+v want=%d", p, tc.want)
			}
		})
	}
}

func TestCertificateExpiryConfigPersists(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, value := range []string{"20", "auto"} {
		if err := Save(Config{CertificateWarningPercent: value}); err != nil {
			t.Fatal(err)
		}
		loaded, err := LoadForStartup()
		if err != nil {
			t.Fatal(err)
		}
		p, err := loaded.CertificateExpiryPolicy()
		if err != nil {
			t.Fatal(err)
		}
		if loaded.CertificateWarningPercent != value || (value == "auto" && p.WarningPercent != 0) || (value == "20" && p.WarningPercent != 20) {
			t.Fatalf("restart policy=%+v file=%s", p, loaded.CertificateWarningPercent)
		}
	}
}

func TestOnlyPercentageSettingIsExposed(t *testing.T) {
	fs := flag.NewFlagSet("certificate", flag.ContinueOnError)
	RegisterCertificateExpiryFlags(fs, Config{})
	for _, name := range []string{"certificate-warning-window", "certificate-high-window", "certificate-live-warning-window"} {
		if fs.Lookup(name) != nil {
			t.Fatalf("removed knob %s exposed", name)
		}
	}
}
