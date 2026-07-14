package cloud

import "testing"

func TestCloudHandshakeHeadersAlwaysAdvertiseSelfUpgradeCapability(t *testing.T) {
	for _, tt := range []struct {
		name      string
		available bool
		want      string
	}{
		{name: "configured", available: true, want: "true"},
		{name: "disabled", available: false, want: "false"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			headers := cloudHandshakeHeaders(Config{
				Token:                "rhc_test",
				SelfUpgradeAvailable: tt.available,
			})
			values := headers.Values(selfUpgradeAvailableHeader)
			if len(values) != 1 || values[0] != tt.want {
				t.Fatalf("%s = %q, want exactly [%q]", selfUpgradeAvailableHeader, values, tt.want)
			}
		})
	}
}
