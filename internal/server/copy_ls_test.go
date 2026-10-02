package server

import (
	"testing"

	"github.com/skyhook-io/radar/internal/images"
)

// busyboxLS is `ls -la` output from a BusyBox image, where the listing falls
// back to ls because find has no -printf.
const busyboxLS = `total 24
drwxr-xr-x    4 root     root          4096 Sep 29 10:00 .
drwxr-xr-x    1 root     root          4096 Sep 29 09:00 ..
-rw-r--r--    1 root     root          1234 Sep 29 10:00 my report.txt
-rw-r--r--    1 root     root            42 Sep 29 10:00 two  spaces.txt
lrwxrwxrwx    1 root     root            20 Sep 29 10:00 current link -> /data/releases/v 2
crw-rw-rw-    1 root     root        1,   3 Sep 29 10:00 null
drwxr-xr-x    2 root     root          4096 Jan  3  2025 old logs
-rw-r--r--    1 root     root             7 Sep 29 10:00 plain
`

func TestParseLSOutput(t *testing.T) {
	nodes := parseLSOutput(busyboxLS, "/data")

	byName := map[string]*images.FileNode{}
	for _, n := range nodes {
		byName[n.Name] = n
	}

	want := []images.FileNode{
		{Name: "my report.txt", Path: "/data/my report.txt", Type: "file", Size: 1234},
		{Name: "two  spaces.txt", Path: "/data/two  spaces.txt", Type: "file", Size: 42},
		{Name: "current link", Path: "/data/current link", Type: "symlink", Size: 20, LinkTarget: "/data/releases/v 2"},
		{Name: "null", Path: "/data/null", Type: "file", Size: 0},
		{Name: "old logs", Path: "/data/old logs", Type: "dir", Size: 4096},
		{Name: "plain", Path: "/data/plain", Type: "file", Size: 7},
	}

	if len(nodes) != len(want) {
		names := make([]string, 0, len(nodes))
		for _, n := range nodes {
			names = append(names, n.Name)
		}
		t.Fatalf("got %d entries %q, want %d", len(nodes), names, len(want))
	}
	for _, w := range want {
		got, ok := byName[w.Name]
		if !ok {
			t.Errorf("missing entry %q", w.Name)
			continue
		}
		if got.Path != w.Path || got.Type != w.Type || got.Size != w.Size || got.LinkTarget != w.LinkTarget {
			t.Errorf("entry %q = {Path:%q Type:%q Size:%d LinkTarget:%q}, want {Path:%q Type:%q Size:%d LinkTarget:%q}",
				w.Name, got.Path, got.Type, got.Size, got.LinkTarget, w.Path, w.Type, w.Size, w.LinkTarget)
		}
	}
}

func TestParseLSOutputSkipsLinesWithoutAName(t *testing.T) {
	out := "total 0\n-rw-r--r-- 1 root root 12 Sep 29 10:00\n\n"
	if nodes := parseLSOutput(out, "/data"); len(nodes) != 0 {
		t.Fatalf("expected no entries, got %d (first %q)", len(nodes), nodes[0].Name)
	}
}
