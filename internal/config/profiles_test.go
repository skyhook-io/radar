package config

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/gofrs/flock"
	"github.com/skyhook-io/radar/pkg/prom"
)

func TestProfileStoreRejectsInvalidFiles(t *testing.T) {
	for _, data := range []string{`{"version":1,"profiles":{},"connections":{}}`, `null`, `{}`, `{"version":2,"profiles":{}}`, `{"version":1}`, `{"version":1,"profiles":null}`, `{"version":1,"profiles":{},"typo":"secret"}`, `{"version":1,"profiles":{}} {}`, `{"version":1,"profiles":{"a":{"context":"a","target":"a","prometheus":{"url":"https://user:secret@prom"}}}}`} {
		t.Run(data, func(t *testing.T) {
			s := &ProfileStore{Path: filepath.Join(t.TempDir(), "clusters.json")}
			if err := os.WriteFile(s.Path, []byte(data), 0600); err != nil {
				t.Fatal(err)
			}
			_, _, err := s.Read()
			if err == nil || strings.Contains(err.Error(), "secret") {
				t.Fatalf("unsafe validation: %v", err)
			}
			called := false
			_, err = s.Update(context.Background(), "", func(*ClusterProfiles) error { called = true; return nil })
			if err == nil || called {
				t.Fatal("invalid file allowed mutation")
			}
			got, _ := os.ReadFile(s.Path)
			if string(got) != data {
				t.Fatal("invalid file overwritten")
			}
		})
	}
}

