package audit

import (
	"os"
	"regexp"
	"strconv"
	"testing"

	bp "github.com/skyhook-io/radar/pkg/audit"
)

func TestREADMECheckCountMatchesRegistry(t *testing.T) {
	readme, err := os.ReadFile("../../README.md")
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`scanner with (\d+) checks`).FindSubmatch(readme)
	if m == nil {
		t.Fatal(`README.md no longer says "scanner with N checks" in the Cluster Audit section`)
	}
	if n, _ := strconv.Atoi(string(m[1])); n != len(bp.CheckRegistry) {
		t.Errorf("README.md says %d audit checks, CheckRegistry has %d: update README.md, and the marketing site's AUDIT_CHECK_COUNT and the radar-docs Cluster Audit page, which list the checks too", n, len(bp.CheckRegistry))
	}
}
