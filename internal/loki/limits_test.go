package loki

import (
	"errors"
	"testing"
)

func TestReadLimits(t *testing.T) {
	releaseA, err := acquireRead("alice")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := acquireRead("alice"); !errors.Is(err, ErrBusy) {
		t.Fatalf("a second read by one user: %v, want ErrBusy", err)
	}
	releaseA()
	releaseA() // a second release must not free another read's slot
	var releases []func()
	for _, u := range []string{"a", "b", "c", "d"} {
		r, err := acquireRead(u)
		if err != nil {
			t.Fatalf("%s: %v", u, err)
		}
		releases = append(releases, r)
	}
	if _, err := acquireRead("e"); !errors.Is(err, ErrBusy) {
		t.Fatalf("a fifth concurrent read: %v, want ErrBusy", err)
	}
	for _, r := range releases {
		r()
	}
	r, err := acquireRead("e")
	if err != nil {
		t.Fatalf("after release: %v", err)
	}
	r()
}