func TestProfileStoreAtomicCAS(t *testing.T) {
	s := &ProfileStore{Path: filepath.Join(t.TempDir(), "private", "clusters.json")}
	_, revision, err := s.Read()
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, binding := range []string{"a", "b"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			independent := &ProfileStore{Path: s.Path}
			_, err := independent.Update(context.Background(), revision, func(p *ClusterProfiles) error {
				p.Profiles[binding] = ClusterProfile{Context: binding, Integrations: map[Integration]IntegrationSettings{IntegrationMetrics: {Target: binding, Prometheus: &prom.Connection{URL: "https://prom/" + binding}}}}
				return nil
			})
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	success, conflict := 0, 0
	for err := range results {
		if err == nil {
			success++
		} else if errors.Is(err, ErrProfileConflict) {
			conflict++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatalf("success %d conflict %d", success, conflict)
	}
	p, revision, err := s.Read()
	if err != nil || len(p.Profiles) != 1 {
		t.Fatalf("invalid commit: %+v %v", p, err)
	}
	if runtime.GOOS != "windows" {
		for path, mode := range map[string]os.FileMode{s.Path: 0600, s.Path + ".lock": 0600, filepath.Dir(s.Path): 0700} {
			info, err := os.Stat(path)
			if err != nil || info.Mode().Perm() != mode {
				t.Fatalf("permissions %s: %v %v", path, info, err)
			}
		}
	}
	before, _ := os.ReadFile(s.Path)
	if err := os.WriteFile(s.Path, append(before, ' '), 0600); err != nil {
		t.Fatal(err)
	}
	_, err = s.Update(context.Background(), revision, func(*ClusterProfiles) error { t.Fatal("stale revision reached mutation"); return nil })
	if !errors.Is(err, ErrProfileConflict) {
		t.Fatalf("hand edit: %v", err)
	}
	leftovers, _ := filepath.Glob(filepath.Join(filepath.Dir(s.Path), ".clusters-*.json"))
	if len(leftovers) != 0 {
		t.Fatalf("temporary credential files left behind: %v", leftovers)
	}
}

func TestProfileStoreNewerVersionIsNotReportedAsCorrupt(t *testing.T) {
	for _, raw := range []string{`{"version":2,"profiles":{}}`, `{"version":2,"profiles":{},"newField":{"secret":"do-not-display"}}`} {
		s := &ProfileStore{Path: filepath.Join(t.TempDir(), "clusters.json")}
		if err := os.WriteFile(s.Path, []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
		_, _, err := s.Read()
		if err == nil || !strings.Contains(err.Error(), "upgrade Radar") || strings.Contains(err.Error(), "repair") || strings.Contains(err.Error(), "do-not-display") {
			t.Fatalf("incorrect newer-format guidance: %v", err)
		}
		if _, err := s.Update(context.Background(), "", func(*ClusterProfiles) error { t.Fatal("newer file reached mutation"); return nil }); err == nil {
			t.Fatal("newer file allowed mutation")
		}
		data, err := os.ReadFile(s.Path)
		if err != nil || string(data) != raw {
			t.Fatal("newer file was modified", err)
		}
	}
}

func TestProfileStoreDecodeErrorsDoNotExposeContents(t *testing.T) {
	for _, tc := range []struct {
		raw          string
		wantLocation bool
	}{
		{`{"version":1,"profiles":{},"do-not-display":"secret"}`, false},
		{`{"version":1,"profiles":{"a":{"context":"dev","integrations":{"metrics":{"prometheus":{"url":12345}}}}}}`, true},
		{`{"version":1,"profiles":{} "do-not-display"}`, true},
		{`{"version":"do-not-display","profiles":{}}`, true},
		{`{"version":1,"profiles":{}} {"do-not-display":true}`, true},
	} {
		s := &ProfileStore{Path: filepath.Join(t.TempDir(), "clusters.json")}
		if err := os.WriteFile(s.Path, []byte(tc.raw), 0600); err != nil {
			t.Fatal(err)
		}
		_, _, err := s.Read()
		if err == nil || strings.Contains(err.Error(), "near byte") != tc.wantLocation || strings.Contains(err.Error(), "do-not-display") || strings.Contains(err.Error(), "12345") {
			t.Fatalf("unsafe or unhelpful decode error: %v", err)
		}
	}
}

func TestProfileStoreCancelledLock(t *testing.T) {
	s := &ProfileStore{Path: filepath.Join(t.TempDir(), "clusters.json")}
	lock := flock.New(s.Path + ".lock")
	if err := lock.Lock(); err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := s.Update(ctx, "", func(*ClusterProfiles) error { t.Fatal("cancelled writer ran"); return nil })
	if err == nil {
		t.Fatal("cancelled lock succeeded")
	}
	if _, err := os.Stat(s.Path); !os.IsNotExist(err) {
		t.Fatalf("cancelled writer created profile: %v", err)
	}
}

func TestProfileStoreAcrossProcesses(t *testing.T) {
	if path := os.Getenv("RADAR_PROFILE_CHILD_PATH"); path != "" {
		s := &ProfileStore{Path: path}
		_, err := s.Update(context.Background(), os.Getenv("RADAR_PROFILE_CHILD_REVISION"), func(p *ClusterProfiles) error {
			p.Imported[IntegrationMetrics] = true
			return nil
		})
		if errors.Is(err, ErrProfileConflict) {
			os.Exit(3)
		}
		if err != nil {
			t.Fatal(err)
		}
		return
	}
	s := &ProfileStore{Path: filepath.Join(t.TempDir(), "clusters.json")}
	_, revision, err := s.Read()
	if err != nil {
		t.Fatal(err)
	}
	commands := make([]*exec.Cmd, 2)
	for i := range commands {
		commands[i] = exec.Command(os.Args[0], "-test.run=^TestProfileStoreAcrossProcesses$")
		commands[i].Env = append(os.Environ(), "RADAR_PROFILE_CHILD_PATH="+s.Path, "RADAR_PROFILE_CHILD_REVISION="+revision)
		if err := commands[i].Start(); err != nil {
			t.Fatal(err)
		}
	}
	success, conflict := 0, 0
	for _, cmd := range commands {
		err := cmd.Wait()
		if err == nil {
			success++
		} else if cmd.ProcessState.ExitCode() == 3 {
			conflict++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatalf("cross-process CAS: success=%d conflict=%d", success, conflict)
	}
}
