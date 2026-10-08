//go:build linux

package main

import (
	"os"
	"testing"
)

const (
	envDisableDMABUF = "WEBKIT_DISABLE_DMABUF_RENDERER"
	envForceSHM      = "WEBKIT_DMABUF_RENDERER_FORCE_SHM"
)

func unsetForTest(t *testing.T, keys ...string) {
	t.Helper()
	for _, k := range keys {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}
}

func TestApplyWebKitDefaultsForcesSHMWithoutDisablingDMABUF(t *testing.T) {
	unsetForTest(t, envDisableDMABUF, envForceSHM)

	applyWebKitDefaults()

	if v, ok := os.LookupEnv(envForceSHM); !ok || v != "1" {
		t.Errorf("%s = %q (set=%v), want \"1\"", envForceSHM, v, ok)
	}
	// Setting it empties WebKit's buffer-transport set, and a view transition
	// then crashes the UI process on a null backing store.
	if _, ok := os.LookupEnv(envDisableDMABUF); ok {
		t.Errorf("%s must stay unset", envDisableDMABUF)
	}
}

func TestApplyWebKitDefaultsRespectsUserChoice(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
	}{
		{"user disabled DMABUF", map[string]string{envDisableDMABUF: "1"}},
		{"user re-enabled DMABUF", map[string]string{envDisableDMABUF: "0"}},
		{"user opted out of SHM", map[string]string{envForceSHM: "0"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			unsetForTest(t, envDisableDMABUF, envForceSHM)
			for k, v := range tc.env {
				t.Setenv(k, v)
			}

			applyWebKitDefaults()

			for _, k := range []string{envDisableDMABUF, envForceSHM} {
				want, wantSet := tc.env[k]
				got, gotSet := os.LookupEnv(k)
				if got != want || gotSet != wantSet {
					t.Errorf("%s = %q (set=%v), want %q (set=%v)", k, got, gotSet, want, wantSet)
				}
			}
		})
	}
}
