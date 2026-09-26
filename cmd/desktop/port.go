package main

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// The webview's origin includes the server's port, and browser storage is
// kept per origin. A new port on every launch would start each session with
// empty storage — theme, favorites, the pinned nav rail, recent resources,
// log viewer and AI panel settings — so Desktop reuses the port it had last
// time, falling back to an OS-assigned one when that port is taken.

const desktopPortFile = "desktop-port"

func desktopPortPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".radar", desktopPortFile)
}

func lastDesktopPort(path string) int {
	if path == "" {
		return 0
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	port, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || port <= 0 || port > 65535 {
		return 0
	}
	return port
}

// rememberDesktopPort runs once the window is up, so a launch that fails
// before then records nothing. A launch that fell back keeps the remembered
// port unless it's clearly lost: something other than Radar answers there, or
// nothing answers and it still can't be bound — as when Windows reserves the
// range for Hyper-V/WSL, which a reboot can move. Another Radar (a second
// Desktop window), a timeout, or a port that frees up again all keep it; the
// next launch gets it back.
func rememberDesktopPort(path string, remembered, actual int, owner func(port int) portOwnerKind, canBind func(port int) bool) {
	if remembered != 0 && actual != remembered {
		switch owner(remembered) {
		case ownerRadar, ownerUnknown:
			return
		case ownerSilent:
			if canBind(remembered) {
				return
			}
		}
	}
	recordDesktopPort(path, actual)
}

type portOwnerKind int

const (
	ownerUnknown portOwnerKind = iota // no answer in time: maybe a busy Radar
	ownerSilent                       // nothing accepted the connection
	ownerRadar
	ownerOther
)

// portOwner asks who holds a loopback port. /api/connection answers from
// memory, so a busy Radar still replies quickly, and ?contexts=0 keeps the
// reply small however many kubeconfig contexts there are. Dial errors are
// told apart only by timeout: refusal error codes differ between Windows and
// Unix, and any non-timeout failure means nothing is serving there.
func portOwner(port int) portOwnerKind {
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	conn, err := net.DialTimeout("tcp", addr, time.Second)
	if err != nil {
		if isTimeout(err) {
			return ownerUnknown
		}
		return ownerSilent
	}
	conn.Close()
	client := http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get("http://" + addr + "/api/connection?contexts=0")
	if err != nil {
		if isTimeout(err) {
			return ownerUnknown
		}
		return ownerOther
	}
	defer resp.Body.Close()
	var status struct {
		State *string `json:"state"`
	}
	if resp.StatusCode == http.StatusOK &&
		json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&status) == nil &&
		status.State != nil {
		return ownerRadar
	}
	return ownerOther
}

func isTimeout(err error) bool {
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

func loopbackPortBindable(port int) bool {
	ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		return false
	}
	ln.Close()
	return true
}

func recordDesktopPort(path string, port int) {
	if path == "" || port <= 0 || port == lastDesktopPort(path) {
		return
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		log.Printf("[desktop] Failed to record port: %v", err)
		return
	}
	tmp, err := os.CreateTemp(dir, desktopPortFile+".*.tmp")
	if err != nil {
		log.Printf("[desktop] Failed to record port: %v", err)
		return
	}
	defer os.Remove(tmp.Name())
	_, werr := tmp.WriteString(strconv.Itoa(port) + "\n")
	cerr := tmp.Close()
	if werr != nil || cerr != nil {
		log.Printf("[desktop] Failed to record port: %v", firstErr(werr, cerr))
		return
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		log.Printf("[desktop] Failed to record port: %v", err)
	}
}

func firstErr(errs ...error) error {
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}
