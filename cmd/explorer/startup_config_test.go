package main

import (
	"context"
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStartupConfig(t *testing.T) {
	if os.Getenv("RADAR_TEST_STARTUP") == "1" {
		for i, arg := range os.Args {
			if arg == "--" {
				os.Args = append([]string{"radar"}, os.Args[i+1:]...)
				break
			}
		}
		flag.CommandLine = flag.NewFlagSet(os.Args[0], flag.ExitOnError)
		main()
		os.Exit(0)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, config := range []string{`{"port":"bad"}`, `{"mcpReadOnly":`, `{"mcpReadOnly":true}`} {
		t.Run(config, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("USERPROFILE", home)
			t.Setenv("RADAR_TEST_STARTUP", "1")
			t.Setenv("RADAR_CLOUD_MODE", "false")
			if err := os.MkdirAll(filepath.Join(home, ".radar"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(home, ".radar", "config.json"), []byte(config), 0o600); err != nil {
				t.Fatal(err)
			}
			cases := []struct {
				arg, want string
				fails     bool
			}{
				{"--help", "Usage of radar", false},
				{"--version", "radar ", false},
			}
			if config != `{"mcpReadOnly":true}` {
				cases = append(cases, struct {
					arg, want string
					fails     bool
				}{"", "invalid Radar config", true})
			}
			for _, tc := range cases {
				t.Run(tc.arg, func(t *testing.T) {
					ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
					defer cancel()
					args := []string{"-test.run=^TestStartupConfig$", "--"}
					if tc.arg != "" {
						args = append(args, tc.arg)
					}
					output, err := exec.CommandContext(ctx, executable, args...).CombinedOutput()
					if ctx.Err() != nil {
						t.Fatalf("startup timed out: %s", output)
					}
					if (err != nil) != tc.fails || !strings.Contains(string(output), tc.want) {
						t.Fatalf("%s: err=%v output=%s; want fails=%v containing %q", tc.arg, err, output, tc.fails, tc.want)
					}
				})
			}
		})
	}
}
