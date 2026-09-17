package memlimit

import (
	"os"
	"path/filepath"
	"testing"
)

const gib = int64(1) << 30

func writeTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, content := range files {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestLimitFromFS(t *testing.T) {
	tests := []struct {
		name     string
		self     string
		files    map[string]string
		memTotal int64
		want     int64
	}{
		{
			name:  "v2 private namespace: limit sits at the mount root",
			self:  "0::/",
			files: map[string]string{"memory.max": "1073741824"},
			want:  gib,
		},
		{
			name: "v2 host namespace: limit sits on the nested cgroup, root is max",
			self: "0::/kubepods.slice/kubepods-burstable.slice/cri-containerd-abc.scope",
			files: map[string]string{
				"memory.max":                "max",
				"kubepods.slice/memory.max": "max",
				"kubepods.slice/kubepods-burstable.slice/memory.max":                          "max",
				"kubepods.slice/kubepods-burstable.slice/cri-containerd-abc.scope/memory.max": "536870912",
			},
			want: 512 << 20,
		},
		{
			name: "v2 nested: a tighter ancestor cap wins",
			self: "0::/parent/child",
			files: map[string]string{
				"parent/memory.max":       "268435456",
				"parent/child/memory.max": "1073741824",
			},
			want: 256 << 20,
		},
		{
			name:  "v2 unlimited everywhere",
			self:  "0::/",
			files: map[string]string{"memory.max": "max"},
			want:  0,
		},
		{
			name: "v1: controller path, unlimited sentinel on the root",
			self: "12:memory:/docker/abc\n0::/",
			files: map[string]string{
				"memory/memory.limit_in_bytes":            "9223372036854771712",
				"memory/docker/abc/memory.limit_in_bytes": "1073741824",
			},
			want: gib,
		},
		{
			name:  "v1 in a comma-joined controller list",
			self:  "3:cpu,memory,pids:/",
			files: map[string]string{"memory/memory.limit_in_bytes": "536870912"},
			want:  512 << 20,
		},
		{
			name:     "host limit leaking through is not a cap",
			self:     "0::/",
			files:    map[string]string{"memory.max": "68719476736"},
			memTotal: 16 * gib,
			want:     0,
		},
		{
			name:     "a limit equal to MemTotal is a pod sized to the node",
			self:     "0::/",
			files:    map[string]string{"memory.max": "17179869184"},
			memTotal: 16 * gib,
			want:     16 * gib,
		},
		{
			name:  "unparseable /proc/self/cgroup",
			self:  "garbage",
			files: map[string]string{"memory.max": "1073741824"},
			want:  0,
		},
		{
			name:  "cgroup path escaping the mount is ignored",
			self:  "0::/../../etc",
			files: map[string]string{"memory.max": "1073741824"},
			want:  0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			writeTree(t, root, tt.files)
			if got := limitFromFS(root, tt.self, tt.memTotal); got != tt.want {
				t.Fatalf("limitFromFS() = %d, want %d", got, tt.want)
			}
		})
	}
}
