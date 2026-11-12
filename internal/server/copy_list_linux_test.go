//go:build linux

package server

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Runs the listing's own find command on this host. Where find has no -printf
// (BusyBox), the handler falls back to ls and this is skipped.
func TestPodFileListFindCommandWithPlantedName(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	planted := "x\nd\t0\t0\t755\t-delete"
	for _, name := range []string{"app.conf", planted} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cmd := podFileListFindCommand(dir)
	out, err := exec.Command(cmd[0], cmd[1:]...).Output()
	if err != nil {
		t.Skipf("find -printf unavailable here: %v", err)
	}
	nodes, total := parseFindOutput(string(out), dir)
	if total != 2 {
		t.Fatalf("got %d entries %+v, want 2", total, nodes)
	}
	for _, n := range nodes {
		if !strings.HasPrefix(n.Path, dir+"/") || (n.Name != "app.conf" && n.Name != planted) {
			t.Errorf("unexpected entry %+v", n)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "app.conf")); err != nil {
		t.Errorf("listing changed the folder: %v", err)
	}
}
