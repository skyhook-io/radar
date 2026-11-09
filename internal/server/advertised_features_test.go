package server

import (
	"reflect"
	"testing"
)

// A flag that is declared but left out of advertisedFeatures reads as an older
// Radar, so the frontend silently keeps the fallback for its own endpoint.
func TestAdvertisedFeaturesSetsEveryFlag(t *testing.T) {
	features := reflect.ValueOf(advertisedFeatures())
	for i := range features.NumField() {
		if !features.Field(i).Bool() {
			t.Errorf("%s is declared in FeatureCapabilities but not advertised", features.Type().Field(i).Name)
		}
	}
}
