package main

import (
	"reflect"
	"testing"
)

func TestHeaderFlagSet(t *testing.T) {
	cases := []struct {
		name    string
		inputs  []string
		want    map[string]string
		wantErr bool
	}{
		{
			name:   "simple key=value",
			inputs: []string{"Authorization=Bearer abc"},
			want:   map[string]string{"Authorization": "Bearer abc"},
		},
		{
			// Bearer / Basic tokens commonly contain '=' padding — splitting
			// on the first '=' (not all) is load-bearing.
			name:   "value preserves embedded =",
			inputs: []string{"Authorization=Bearer abc=def="},
			want:   map[string]string{"Authorization": "Bearer abc=def="},
		},
		{
			name:   "empty value accepted",
			inputs: []string{"X-Empty="},
			want:   map[string]string{"X-Empty": ""},
		},
		{
			name:   "two flags accumulate",
			inputs: []string{"Authorization=Bearer abc", "X-Scope-OrgID=tenant-1"},
			want: map[string]string{
				"Authorization": "Bearer abc",
				"X-Scope-OrgID": "tenant-1",
			},
		},
		{
			name:   "key trimmed",
			inputs: []string{"  Authorization  =Bearer abc"},
			want:   map[string]string{"Authorization": "Bearer abc"},
		},
		{
			name:    "no equals",
			inputs:  []string{"Authorization"},
			wantErr: true,
		},
		{
			name:    "empty key",
			inputs:  []string{"=value"},
			wantErr: true,
		},
		{
			name:    "whitespace-only key",
			inputs:  []string{"   =value"},
			wantErr: true,
		},
		{
			name:    "invalid header name with space",
			inputs:  []string{"Bad Header=value"},
			wantErr: true,
		},
		{
			name:    "CRLF injection in value",
			inputs:  []string{"X-Foo=safe\r\nX-Injected: evil"},
			wantErr: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHeaderFlag(nil)
			var lastErr error
			for _, in := range tc.inputs {
				lastErr = h.Set(in)
				if lastErr != nil {
					break
				}
			}
			if tc.wantErr {
				if lastErr == nil {
					t.Fatalf("expected error, got nil")
				}
				return
			}
			if lastErr != nil {
				t.Fatalf("unexpected error: %v", lastErr)
			}
			if !reflect.DeepEqual(h.value(), tc.want) {
				t.Errorf("got %v, want %v", h.value(), tc.want)
			}
		})
	}
}

// File defaults must survive when no CLI flag is passed, but the FIRST CLI
// flag must wipe them outright (kubectl-style replacement, not merge).
// Easy to break in a refactor; impossible to catch without a test because
// the surprise lives entirely in the `overrides` latch.
func TestHeaderFlagOverridesFileDefaults(t *testing.T) {
	defaults := map[string]string{
		"Authorization": "Bearer from-file",
		"X-Tenant":      "from-file",
	}

	t.Run("no CLI flags keeps defaults", func(t *testing.T) {
		h := newHeaderFlag(defaults)
		if !reflect.DeepEqual(h.value(), defaults) {
			t.Errorf("got %v, want %v", h.value(), defaults)
		}
	})

	t.Run("one CLI flag wipes defaults", func(t *testing.T) {
		h := newHeaderFlag(defaults)
		if err := h.Set("X-New=from-cli"); err != nil {
			t.Fatalf("Set failed: %v", err)
		}
		want := map[string]string{"X-New": "from-cli"}
		if !reflect.DeepEqual(h.value(), want) {
			t.Errorf("got %v, want %v (defaults must NOT survive a single CLI override)", h.value(), want)
		}
	})

	t.Run("subsequent CLI flags accumulate, not re-wipe", func(t *testing.T) {
		h := newHeaderFlag(defaults)
		if err := h.Set("X-First=1"); err != nil {
			t.Fatalf("Set failed: %v", err)
		}
		if err := h.Set("X-Second=2"); err != nil {
			t.Fatalf("Set failed: %v", err)
		}
		want := map[string]string{"X-First": "1", "X-Second": "2"}
		if !reflect.DeepEqual(h.value(), want) {
			t.Errorf("got %v, want %v", h.value(), want)
		}
	})
}

func TestHeaderFlagValueCopies(t *testing.T) {
	h := newHeaderFlag(map[string]string{"A": "1"})
	got := h.value()
	got["A"] = "mutated"
	if h.value()["A"] != "1" {
		t.Error("value() must return a defensive copy — caller mutation leaked into headerFlag state")
	}
}
