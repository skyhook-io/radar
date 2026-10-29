package nodeimage

import (
	"archive/tar"
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/layout"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/tarball"
	"github.com/skyhook-io/radar/internal/images"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestLoadImportedContentAndApplyWhiteouts(t *testing.T) {
	layer := func(files map[string]string) io.Reader {
		var b bytes.Buffer
		w := tar.NewWriter(&b)
		for name, content := range files {
			if err := w.WriteHeader(&tar.Header{Name: name, Size: int64(len(content)), Mode: 0644}); err != nil {
				t.Fatal(err)
			}
			if _, err := w.Write([]byte(content)); err != nil {
				t.Fatal(err)
			}
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
		return &b
	}
	l1, err := tarball.LayerFromReader(layer(map[string]string{"removed": "private", "kept": "hello"}))
	if err != nil {
		t.Fatal(err)
	}
	l2, err := tarball.LayerFromReader(layer(map[string]string{".wh.removed": "", "added": "world"}))
	if err != nil {
		t.Fatal(err)
	}
	img, err := mutate.AppendLayers(empty.Image, l1, l2)
	if err != nil {
		t.Fatal(err)
	}
	store := t.TempDir()
	lp, err := layout.Write(store, empty.Index)
	if err != nil {
		t.Fatal(err)
	}
	if err := lp.AppendImage(img); err != nil {
		t.Fatal(err)
	}
	digest, err := img.Digest()
	if err != nil {
		t.Fatal(err)
	}
	read := func(ctx context.Context, d string, w io.Writer) error { return CopyBlob(store, d, w) }
	loaded, err := loadImage(context.Background(), t.TempDir(), digest.String(), "linux/arm64", read)
	if err != nil {
		t.Fatal(err)
	}
	fs, err := images.InspectLocal(context.Background(), loaded, "local-only:test")
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, child := range fs.Root.Children {
		names[child.Name] = true
	}
	if names["removed"] || !names["kept"] || !names["added"] {
		t.Fatalf("incorrect merged filesystem: %v", names)
	}
	// A bare local ID can identify the image config, not its manifest.
	config, err := img.ConfigName()
	if err != nil {
		t.Fatal(err)
	}
	_, err = loadImage(context.Background(), t.TempDir(), config.String(), "linux/arm64", read)
	if err == nil || !strings.Contains(err.Error(), "runtime metadata") {
		t.Fatalf("config ID accepted as manifest: %v", err)
	}
	// Missing compressed layers must fail, never silently show a partial image.
	hash, err := l1.Digest()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(store, "blobs", "sha256", hash.Hex)); err != nil {
		t.Fatal(err)
	}
	if _, err := loadImage(context.Background(), t.TempDir(), digest.String(), "linux/arm64", read); err == nil {
		t.Fatal("missing layer accepted")
	}
}

func TestRejectCorruptBlobAndPathEscape(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	_, err := loadImage(context.Background(), t.TempDir(), digest, "linux/arm64", func(_ context.Context, _ string, w io.Writer) error {
		_, err := w.Write([]byte(`{"schemaVersion":2}`))
		return err
	})
	if err == nil || !strings.Contains(err.Error(), "verification") {
		t.Fatalf("corruption accepted: %v", err)
	}
	for _, d := range []string{"../../etc/passwd", "sha256:../passwd", strings.ToUpper(digest)} {
		if err := CopyBlob(t.TempDir(), d, io.Discard); err == nil {
			t.Fatalf("accepted %q", d)
		}
	}
	store := t.TempDir()
	if err := os.MkdirAll(filepath.Join(store, "blobs"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(store, "blobs", "sha256")); err != nil {
		t.Fatal(err)
	}
	if err := CopyBlob(store, digest, io.Discard); err == nil {
		t.Fatal("symlink escape accepted")
	}
}

func TestHelperPolicyAndImageID(t *testing.T) {
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "workload", Namespace: "test", UID: "uid"}, Spec: corev1.PodSpec{NodeName: "worker", Containers: []corev1.Container{{Name: "app"}}}, Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{Name: "app", Image: "local:test", ImageID: "containerd://sha256:" + strings.Repeat("a", 64)}}}}
	_, digest, err := workloadImage(pod, "")
	if err != nil || digest != "sha256:"+strings.Repeat("a", 64) {
		t.Fatalf("bare ID was not retained for content probing: %s %v", digest, err)
	}
	h := helperPod(pod, "radar:test", "/var/lib/containerd/io.containerd.content.v1.content")
	s := h.Spec
	c := s.Containers[0]
	if s.NodeName != "worker" || *s.AutomountServiceAccountToken || s.HostPID || s.HostIPC || s.HostNetwork || *c.SecurityContext.Privileged || *c.SecurityContext.AllowPrivilegeEscalation || !*c.SecurityContext.ReadOnlyRootFilesystem || !c.VolumeMounts[0].ReadOnly {
		t.Fatal("helper policy grants unexpected privileges")
	}
	if *s.ActiveDeadlineSeconds != 180 || s.RestartPolicy != corev1.RestartPolicyNever || s.Volumes[0].HostPath.Path == "/" {
		t.Fatal("helper lifetime or mount is unsafe")
	}
	if len(c.SecurityContext.Capabilities.Drop) != 1 || c.SecurityContext.Capabilities.Drop[0] != "ALL" {
		t.Fatal("capabilities were not dropped")
	}
}
