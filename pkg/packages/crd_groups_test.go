package packages

import (
	"regexp"
	"testing"
)

// The usage receiver checks every integration name against this pattern and
// refuses the whole report when one fails, so a long or oddly spelled chart
// name here would silently drop every usage report from clusters running it.
var usageReceiverToken = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)

func TestIntegrationNamesFitUsageReceiver(t *testing.T) {
	for group, name := range crdGroupToChart {
		if !usageReceiverToken.MatchString(name) {
			t.Errorf("crdGroupToChart[%q]=%q would make the usage receiver refuse the report", group, name)
		}
	}
}
