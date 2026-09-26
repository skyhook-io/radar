package server

import (
	"net"
	"testing"
)

func TestListenPreferringPortFallsBackWhenTaken(t *testing.T) {
	taken, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer taken.Close()
	port := taken.Addr().(*net.TCPAddr).Port

	if _, err := listenPreferringPort("127.0.0.1", port, false); err == nil {
		t.Fatal("without fallback, binding a taken port succeeded")
	}
	ln, err := listenPreferringPort("127.0.0.1", port, true)
	if err != nil {
		t.Fatalf("with fallback: %v", err)
	}
	defer ln.Close()
	if got := ln.Addr().(*net.TCPAddr).Port; got == port || got == 0 {
		t.Fatalf("fallback bound port %d, want a different OS-assigned port", got)
	}
}

func TestListenPreferringPortUsesTheFreePort(t *testing.T) {
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := probe.Addr().(*net.TCPAddr).Port
	probe.Close()
	ln, err := listenPreferringPort("127.0.0.1", port, true)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	if got := ln.Addr().(*net.TCPAddr).Port; got != port {
		t.Fatalf("bound %d, want the preferred %d", got, port)
	}
}
