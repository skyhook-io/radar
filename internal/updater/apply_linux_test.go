//go:build linux

package updater

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const swapHelperEnv = "RADAR_UPDATER_SWAP_HELPER"

// TestLaunchedExecutableSurvivesSwap runs a copy of this test binary that
// replaces itself the way applyUpdate does (rename to .old, new file in place,
// delete .old) and then asks where to relaunch from. On Linux os.Executable
// follows the running file to the deleted .old path at that point.
func TestLaunchedExecutableSurvivesSwap(t *testing.T) {
	if os.Getenv(swapHelperEnv) == "1" {
		swapHelper()
		return
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(dir, "radar-desktop")
	if err := copyExecutable(self, exe); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, "-test.run=^TestLaunchedExecutableSurvivesSwap$")
	cmd.Env = append(os.Environ(), swapHelperEnv+"=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("helper failed: %v\n%s", err, out)
	}
	var got string
	for _, line := range strings.Split(string(out), "\n") {
		if p, ok := strings.CutPrefix(line, "relaunch: "); ok {
			got = p
		}
	}
	if got != exe {
		t.Fatalf("relaunch path after the swap = %q, want %q\n%s", got, exe, out)
	}
	if _, err := os.Stat(got); err != nil {
		t.Fatalf("relaunch path does not exist: %v", err)
	}
}

func swapHelper() {
	exe, err := launchedExecutable()
	if err != nil {
		fmt.Println("error:", err)
		os.Exit(1)
	}
	if err := os.Rename(exe, exe+".old"); err != nil {
		fmt.Println("error:", err)
		os.Exit(1)
	}
	if err := os.WriteFile(exe, []byte("new binary"), 0o755); err != nil {
		fmt.Println("error:", err)
		os.Exit(1)
	}
	if err := os.Remove(exe + ".old"); err != nil {
		fmt.Println("error:", err)
		os.Exit(1)
	}
	again, err := launchedExecutable()
	if err != nil {
		fmt.Println("error:", err)
		os.Exit(1)
	}
	fmt.Println("relaunch: " + again)
}

func copyExecutable(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
