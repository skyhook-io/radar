package server

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/skyhook-io/radar/internal/k8s"
)

// A listing path must name a folder. find and ls read a leading dash as an
// option, so a relative path never reaches the container.
func TestPodFileListRefusesPathsThatAreNotAbsolute(t *testing.T) {
	prev := k8s.GetConnectionStatus()
	k8s.SetConnectionStatus(k8s.ConnectionStatus{State: k8s.StateConnected})
	t.Cleanup(func() { k8s.SetConnectionStatus(prev) })

	router := chi.NewRouter()
	router.Get("/api/pods/{namespace}/{name}/files", (&Server{}).handlePodFileList)
	ts := httptest.NewServer(router)
	t.Cleanup(ts.Close)

	for _, p := range []string{"-delete", "-exec", "data", "./data", "--help"} {
		resp, err := http.Get(ts.URL + "/api/pods/ns/pod/files?container=app&path=" + url.QueryEscape(p))
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("path %q: status %d, want 400", p, resp.StatusCode)
		}
	}
}

// One entry per NUL: a file name holding a newline and tabs stays one entry
// with that name, and can't add a line that reads as another path.
func TestParseFindOutputKeepsPlantedNamesInTheirFolder(t *testing.T) {
	planted := "x\nd\t0\t0\t755\t-delete"
	out := "d\t4096\t1.0\t755\t/data\x00" +
		"f\t3\t1.0\t644\t/data/app.conf\x00" +
		"f\t0\t1.0\t644\t/data/" + planted + "\x00" +
		"d\t0\t1.0\t755\t-delete\x00" +
		"d\t0\t1.0\t755\t/etc\x00"
	nodes, total := parseFindOutput(out, "/data")
	if total != 2 || len(nodes) != 2 {
		t.Fatalf("got %d entries %+v, want 2", total, nodes)
	}
	names := map[string]string{}
	for _, n := range nodes {
		names[n.Name] = n.Path
		if !strings.HasPrefix(n.Path, "/data/") {
			t.Errorf("entry %q escapes the folder", n.Path)
		}
	}
	if names["app.conf"] != "/data/app.conf" || names[planted] != "/data/"+planted {
		t.Errorf("entries = %v", names)
	}
}

func TestParseLSOutputRejectsPlantedPaths(t *testing.T) {
	for _, dir := range []string{"/data", "/"} {
		t.Run(dir, func(t *testing.T) {
			listing := "-rw-r--r-- 1 root root 1 Jan 1 00:00 good.txt\n" +
				"-rw-r--r-- 1 root root 1 Jan 1 00:00 x\n" +
				"-rw-r--r-- 1 root root 5 Jan 1 00:00 ../../etc/shadow\n" +
				"-rw-r--r-- 1 root root 5 Jan 1 00:00 /etc/shadow\n" +
				"-rw-r--r-- 1 root root 5 Jan 1 00:00 nested/fake.txt\n" +
				"lrwxrwxrwx 1 root root 5 Jan 1 00:00 link -> ../../etc/shadow\n"
			nodes := parseLSOutput(listing, dir)
			if len(nodes) != 3 {
				t.Fatalf("got %d nodes, want good.txt, x, link: %+v", len(nodes), nodes)
			}
			for _, node := range nodes {
				if strings.Contains(node.Name, "/") {
					t.Fatalf("planted name accepted: %q", node.Name)
				}
			}
			foundLink := false
			for _, node := range nodes {
				if node.Name == "link" {
					foundLink = true
					if node.LinkTarget != "../../etc/shadow" {
						t.Fatalf("lost literal symlink target: %q", node.LinkTarget)
					}
				}
			}
			if !foundLink {
				t.Fatal("lost valid symlink entry")
			}
		})
	}
}
