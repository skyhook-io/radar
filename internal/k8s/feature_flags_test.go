package k8s

import (
	"os"
	"reflect"
	"regexp"
	"sort"
	"testing"
)

// radarFeaturesPath is the frontend's table of endpoints an embedding host may
// find missing on an older Radar, with the flag each one is negotiated by.
const radarFeaturesPath = "../../web/src/api/radarFeatures.ts"

var flagInRadarFeatures = regexp.MustCompile(`flag:\s*'([A-Za-z]+)'`)

// Flags that hide a control rather than gate an endpoint, so an older Radar
// shows no upgrade note for them and they have no entry in the table.
var flagsWithoutUpgradeNote = map[string]bool{
	"yamlReview":     true,
	"yamlSchemas":    true,
	"workloadImages": true,
}

// TestFeatureFlagsHaveFrontendGates fails when FeatureCapabilities and the
// frontend's feature table drift apart. A flag without an entry means a newer
// UI calls the endpoint unguarded and shows an older Radar a red error instead
// of an upgrade note; an entry without a flag waits on a flag no Radar sends.
func TestFeatureFlagsHaveFrontendGates(t *testing.T) {
	raw, err := os.ReadFile(radarFeaturesPath)
	if err != nil {
		t.Fatalf("read %s: %v", radarFeaturesPath, err)
	}
	gated := map[string]bool{}
	for _, m := range flagInRadarFeatures.FindAllStringSubmatch(string(raw), -1) {
		gated[m[1]] = true
	}
	if len(gated) == 0 {
		t.Fatalf("no flags parsed from %s; did the table format change?", radarFeaturesPath)
	}

	advertised := map[string]bool{}
	featuresType := reflect.TypeOf(FeatureCapabilities{})
	for i := 0; i < featuresType.NumField(); i++ {
		advertised[featuresType.Field(i).Tag.Get("json")] = true
	}

	var ungated, unadvertised []string
	for flag := range advertised {
		if !gated[flag] && !flagsWithoutUpgradeNote[flag] {
			ungated = append(ungated, flag)
		}
	}
	for flag := range gated {
		if !advertised[flag] {
			unadvertised = append(unadvertised, flag)
		}
	}
	sort.Strings(ungated)
	sort.Strings(unadvertised)
	if len(ungated) > 0 {
		t.Errorf("FeatureCapabilities flags with no entry in %s: %v (add one, or list the flag in flagsWithoutUpgradeNote if it only hides a control)", radarFeaturesPath, ungated)
	}
	if len(unadvertised) > 0 {
		t.Errorf("%s gates on flags FeatureCapabilities does not advertise: %v", radarFeaturesPath, unadvertised)
	}
}
