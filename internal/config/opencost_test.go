package config

import "testing"

func TestNormalizeOpenCostCurrency(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		want    string
		wantErr bool
	}{
		{name: "auto", want: ""},
		{name: "trim and uppercase", raw: " gbp ", want: "GBP"},
		{name: "zero decimal currency", raw: "jpy", want: "JPY"},
		{name: "current code missing from CLDR", raw: "ves", want: "VES"},
		{name: "unknown", raw: "ZZZ", wantErr: true},
		{name: "malformed", raw: "EURO", wantErr: true},
		{name: "no currency", raw: "XXX", wantErr: true},
		{name: "testing code", raw: "XTS", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NormalizeOpenCostCurrency(tt.raw)
			if (err != nil) != tt.wantErr {
				t.Fatalf("NormalizeOpenCostCurrency(%q) error = %v, wantErr %v", tt.raw, err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("NormalizeOpenCostCurrency(%q) = %q, want %q", tt.raw, got, tt.want)
			}
		})
	}
}

func TestNormalizeOpenCostCurrencyAcceptsCurrentCodesMissingFromCLDR(t *testing.T) {
	for _, code := range []string{"MRU", "SLE", "UYW", "VED", "VES", "XAD", "XCG", "ZWG"} {
		t.Run(code, func(t *testing.T) {
			got, err := NormalizeOpenCostCurrency(code)
			if err != nil {
				t.Fatal(err)
			}
			if got != code {
				t.Fatalf("NormalizeOpenCostCurrency(%q) = %q, want %q", code, got, code)
			}
		})
	}
}
