//go:build linux && cgo

package main

import (
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/skyhook-io/radar/internal/desktopenv"
)

func TestApplyChildEnvSplitsGoAndC(t *testing.T) {
	t.Setenv("RBQW_TEST_FORCED", "x11")       // image value; user had "wayland"
	t.Setenv("RBQW_TEST_ADDED", "/image/lib") // image value; user had it unset
	t.Setenv("RBQW_TEST_EMPTIED", "Adwaita")  // image value; user had it set to ""
	t.Cleanup(func() {
		for _, k := range []string{"RBQW_TEST_FORCED", "RBQW_TEST_ADDED", "RBQW_TEST_EMPTIED"} {
			_ = os.Unsetenv(k)
		}
	})

	applyChildEnv([]desktopenv.EnvChange{
		{Key: "RBQW_TEST_FORCED", Child: "wayland", ChildSet: true, Image: "x11", ImageSet: true},
		{Key: "RBQW_TEST_ADDED", Image: "/image/lib", ImageSet: true},
		{Key: "RBQW_TEST_EMPTIED", Child: "", ChildSet: true, Image: "Adwaita", ImageSet: true},
	})

	for _, tc := range []struct {
		key         string
		goVal, cVal string
		goSet       bool
	}{
		{key: "RBQW_TEST_FORCED", goVal: "wayland", goSet: true, cVal: "x11"},
		{key: "RBQW_TEST_ADDED", goSet: false, cVal: "/image/lib"},
		{key: "RBQW_TEST_EMPTIED", goVal: "", goSet: true, cVal: "Adwaita"},
	} {
		if v, ok := os.LookupEnv(tc.key); v != tc.goVal || ok != tc.goSet {
			t.Errorf("Go %s = %q,%v, want %q,%v", tc.key, v, ok, tc.goVal, tc.goSet)
		}
		if v, ok := cGetenv(tc.key); v != tc.cVal || !ok {
			t.Errorf("C %s = %q,%v, want %q", tc.key, v, ok, tc.cVal)
		}
	}

	// A Go child (no explicit Env) inherits Go's copy.
	out, err := exec.Command("env").Output()
	if err != nil {
		t.Fatal(err)
	}
	child := map[string]string{}
	for _, line := range strings.Split(string(out), "\n") {
		if k, v, ok := strings.Cut(line, "="); ok {
			child[k] = v
		}
	}
	if child["RBQW_TEST_FORCED"] != "wayland" {
		t.Errorf("child RBQW_TEST_FORCED = %q, want wayland", child["RBQW_TEST_FORCED"])
	}
	if _, ok := child["RBQW_TEST_ADDED"]; ok {
		t.Errorf("child must not get RBQW_TEST_ADDED")
	}
	if v, ok := child["RBQW_TEST_EMPTIED"]; !ok || v != "" {
		t.Errorf("child RBQW_TEST_EMPTIED = %q,%v, want set and empty", v, ok)
	}

	// The documented limit: a later os.Setenv writes through to C again.
	if err := os.Setenv("RBQW_TEST_FORCED", "broadway"); err != nil {
		t.Fatal(err)
	}
	if v, _ := cGetenv("RBQW_TEST_FORCED"); v != "broadway" {
		t.Errorf("os.Setenv after the split: C = %q, want broadway", v)
	}
}
